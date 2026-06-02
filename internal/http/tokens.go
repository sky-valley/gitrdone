package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	ID        string `json:"id"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	GitURL    string `json:"gitUrl"`
	Scope     string `json:"scope"`
	Subject   string `json:"subject"`
}

type repoTokenMetadataResponse struct {
	ID         string  `json:"id"`
	Scope      string  `json:"scope"`
	Subject    string  `json:"subject"`
	CreatedAt  string  `json:"createdAt"`
	ExpiresAt  string  `json:"expiresAt"`
	RevokedAt  *string `json:"revokedAt"`
	LastUsedAt *string `json:"lastUsedAt"`
}

type listRepoTokensResponse struct {
	Tokens []repoTokenMetadataResponse `json:"tokens"`
}

func createRepoTokenHandler(tokens repoTokenCreator, idempotency idempotencyDoer, baseURL string) http.Handler {
	if idempotency == nil {
		idempotency = newMemoryIdempotencyStore(nil)
	}
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
		if err := decodeJSON(w, r, &request); err != nil {
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
		if errMessage := validateTokenSubject(request.Subject); errMessage != "" {
			writeError(w, http.StatusBadRequest, errMessage)
			return
		}
		if request.TTLSeconds < 1 || request.TTLSeconds > maxRepoTokenTTLSeconds {
			writeError(w, http.StatusBadRequest, "ttlSeconds must be between 1 and 604800")
			return
		}

		create := func() (createRepoTokenResponse, error) {
			token, err := tokens.CreateRepoToken(r.Context(), createRepoTokenInput{
				RepoID:     repoID,
				Scope:      request.Scope,
				Subject:    request.Subject,
				TTLSeconds: request.TTLSeconds,
			})
			if err != nil {
				return createRepoTokenResponse{}, err
			}
			return createRepoTokenResponse{
				ID:        formatRepoTokenControlID(token.ID),
				Token:     token.Token,
				ExpiresAt: token.ExpiresAt.Format(time.RFC3339),
				GitURL:    repoGitURL(baseURL, token.RepoID),
				Scope:     token.Scope,
				Subject:   token.Subject,
			}, nil
		}

		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if idempotencyKey != "" {
			result, err := idempotency.Do(r.Context(), idempotencyInput{
				Scope:       createRepoTokenIdempotencyScope(repoID),
				Key:         idempotencyKey,
				RequestHash: createRepoTokenRequestHash(repoID, request),
			}, create)
			if err != nil {
				writeCreateRepoTokenError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, result.Response)
			return
		}

		response, err := create()
		if err != nil {
			writeCreateRepoTokenError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, response)
	})
}

func listRepoTokensHandler(tokens repoTokenLister) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoID, ok := repoIDFromPath(w, r)
		if !ok {
			return
		}
		records, err := tokens.ListRepoTokens(r.Context(), listRepoTokensInput{
			RepoID: repoID,
		})
		if errors.Is(err, errRepoNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "repo tokens could not be listed")
			return
		}

		response := listRepoTokensResponse{
			Tokens: make([]repoTokenMetadataResponse, 0, len(records)),
		}
		for _, record := range records {
			response.Tokens = append(response.Tokens, repoTokenMetadataFromRecord(record))
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func revokeRepoTokenHandler(tokens repoTokenRevoker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoID, ok := repoIDFromPath(w, r)
		if !ok {
			return
		}
		tokenID, ok := parseRepoTokenControlID(r.PathValue("tokenID"))
		if !ok {
			writeError(w, http.StatusBadRequest, "repo token id is invalid")
			return
		}

		record, err := tokens.RevokeRepoToken(r.Context(), revokeRepoTokenInput{
			RepoID:  repoID,
			TokenID: tokenID,
		})
		if errors.Is(err, errRepoNotFound) {
			writeError(w, http.StatusNotFound, "repo not found")
			return
		}
		if errors.Is(err, errRepoTokenNotFound) {
			writeError(w, http.StatusNotFound, "repo token not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "repo token could not be revoked")
			return
		}

		writeJSON(w, http.StatusOK, repoTokenMetadataFromRecord(record))
	})
}

func isRepoTokenScope(scope string) bool {
	return scope == "read" || scope == "write" || scope == "readwrite"
}

func writeCreateRepoTokenError(w http.ResponseWriter, err error) {
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency key already used for a different token request")
		return
	}
	if errors.Is(err, errRepoNotFound) {
		writeError(w, http.StatusNotFound, "repo not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "repo token could not be created")
}

func repoIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	rawRepoID := strings.TrimSpace(r.PathValue("repoID"))
	if rawRepoID == "" {
		writeError(w, http.StatusBadRequest, "repo id is required")
		return "", false
	}
	repoID, ok := parseRepoControlID(rawRepoID)
	if !ok {
		writeError(w, http.StatusBadRequest, "repo id is invalid")
		return "", false
	}
	return repoID, true
}

func createRepoTokenIdempotencyScope(repoID string) string {
	return "POST /v1/repos/" + formatRepoControlID(repoID) + "/tokens"
}

func createRepoTokenRequestHash(repoID string, request createRepoTokenRequest) string {
	payload := struct {
		RepoID     string `json:"repoID"`
		Scope      string `json:"scope"`
		TTLSeconds int    `json:"ttlSeconds"`
		Subject    string `json:"subject"`
	}{
		RepoID:     repoID,
		Scope:      request.Scope,
		TTLSeconds: request.TTLSeconds,
		Subject:    request.Subject,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func repoTokenMetadataFromRecord(record repoTokenRecord) repoTokenMetadataResponse {
	return repoTokenMetadataResponse{
		ID:         formatRepoTokenControlID(record.ID),
		Scope:      record.Scope,
		Subject:    record.Subject,
		CreatedAt:  record.CreatedAt.Format(time.RFC3339),
		ExpiresAt:  record.ExpiresAt.Format(time.RFC3339),
		RevokedAt:  optionalTime(record.RevokedAt),
		LastUsedAt: optionalTime(record.LastUsedAt),
	}
}

func optionalTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.Format(time.RFC3339)
	return &formatted
}
