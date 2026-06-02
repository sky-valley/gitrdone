package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

const maxRepoTokenTTLSeconds = 7 * 24 * 60 * 60

type createRepoTokenRequest struct {
	Scope      string `json:"scope"`
	TTLSeconds int    `json:"ttlSeconds"`
	Subject    string `json:"subject"`
}

type createRepoTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	GitURL    string `json:"gitUrl"`
	Scope     string `json:"scope"`
	Subject   string `json:"subject"`
}

func createRepoTokenHandler(tokens repoTokenCreator, baseURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawRepoID := strings.TrimSpace(r.PathValue("repoID"))
		if rawRepoID == "" {
			writeError(w, http.StatusBadRequest, "repo id is required")
			return
		}
		repoID, ok := parseRepoControlID(rawRepoID)
		if !ok {
			writeError(w, http.StatusBadRequest, "repo id is invalid")
			return
		}

		var request createRepoTokenRequest
		if err := decodeJSON(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be valid JSON for create repo token")
			return
		}

		request.Scope = strings.TrimSpace(request.Scope)
		request.Subject = strings.TrimSpace(request.Subject)
		if !isRepoTokenScope(request.Scope) {
			writeError(w, http.StatusBadRequest, "scope must be read, write, or readwrite")
			return
		}
		if request.Subject == "" {
			writeError(w, http.StatusBadRequest, "subject is required")
			return
		}
		if request.TTLSeconds < 1 || request.TTLSeconds > maxRepoTokenTTLSeconds {
			writeError(w, http.StatusBadRequest, "ttlSeconds must be between 1 and 604800")
			return
		}

		token, err := tokens.CreateRepoToken(r.Context(), createRepoTokenInput{
			RepoID:     repoID,
			Scope:      request.Scope,
			Subject:    request.Subject,
			TTLSeconds: request.TTLSeconds,
		})
		if errors.Is(err, errRepoNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "repo token could not be created")
			return
		}

		writeJSON(w, http.StatusCreated, createRepoTokenResponse{
			Token:     token.Token,
			ExpiresAt: token.ExpiresAt.Format(time.RFC3339),
			GitURL:    repoGitURLWithToken(baseURL, token.RepoID, token.Token),
			Scope:     token.Scope,
			Subject:   token.Subject,
		})
	})
}

func isRepoTokenScope(scope string) bool {
	return scope == "read" || scope == "write" || scope == "readwrite"
}
