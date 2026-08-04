package intentapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
	"github.com/sky-valley/gitrdone/internal/requestauth"
)

type reviewResponseBody struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Concern   string `json:"concern"`
	Reviewer  string `json:"reviewer"`
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
}

type reviewObligationResponse struct {
	Version        string              `json:"version"`
	Concern        string              `json:"concern"`
	Reviewer       string              `json:"reviewer"`
	Reason         string              `json:"reason"`
	Evidence       []string            `json:"evidence"`
	LatestResponse *reviewResponseBody `json:"latestResponse,omitempty"`
}

func listReviewsHandler(service *intentservice.Service) http.Handler {
	type responseBody struct {
		Reviews    []reviewObligationResponse `json:"reviews"`
		NextCursor string                     `json:"nextCursor,omitempty"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit, err := versionPageLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		cursor, err := decodeReviewCursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
		if err != nil {
			writeError(w, http.StatusBadRequest, "review cursor is invalid")
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		page, err := service.PendingReviews(r.Context(), repoID, intent.PendingReviewQuery{
			Reviewer: requestauth.Subject(r),
			After:    cursor,
			Limit:    limit,
		})
		switch {
		case err == nil:
		case errors.Is(err, intentservice.ErrRepositoryNotFound):
			writeError(w, http.StatusNotFound, "repository not found")
			return
		case errors.Is(err, intent.ErrReviewNotAssigned):
			writeError(w, http.StatusForbidden, "review is not assigned to this identity")
			return
		case errors.Is(err, intent.ErrReviewNotFound):
			writeError(w, http.StatusBadRequest, "review cursor is invalid")
			return
		default:
			writeError(w, http.StatusInternalServerError, "assigned reviews could not be loaded")
			return
		}
		response := responseBody{Reviews: make([]reviewObligationResponse, 0, len(page.Obligations))}
		for _, obligation := range page.Obligations {
			response.Reviews = append(response.Reviews, mapReviewObligation(obligation))
		}
		if page.NextCursor.VersionID != "" {
			response.NextCursor = encodeReviewCursor(page.NextCursor)
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func recordReviewResponseHandler(service *intentservice.Service) http.Handler {
	type requestBody struct {
		Version   string `json:"version"`
		Concern   string `json:"concern"`
		Decision  string `json:"decision"`
		Rationale string `json:"rationale"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
			return
		}
		var body requestBody
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be valid review response JSON")
			return
		}
		body.Version = strings.TrimSpace(body.Version)
		body.Concern = strings.TrimSpace(body.Concern)
		body.Decision = strings.TrimSpace(body.Decision)
		body.Rationale = strings.TrimSpace(body.Rationale)
		if body.Version == "" || body.Concern == "" || body.Rationale == "" {
			writeError(w, http.StatusBadRequest, "version, concern, and rationale are required")
			return
		}
		if body.Decision != string(intent.ReviewApproved) && body.Decision != string(intent.ReviewChangesRequested) {
			writeError(w, http.StatusBadRequest, "decision must be approved or changes_requested")
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		response, err := service.RecordReviewResponse(r.Context(), repoID, intent.ReviewResponseRequest{
			IdempotencyKey: idempotencyKey,
			VersionID:      intent.VersionID(body.Version),
			Concern:        body.Concern,
			Reviewer:       requestauth.Subject(r),
			Decision:       intent.ReviewDecision(body.Decision),
			Rationale:      body.Rationale,
		})
		switch {
		case err == nil:
		case errors.Is(err, intentservice.ErrRepositoryNotFound), errors.Is(err, intent.ErrVersionNotFound), errors.Is(err, intent.ErrReviewNotFound):
			writeError(w, http.StatusNotFound, "review obligation not found")
			return
		case errors.Is(err, intent.ErrReviewNotAssigned):
			writeError(w, http.StatusForbidden, "review is not assigned to this identity")
			return
		case errors.Is(err, intent.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "Idempotency-Key was already used for a different operation")
			return
		case errors.Is(err, intent.ErrVersionAdvanced), errors.Is(err, intent.ErrVersionPromotionStarted), errors.Is(err, intent.ErrIntentAdvanced):
			writeError(w, http.StatusConflict, "review obligation is no longer current")
			return
		default:
			writeError(w, http.StatusInternalServerError, "review response could not be recorded")
			return
		}
		writeJSON(w, http.StatusOK, mapReviewResponse(response))
	})
}

func mapReviewObligation(obligation intent.ReviewObligation) reviewObligationResponse {
	response := reviewObligationResponse{
		Version:  string(obligation.VersionID),
		Concern:  obligation.Concern,
		Reviewer: obligation.Reviewer,
		Reason:   obligation.Reason,
		Evidence: append([]string(nil), obligation.Evidence...),
	}
	if obligation.LatestResponse != nil {
		mapped := mapReviewResponse(*obligation.LatestResponse)
		response.LatestResponse = &mapped
	}
	return response
}

func mapReviewResponse(response intent.ReviewResponse) reviewResponseBody {
	return reviewResponseBody{
		ID:        string(response.ID),
		Version:   string(response.VersionID),
		Concern:   response.Concern,
		Reviewer:  response.Reviewer,
		Decision:  string(response.Decision),
		Rationale: response.Rationale,
	}
}

func decodeReviewCursor(cursor string) (intent.ReviewCursor, error) {
	if cursor == "" {
		return intent.ReviewCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return intent.ReviewCursor{}, err
	}
	version, concern, found := strings.Cut(string(decoded), "\x00")
	if !found || version == "" || concern == "" {
		return intent.ReviewCursor{}, errors.New("invalid review cursor")
	}
	return intent.ReviewCursor{VersionID: intent.VersionID(version), Concern: concern}, nil
}

func encodeReviewCursor(cursor intent.ReviewCursor) string {
	value := string(cursor.VersionID) + "\x00" + cursor.Concern
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}
