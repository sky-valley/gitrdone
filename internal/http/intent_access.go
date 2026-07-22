package httpapi

import (
	"errors"
	"net/http"

	"github.com/sky-valley/gitrdone/internal/intentapi"
)

func repositoryAccessAuth(controlBearer string, access repoAccessAuthorizer, capability repoCapability, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if matchesControlBearer(r, controlBearer) {
			next.ServeHTTP(w, intentapi.WithAuthenticatedProducer(r, "control-api"))
			return
		}
		token := repoTokenFromRequest(r)
		if token == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		repoID, ok := parseRepoControlID(r.PathValue("repoID"))
		if !ok {
			writeError(w, http.StatusBadRequest, "repo id is invalid")
			return
		}
		grant, err := access.AuthorizeRepoAccess(r.Context(), authorizeRepoAccessInput{
			RepoID:     repoID,
			Token:      token,
			Capability: capability,
		})
		if err != nil {
			writeRepoAccessError(w, err)
			return
		}
		next.ServeHTTP(w, intentapi.WithAuthenticatedProducer(r, grant.Subject))
	})
}

func writeRepoAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRepoTokenForbidden):
		writeError(w, http.StatusForbidden, "repo token cannot perform this repository operation")
	case errors.Is(err, errRepoNotFound), errors.Is(err, errRepoArchived), errors.Is(err, errRepoTokenInvalid):
		writeError(w, http.StatusUnauthorized, "repo token is required")
	default:
		writeError(w, http.StatusInternalServerError, "repository authorization failed")
	}
}
