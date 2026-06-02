package httpapi

import (
	"errors"
	"net/http"
	"strings"
)

type gitHTTPBackend interface {
	ServeHTTPGit(w http.ResponseWriter, r *http.Request, grant gitAccessGrant) error
}

type notImplementedGitHTTPBackend struct{}

func (notImplementedGitHTTPBackend) ServeHTTPGit(w http.ResponseWriter, r *http.Request, grant gitAccessGrant) error {
	http.Error(w, "git http backend is not implemented", http.StatusNotImplemented)
	return nil
}

func gitSmartHTTPHandler(access gitAccessAuthorizer, backend gitHTTPBackend) http.Handler {
	if backend == nil {
		backend = notImplementedGitHTTPBackend{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoID, ok := parseRepoControlID(r.PathValue("repoID"))
		if !ok {
			http.Error(w, "repo id is invalid", http.StatusBadRequest)
			return
		}
		operation, ok := gitOperationFromRequest(r)
		if !ok {
			http.Error(w, "git route is invalid", http.StatusBadRequest)
			return
		}

		grant, err := access.AuthorizeGitAccess(r.Context(), authorizeGitAccessInput{
			RepoID:    repoID,
			Token:     gitTokenFromRequest(r),
			Operation: operation,
		})
		if err != nil {
			writeGitAccessError(w, err)
			return
		}

		if err := backend.ServeHTTPGit(w, r, grant); err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
	})
}

func gitOperationFromRequest(r *http.Request) (gitOperation, bool) {
	switch r.PathValue("gitPath") {
	case "info/refs":
		if r.Method != http.MethodGet {
			return "", false
		}
		return gitOperationFromService(r.URL.Query().Get("service"))
	case "git-upload-pack":
		if r.Method != http.MethodPost {
			return "", false
		}
		return gitOperationRead, true
	case "git-receive-pack":
		if r.Method != http.MethodPost {
			return "", false
		}
		return gitOperationWrite, true
	default:
		return "", false
	}
}

func gitOperationFromService(service string) (gitOperation, bool) {
	switch service {
	case "git-upload-pack":
		return gitOperationRead, true
	case "git-receive-pack":
		return gitOperationWrite, true
	default:
		return "", false
	}
}

func gitTokenFromRequest(r *http.Request) string {
	username, password, ok := r.BasicAuth()
	if ok {
		if password != "" {
			return password
		}
		return username
	}

	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

func writeGitAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRepoNotFound):
		http.Error(w, "repo not found", http.StatusNotFound)
	case errors.Is(err, errRepoArchived):
		http.Error(w, "repo archived", http.StatusGone)
	case errors.Is(err, errRepoTokenInvalid):
		w.Header().Set("WWW-Authenticate", `Basic realm="giterdone"`)
		http.Error(w, "repo token is required", http.StatusUnauthorized)
	case errors.Is(err, errRepoTokenForbidden):
		http.Error(w, "repo token cannot perform this git operation", http.StatusForbidden)
	case errors.Is(err, errRepoStorageNotFound):
		http.Error(w, "repo storage is unavailable", http.StatusInternalServerError)
	default:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}
