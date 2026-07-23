package intentapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

type reconciliationConflictResponse struct {
	ID            string                 `json:"id"`
	State         string                 `json:"state"`
	Change        changeIdentityResponse `json:"change"`
	Version       versionResponse        `json:"version"`
	FromVersion   string                 `json:"fromVersion"`
	ToVersion     string                 `json:"toVersion"`
	ReportedBy    string                 `json:"reportedBy"`
	AffectedPaths []string               `json:"affectedPaths"`
}

func recordReconciliationConflictHandler(service *intentservice.Service) http.Handler {
	type requestBody struct {
		FromVersion       string   `json:"fromVersion"`
		ToVersion         string   `json:"toVersion"`
		DescendantVersion string   `json:"descendantVersion"`
		AffectedPaths     []string `json:"affectedPaths"`
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
			return
		}
		var body requestBody
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be valid reconciliation conflict JSON")
			return
		}
		body.FromVersion = strings.TrimSpace(body.FromVersion)
		body.ToVersion = strings.TrimSpace(body.ToVersion)
		body.DescendantVersion = strings.TrimSpace(body.DescendantVersion)
		if body.FromVersion == "" || body.ToVersion == "" || body.DescendantVersion == "" {
			writeError(w, http.StatusBadRequest, "fromVersion, toVersion, and descendantVersion are required")
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		conflict, err := service.RecordReconciliationConflict(r.Context(), repoID, intentservice.ReconciliationConflictRequest{
			IdempotencyKey:    idempotencyKey,
			FromVersion:       intent.VersionID(body.FromVersion),
			ToVersion:         intent.VersionID(body.ToVersion),
			DescendantVersion: intent.VersionID(body.DescendantVersion),
			ReportedBy:        authenticatedProducer(r),
			AffectedPaths:     body.AffectedPaths,
		})
		if !writeReconciliationConflictError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, mapReconciliationConflict(conflict))
	})
}

func getReconciliationConflictHandler(service *intentservice.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conflictID := strings.TrimSpace(r.PathValue("conflictID"))
		if conflictID == "" {
			writeError(w, http.StatusBadRequest, "conflict id is required")
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		conflict, err := service.ReconciliationConflict(r.Context(), repoID, intent.ConflictID(conflictID))
		if !writeReconciliationConflictError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, mapReconciliationConflict(conflict))
	})
}

func writeReconciliationConflictError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, intentservice.ErrRepositoryNotFound),
		errors.Is(err, intent.ErrVersionNotFound),
		errors.Is(err, intent.ErrChangeNotFound),
		errors.Is(err, intent.ErrReconciliationConflictNotFound):
		writeError(w, http.StatusNotFound, "reconciliation conflict input was not found")
	case errors.Is(err, intent.ErrInvalidReconciliationConflict):
		writeError(w, http.StatusBadRequest, "reconciliation conflict is invalid")
	case errors.Is(err, intent.ErrIdempotencyConflict),
		errors.Is(err, intent.ErrInvalidReconciliationLineage),
		errors.Is(err, intent.ErrIntentAdvanced),
		errors.Is(err, intent.ErrVersionAdvanced),
		errors.Is(err, intent.ErrVersionNotPromoted):
		writeError(w, http.StatusConflict, "reconciliation conflict cannot be recorded")
	default:
		writeError(w, http.StatusInternalServerError, "reconciliation conflict could not be processed")
	}
	return false
}

func mapReconciliationConflict(conflict intent.ReconciliationConflict) reconciliationConflictResponse {
	return reconciliationConflictResponse{
		ID:            string(conflict.ID),
		State:         "awaiting_judgement",
		Change:        mapChange(conflict.Change),
		Version:       mapVersion(conflict.Version),
		FromVersion:   string(conflict.FromVersion),
		ToVersion:     string(conflict.ToVersion),
		ReportedBy:    conflict.ReportedBy,
		AffectedPaths: conflict.AffectedPaths,
	}
}
