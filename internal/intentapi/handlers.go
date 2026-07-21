package intentapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

const maxRequestBodyBytes = 64 * 1024
const defaultVersionPageSize = 50

type Handlers struct {
	CurrentIntent http.Handler
	Bootstrap     http.Handler
	Propose       http.Handler
	GetChange     http.Handler
	ListVersions  http.Handler
}

func NewHandlers(service *intentservice.Service) Handlers {
	return Handlers{
		CurrentIntent: currentIntentHandler(service),
		Bootstrap:     bootstrapHandler(service),
		Propose:       proposeHandler(service),
		GetChange:     getChangeHandler(service),
		ListVersions:  listVersionsHandler(service),
	}
}

func bootstrapHandler(service *intentservice.Service) http.Handler {
	type requestBody struct {
		ContentRef struct {
			Engine   string `json:"engine"`
			Revision string `json:"revision"`
		} `json:"contentRef"`
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body requestBody
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be valid bootstrap JSON")
			return
		}
		body.ContentRef.Engine = strings.TrimSpace(body.ContentRef.Engine)
		body.ContentRef.Revision = strings.TrimSpace(body.ContentRef.Revision)
		if body.ContentRef.Engine == "" || body.ContentRef.Revision == "" {
			writeError(w, http.StatusBadRequest, "contentRef requires engine and revision")
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		initial, err := service.Bootstrap(r.Context(), repoID, intent.ContentRef{
			Engine:   body.ContentRef.Engine,
			Revision: body.ContentRef.Revision,
		})
		switch {
		case err == nil:
		case errors.Is(err, intentservice.ErrRepositoryNotFound):
			writeError(w, http.StatusNotFound, "repository not found")
			return
		case errors.Is(err, intentservice.ErrRepositoryAlreadyInitialized):
			writeError(w, http.StatusConflict, "repository intent is already initialized")
			return
		case errors.Is(err, intent.ErrContentNotAdmissible):
			writeError(w, http.StatusUnprocessableEntity, "contentRef cannot be admitted by this repository engine")
			return
		default:
			writeError(w, http.StatusInternalServerError, "repository intent could not be initialized")
			return
		}
		writeJSON(w, http.StatusOK, mapIntent(initial))
	})
}

type contentRefResponse struct {
	Engine   string `json:"engine"`
	Revision string `json:"revision"`
}

type intentResponse struct {
	ID             string             `json:"id"`
	PreviousIntent string             `json:"previousIntent,omitempty"`
	ContentRef     contentRefResponse `json:"contentRef"`
}

type changeIdentityResponse struct {
	ID string `json:"id"`
}

type changeSummaryResponse struct {
	ID            string             `json:"id"`
	LatestVersion versionResponse    `json:"latestVersion"`
	Promotion     *promotionResponse `json:"promotion,omitempty"`
}

type versionResponse struct {
	ID         string             `json:"id"`
	Change     string             `json:"change"`
	BaseIntent string             `json:"baseIntent"`
	ContentRef contentRefResponse `json:"contentRef"`
	Producer   string             `json:"producer"`
}

type promotionResponse struct {
	ID         string `json:"id"`
	FromIntent string `json:"fromIntent"`
	ToIntent   string `json:"toIntent"`
	Version    string `json:"version"`
}

type proposalReceipt struct {
	Change    changeIdentityResponse `json:"change"`
	Version   versionResponse        `json:"version"`
	State     string                 `json:"state"`
	Promotion *promotionResponse     `json:"promotion,omitempty"`
}

func currentIntentHandler(service *intentservice.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		current, err := service.CurrentIntent(r.Context(), repoID)
		if !writeRepositoryError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, mapIntent(current))
	})
}

func proposeHandler(service *intentservice.Service) http.Handler {
	type requestBody struct {
		BaseIntent string `json:"baseIntent"`
		ContentRef struct {
			Engine   string `json:"engine"`
			Revision string `json:"revision"`
		} `json:"contentRef"`
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if idempotencyKey == "" {
			writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
			return
		}
		var body requestBody
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be valid proposal JSON")
			return
		}
		body.BaseIntent = strings.TrimSpace(body.BaseIntent)
		body.ContentRef.Engine = strings.TrimSpace(body.ContentRef.Engine)
		body.ContentRef.Revision = strings.TrimSpace(body.ContentRef.Revision)
		if body.BaseIntent == "" {
			writeError(w, http.StatusBadRequest, "baseIntent is required")
			return
		}
		if body.ContentRef.Engine == "" || body.ContentRef.Revision == "" {
			writeError(w, http.StatusBadRequest, "contentRef requires engine and revision")
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		admission, err := service.Propose(r.Context(), repoID, intentservice.Proposal{
			IdempotencyKey: idempotencyKey,
			BaseIntent:     intent.RevisionID(body.BaseIntent),
			Content: intent.ContentRef{
				Engine:   body.ContentRef.Engine,
				Revision: body.ContentRef.Revision,
			},
		})
		if err != nil {
			writeProposalError(w, err)
			return
		}

		receipt := proposalReceipt{
			Change:  mapChange(admission.Proposed.Change),
			Version: mapVersion(admission.Proposed.Version),
			State:   "admitted",
		}
		if admission.Promotion != nil {
			mapped := mapPromotion(admission.Promotion.Promotion)
			receipt.Promotion = &mapped
		}

		writeJSON(w, http.StatusOK, receipt)
	})
}

