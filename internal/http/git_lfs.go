package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	lfsMediaType        = "application/vnd.git-lfs+json"
	maxLFSJSONBodyBytes = 1024 * 1024
)

const DefaultMaxLFSObjectBytes int64 = 5 * 1024 * 1024 * 1024

type lfsBatchRequest struct {
	Operation string             `json:"operation"`
	Transfers []string           `json:"transfers"`
	Objects   []lfsObjectRequest `json:"objects"`
}

type lfsObjectRequest struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type lfsBatchResponse struct {
	Transfer string              `json:"transfer,omitempty"`
	Objects  []lfsObjectResponse `json:"objects"`
}

type lfsObjectResponse struct {
	OID     string               `json:"oid"`
	Size    int64                `json:"size"`
	Actions map[string]lfsAction `json:"actions,omitempty"`
	Error   *lfsObjectError      `json:"error,omitempty"`
}

type lfsAction struct {
	Href string `json:"href"`
}

type lfsObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type lfsLocksVerifyResponse struct {
	Ours   []any `json:"ours"`
	Theirs []any `json:"theirs"`
}

func gitLFSHandler(access gitAccessAuthorizer, store lfsObjectStore, maxObjectBytes int64) http.Handler {
	maxObjectBytes = normalizeMaxLFSObjectBytes(maxObjectBytes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoID, ok := parseRepoControlID(r.PathValue("repoID"))
		if !ok {
			writeLFSJSON(w, http.StatusBadRequest, errorResponse{Error: "repo id is invalid"})
			return
		}
		if store == nil {
			writeLFSJSON(w, http.StatusNotImplemented, errorResponse{Error: "git lfs backend is not implemented"})
			return
		}

		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/info/lfs/objects/batch"):
			serveLFSBatch(w, r, access, store, repoID, maxObjectBytes)
		case r.Method == http.MethodPut && r.PathValue("oid") != "":
			serveLFSUpload(w, r, access, store, repoID, maxObjectBytes)
		case r.Method == http.MethodGet && r.PathValue("oid") != "":
			serveLFSDownload(w, r, access, store, repoID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/info/lfs/locks/verify"):
			serveLFSLocksVerify(w, r, access, repoID)
		default:
			writeLFSJSON(w, http.StatusBadRequest, errorResponse{Error: "git lfs route is invalid"})
		}
	})
}

func serveLFSBatch(w http.ResponseWriter, r *http.Request, access gitAccessAuthorizer, store lfsObjectStore, repoID string, maxObjectBytes int64) {
	var request lfsBatchRequest
	if err := decodeLFSJSON(w, r, &request); err != nil {
		writeLFSJSON(w, http.StatusBadRequest, errorResponse{Error: "request body must be valid JSON for git lfs batch"})
		return
	}

	var operation gitOperation
	switch request.Operation {
	case "download":
		operation = gitOperationRead
	case "upload":
		operation = gitOperationWrite
	default:
		writeLFSJSON(w, http.StatusBadRequest, errorResponse{Error: "git lfs operation is invalid"})
		return
	}

	grant, err := authorizeLFSAccess(r, access, repoID, operation)
	if err != nil {
		writeLFSAccessError(w, err)
		return
	}

	objects := make([]lfsObjectResponse, 0, len(request.Objects))
	for _, object := range request.Objects {
		oid, ok := parseLFSOID(object.OID)
		if !ok || object.Size < 0 {
			writeLFSJSON(w, http.StatusBadRequest, errorResponse{Error: "git lfs object is invalid"})
			return
		}

		response := lfsObjectResponse{OID: oid, Size: object.Size}
		switch request.Operation {
		case "upload":
			if object.Size > maxObjectBytes {
				response.Error = &lfsObjectError{Code: http.StatusRequestEntityTooLarge, Message: "object is too large"}
				objects = append(objects, response)
				continue
			}
			exists, err := store.Exists(r.Context(), grant.RepoID, oid, object.Size)
			if err != nil {
				writeLFSJSON(w, http.StatusInternalServerError, errorResponse{Error: http.StatusText(http.StatusInternalServerError)})
				return
			}
			if !exists {
				response.Actions = map[string]lfsAction{
					"upload": {Href: lfsObjectURL(r, oid)},
				}
			}
		case "download":
			exists, err := store.Exists(r.Context(), grant.RepoID, oid, object.Size)
			if err != nil {
				writeLFSJSON(w, http.StatusInternalServerError, errorResponse{Error: http.StatusText(http.StatusInternalServerError)})
				return
			}
			if exists {
				response.Actions = map[string]lfsAction{
					"download": {Href: lfsObjectURL(r, oid)},
				}
			} else {
				response.Error = &lfsObjectError{Code: http.StatusNotFound, Message: "object not found"}
			}
		}
		objects = append(objects, response)
	}

	writeLFSJSON(w, http.StatusOK, lfsBatchResponse{
		Transfer: "basic",
		Objects:  objects,
	})
}

