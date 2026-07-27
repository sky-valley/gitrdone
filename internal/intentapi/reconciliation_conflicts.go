package intentapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

const defaultReconciliationConflictPageSize = 50

type reconciliationConflictResponse struct {
	ID            string                            `json:"id"`
	State         string                            `json:"state"`
	Change        changeIdentityResponse            `json:"change"`
	Version       versionResponse                   `json:"version"`
	FromVersion   string                            `json:"fromVersion"`
	ToVersion     string                            `json:"toVersion"`
	ReportedBy    string                            `json:"reportedBy"`
	AffectedPaths []string                          `json:"affectedPaths"`
	Resolution    *reconciliationResolutionResponse `json:"resolution,omitempty"`
}

type reconciliationResolutionResponse struct {
	ID          string `json:"id"`
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	BaseIntent  string `json:"baseIntent"`
	ResolvedBy  string `json:"resolvedBy"`
	Rationale   string `json:"rationale"`
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

func listReconciliationConflictsHandler(service *intentservice.Service) http.Handler {
	type responseBody struct {
		Conflicts  []reconciliationConflictResponse `json:"conflicts"`
		NextCursor string                           `json:"nextCursor,omitempty"`
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit, err := reconciliationConflictPageLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		page, err := service.ReconciliationConflicts(r.Context(), repoID, intent.ReconciliationConflictQuery{
			After: intent.ConflictID(strings.TrimSpace(r.URL.Query().Get("cursor"))),
			Limit: limit,
		})
		switch {
		case err == nil:
		case errors.Is(err, intentservice.ErrRepositoryNotFound):
			writeError(w, http.StatusNotFound, "repository not found")
			return
		case errors.Is(err, intent.ErrReconciliationConflictNotFound):
			writeError(w, http.StatusBadRequest, "reconciliation conflict cursor is invalid")
			return
		default:
			writeError(w, http.StatusInternalServerError, "reconciliation conflicts could not be loaded")
			return
		}
		response := responseBody{
			Conflicts:  make([]reconciliationConflictResponse, 0, len(page.Conflicts)),
			NextCursor: string(page.NextCursor),
		}
		for _, conflict := range page.Conflicts {
			response.Conflicts = append(response.Conflicts, mapReconciliationConflict(conflict))
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func reconciliationConflictPageLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultReconciliationConflictPageSize, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 100 {
		return 0, errors.New("limit must be an integer between 1 and 100")
	}
	return limit, nil
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

func mapReconciliationConflict(conflict intent.ReconciliationConflictInspection) reconciliationConflictResponse {
	response := reconciliationConflictResponse{
		ID:            string(conflict.ID),
		State:         "awaiting_judgement",
		Change:        mapChange(conflict.Change),
		Version:       mapVersion(conflict.Version),
		FromVersion:   string(conflict.FromVersion),
		ToVersion:     string(conflict.ToVersion),
		ReportedBy:    conflict.ReportedBy,
		AffectedPaths: conflict.AffectedPaths,
	}
	if conflict.Resolution != nil {
		response.State = "resolved"
		response.Resolution = &reconciliationResolutionResponse{
			ID:          string(conflict.Resolution.ID),
			FromVersion: string(conflict.Resolution.FromVersion),
			ToVersion:   string(conflict.Resolution.ToVersion),
			BaseIntent:  string(conflict.Resolution.BaseIntent),
			ResolvedBy:  conflict.Resolution.ResolvedBy,
			Rationale:   conflict.Resolution.Rationale,
		}
	}
	return response
}