func getChangeHandler(service *intentservice.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changeID := strings.TrimSpace(r.PathValue("changeID"))
		if changeID == "" {
			writeError(w, http.StatusBadRequest, "change id is required")
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		inspection, err := service.InspectChange(r.Context(), repoID, intent.ChangeID(changeID))
		if errors.Is(err, intentservice.ErrRepositoryNotFound) {
			writeError(w, http.StatusNotFound, "repository not found")
			return
		}
		if errors.Is(err, intent.ErrChangeNotFound) {
			writeError(w, http.StatusNotFound, "change not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "change could not be loaded")
			return
		}
		response := changeSummaryResponse{
			ID:            string(inspection.Change.ID),
			LatestVersion: mapVersion(inspection.LatestVersion),
		}
		if inspection.Promotion != nil {
			mapped := mapPromotion(inspection.Promotion.Promotion)
			response.Promotion = &mapped
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func listVersionsHandler(service *intentservice.Service) http.Handler {
	type responseBody struct {
		Versions   []versionResponse `json:"versions"`
		NextCursor string            `json:"nextCursor,omitempty"`
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		changeID := strings.TrimSpace(r.PathValue("changeID"))
		if changeID == "" {
			writeError(w, http.StatusBadRequest, "change id is required")
			return
		}
		limit, err := versionPageLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		repoID, ok := repositoryID(w, r)
		if !ok {
			return
		}
		page, err := service.Versions(r.Context(), repoID, intent.VersionQuery{
			ChangeID: intent.ChangeID(changeID),
			After:    intent.VersionID(strings.TrimSpace(r.URL.Query().Get("cursor"))),
			Limit:    limit,
		})
		switch {
		case err == nil:
		case errors.Is(err, intentservice.ErrRepositoryNotFound):
			writeError(w, http.StatusNotFound, "repository not found")
			return
		case errors.Is(err, intent.ErrChangeNotFound):
			writeError(w, http.StatusNotFound, "change not found")
			return
		case errors.Is(err, intent.ErrVersionNotFound):
			writeError(w, http.StatusBadRequest, "version cursor is invalid")
			return
		default:
			writeError(w, http.StatusInternalServerError, "change versions could not be loaded")
			return
		}
		response := responseBody{
			Versions:   make([]versionResponse, 0, len(page.Versions)),
			NextCursor: string(page.NextCursor),
		}
		for _, version := range page.Versions {
			response.Versions = append(response.Versions, mapVersion(version))
		}
		writeJSON(w, http.StatusOK, response)
	})
}

func repositoryID(w http.ResponseWriter, r *http.Request) (string, bool) {
	repoID := strings.TrimSpace(r.PathValue("repoID"))
	if repoID == "" {
		writeError(w, http.StatusBadRequest, "repo id is required")
		return "", false
	}
	return repoID, true
}

func writeRepositoryError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, intentservice.ErrRepositoryNotFound) {
		writeError(w, http.StatusNotFound, "repository not found")
		return false
	}
	writeError(w, http.StatusInternalServerError, "repository judgement could not be loaded")
	return false
}

func writeProposalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, intentservice.ErrRepositoryNotFound):
		writeError(w, http.StatusNotFound, "repository not found")
	case errors.Is(err, intent.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "Idempotency-Key was already used for a different proposal")
	case errors.Is(err, intent.ErrIntentNotFound):
		writeError(w, http.StatusUnprocessableEntity, "baseIntent does not identify accepted repository intent")
	case errors.Is(err, intent.ErrContentNotAdmissible):
		writeError(w, http.StatusUnprocessableEntity, "contentRef cannot be admitted by this repository engine")
	default:
		writeError(w, http.StatusInternalServerError, "proposal could not be admitted")
	}
}

func versionPageLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultVersionPageSize, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 100 {
		return 0, errors.New("limit must be an integer between 1 and 100")
	}
	return limit, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	reader := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func mapIntent(revision intent.Revision) intentResponse {
	return intentResponse{
		ID:             string(revision.ID),
		PreviousIntent: string(revision.PreviousID),
		ContentRef:     mapContentRef(revision.Content),
	}
}

func mapContentRef(content intent.ContentRef) contentRefResponse {
	return contentRefResponse{Engine: content.Engine, Revision: content.Revision}
}

func mapChange(change intent.Change) changeIdentityResponse {
	return changeIdentityResponse{ID: string(change.ID)}
}

func mapVersion(version intent.Version) versionResponse {
	return versionResponse{
		ID:         string(version.ID),
		Change:     string(version.ChangeID),
		BaseIntent: string(version.BaseIntent),
		ContentRef: mapContentRef(version.Content),
		Producer:   version.Producer,
	}
}

func mapPromotion(promotion intent.Promotion) promotionResponse {
	return promotionResponse{
		ID:         string(promotion.ID),
		FromIntent: string(promotion.FromIntent),
		ToIntent:   string(promotion.ToIntent),
		Version:    string(promotion.VersionID),
	}
}