func serveLFSUpload(w http.ResponseWriter, r *http.Request, access gitAccessAuthorizer, store lfsObjectStore, repoID string, maxObjectBytes int64) {
	grant, err := authorizeLFSAccess(r, access, repoID, gitOperationWrite)
	if err != nil {
		writeLFSAccessError(w, err)
		return
	}
	oid, ok := parseLFSOID(r.PathValue("oid"))
	if !ok {
		writeLFSJSON(w, http.StatusBadRequest, errorResponse{Error: "git lfs object id is invalid"})
		return
	}
	if r.ContentLength < 0 {
		writeLFSJSON(w, http.StatusLengthRequired, errorResponse{Error: "git lfs object size is required"})
		return
	}
	if r.ContentLength > maxObjectBytes {
		writeLFSJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "git lfs object is too large"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxObjectBytes)
	if err := store.Put(r.Context(), grant.RepoID, oid, r.ContentLength, maxObjectBytes, r.Body); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeLFSJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "git lfs object is too large"})
			return
		}
		if errors.Is(err, errLFSObjectTooLarge) {
			writeLFSJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "git lfs object is too large"})
			return
		}
		if errors.Is(err, errLFSObjectInvalid) {
			writeLFSJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "git lfs object does not match oid or size"})
			return
		}
		writeLFSJSON(w, http.StatusInternalServerError, errorResponse{Error: http.StatusText(http.StatusInternalServerError)})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func serveLFSDownload(w http.ResponseWriter, r *http.Request, access gitAccessAuthorizer, store lfsObjectStore, repoID string) {
	grant, err := authorizeLFSAccess(r, access, repoID, gitOperationRead)
	if err != nil {
		writeLFSAccessError(w, err)
		return
	}
	oid, ok := parseLFSOID(r.PathValue("oid"))
	if !ok {
		writeLFSJSON(w, http.StatusBadRequest, errorResponse{Error: "git lfs object id is invalid"})
		return
	}
	object, err := store.Open(r.Context(), grant.RepoID, oid)
	if err != nil {
		if errors.Is(err, errLFSObjectNotFound) {
			writeLFSJSON(w, http.StatusNotFound, errorResponse{Error: "git lfs object was not found"})
			return
		}
		writeLFSJSON(w, http.StatusInternalServerError, errorResponse{Error: http.StatusText(http.StatusInternalServerError)})
		return
	}
	defer object.Body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(object.Size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, object.Body)
}

func serveLFSLocksVerify(w http.ResponseWriter, r *http.Request, access gitAccessAuthorizer, repoID string) {
	if _, err := authorizeLFSAccess(r, access, repoID, gitOperationWrite); err != nil {
		writeLFSAccessError(w, err)
		return
	}
	writeLFSJSON(w, http.StatusOK, lfsLocksVerifyResponse{
		Ours:   []any{},
		Theirs: []any{},
	})
}

func authorizeLFSAccess(r *http.Request, access gitAccessAuthorizer, repoID string, operation gitOperation) (gitAccessGrant, error) {
	return access.AuthorizeGitAccess(r.Context(), authorizeGitAccessInput{
		RepoID:    repoID,
		Token:     repoTokenFromRequest(r),
		Operation: operation,
	})
}

func decodeLFSJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxLFSJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain a single JSON value")
		}
		return err
	}
	return nil
}

func writeLFSAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRepoNotFound):
		writeLFSUnauthorized(w)
	case errors.Is(err, errRepoArchived):
		writeLFSUnauthorized(w)
	case errors.Is(err, errRepoTokenInvalid):
		writeLFSUnauthorized(w)
	case errors.Is(err, errRepoTokenForbidden):
		writeLFSJSON(w, http.StatusForbidden, errorResponse{Error: "repo token cannot perform this git lfs operation"})
	case errors.Is(err, errRepoStorageNotFound):
		writeLFSJSON(w, http.StatusInternalServerError, errorResponse{Error: "repo storage is unavailable"})
	default:
		writeLFSJSON(w, http.StatusInternalServerError, errorResponse{Error: http.StatusText(http.StatusInternalServerError)})
	}
}

func writeLFSUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="gitrdone"`)
	writeLFSJSON(w, http.StatusUnauthorized, errorResponse{Error: "repo token is required"})
}

func writeLFSJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", lfsMediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func parseLFSOID(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256HexLength {
		return "", false
	}
	for _, char := range value {
		if isASCIIDigit(char) || (char >= 'a' && char <= 'f') {
			continue
		}
		return "", false
	}
	return value, true
}

const sha256HexLength = 64

func lfsObjectURL(r *http.Request, oid string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto == "http" || forwardedProto == "https" {
		scheme = forwardedProto
	}
	repoPrefix, _, _ := strings.Cut(r.URL.Path, "/info/lfs/")
	return fmt.Sprintf("%s://%s%s/info/lfs/objects/%s", scheme, r.Host, repoPrefix, oid)
}

func normalizeMaxLFSObjectBytes(value int64) int64 {
	if value <= 0 {
		return DefaultMaxLFSObjectBytes
	}
	return value
}
