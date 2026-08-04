package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sky-valley/gitrdone/internal/reviewidentity"
)

var ErrReviewNotAssigned = errors.New("review is not assigned to this reviewer")
var ErrReviewNotFound = errors.New("review obligation not found")

type ReviewResponseID string

type ReviewDecision string

const (
	ReviewApproved         ReviewDecision = "approved"
	ReviewChangesRequested ReviewDecision = "changes_requested"
)

type ReviewResponse struct {
	ID        ReviewResponseID
	VersionID VersionID
	Concern   string
	Reviewer  string
	Decision  ReviewDecision
	Rationale string
}

type ReviewResponseRequest struct {
	IdempotencyKey string
	VersionID      VersionID
	Concern        string
	Reviewer       string
	Decision       ReviewDecision
	Rationale      string
}

type PendingReviewQuery struct {
	Reviewer string
	After    ReviewCursor
	Limit    int
}

type ReviewCursor struct {
	VersionID VersionID
	Concern   string
}

type PendingReviewPage struct {
	Obligations []ReviewObligation
	NextCursor  ReviewCursor
}

func (repository *Repository) RecordReviewResponse(ctx context.Context, request ReviewResponseRequest) (ReviewResponse, error) {
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.Concern = strings.TrimSpace(request.Concern)
	request.Reviewer = strings.TrimSpace(request.Reviewer)
	request.Rationale = strings.TrimSpace(request.Rationale)
	if request.IdempotencyKey == "" || request.VersionID == "" || request.Concern == "" || request.Rationale == "" {
		return ReviewResponse{}, errors.New("review response requires idempotency key, Version, concern, and rationale")
	}
	if reviewer, valid := reviewidentity.Canonical(request.Reviewer); !valid || reviewer != request.Reviewer {
		return ReviewResponse{}, ErrReviewNotAssigned
	}
	if request.Decision != ReviewApproved && request.Decision != ReviewChangesRequested {
		return ReviewResponse{}, errors.New("review response decision must be approved or changes_requested")
	}

	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()
	existing, found, err := repository.reviewResponses.ReviewResponseByIdempotencyKey(ctx, request.IdempotencyKey)
	if err != nil {
		return ReviewResponse{}, fmt.Errorf("read review response idempotency: %w", err)
	}
	if found {
		if sameReviewResponseRequest(existing, request) {
			return existing, nil
		}
		return ReviewResponse{}, ErrIdempotencyConflict
	}

	id, err := newID("review_response")
	if err != nil {
		return ReviewResponse{}, fmt.Errorf("create review response id: %w", err)
	}
	response := ReviewResponse{
		ID:        ReviewResponseID(id),
		VersionID: request.VersionID,
		Concern:   request.Concern,
		Reviewer:  request.Reviewer,
		Decision:  request.Decision,
		Rationale: request.Rationale,
	}
	if err := repository.reviewResponses.RecordReviewResponse(ctx, request.IdempotencyKey, response); err != nil {
		return ReviewResponse{}, fmt.Errorf("record review response: %w", err)
	}
	return response, nil
}

func (repository *Repository) PendingReviews(ctx context.Context, query PendingReviewQuery) (PendingReviewPage, error) {
	query.Reviewer = strings.TrimSpace(query.Reviewer)
	if reviewer, valid := reviewidentity.Canonical(query.Reviewer); !valid || reviewer != query.Reviewer {
		return PendingReviewPage{}, ErrReviewNotAssigned
	}
	if query.Limit < 1 || query.Limit > 100 {
		return PendingReviewPage{}, errors.New("pending review page limit must be between 1 and 100")
	}
	obligations, more, err := repository.reviewResponses.PendingReviews(ctx, query.Reviewer, query.After, query.Limit)
	if err != nil {
		return PendingReviewPage{}, fmt.Errorf("read pending reviews: %w", err)
	}
	page := PendingReviewPage{Obligations: obligations}
	if more && len(obligations) > 0 {
		last := obligations[len(obligations)-1]
		page.NextCursor = ReviewCursor{VersionID: last.VersionID, Concern: last.Concern}
	}
	return page, nil
}

func (repository *Repository) UnresolvedReviewObligations(ctx context.Context, versionID VersionID) ([]ReviewObligation, error) {
	assessment, found, err := repository.concernAssessments.ConcernAssessment(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("read Version assessment: %w", err)
	}
	if !found {
		return nil, ErrReviewNotFound
	}
	responses, err := repository.reviewResponses.ReviewResponses(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("read Version review responses: %w", err)
	}
	return cloneReviewObligations(unresolvedReviewObligations(assessment, responses)), nil
}

func unresolvedReviewObligations(assessment ConcernAssessment, responses []ReviewResponse) []ReviewObligation {
	latest := make(map[string]ReviewResponse, len(responses))
	for _, response := range responses {
		latest[response.Concern] = response
	}
	obligations := assessment.ReviewObligations()
	result := make([]ReviewObligation, 0, len(obligations))
	for _, obligation := range obligations {
		response, found := latest[obligation.Concern]
		if found && response.Decision == ReviewApproved {
			continue
		}
		if found {
			copy := response
			obligation.LatestResponse = &copy
		}
		result = append(result, obligation)
	}
	return result
}

func cloneReviewObligations(obligations []ReviewObligation) []ReviewObligation {
	result := slices.Clone(obligations)
	for index := range result {
		result[index].Evidence = slices.Clone(result[index].Evidence)
		if result[index].LatestResponse != nil {
			copy := *result[index].LatestResponse
			result[index].LatestResponse = &copy
		}
	}
	return result
}

func sameReviewResponseRequest(response ReviewResponse, request ReviewResponseRequest) bool {
	return response.VersionID == request.VersionID &&
		response.Concern == request.Concern &&
		response.Reviewer == request.Reviewer &&
		response.Decision == request.Decision &&
		response.Rationale == request.Rationale
}

func (ledger *transientLedger) ReviewResponseByIdempotencyKey(_ context.Context, key string) (ReviewResponse, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return ReviewResponse{}, false, nil
	}
	if record.operation != transientReviewResponseOperation {
		return ReviewResponse{}, false, ErrIdempotencyConflict
	}
	response, found := ledger.reviewResponseByID[record.reviewID]
	return response, found, nil
}

func (ledger *transientLedger) ReviewResponses(_ context.Context, versionID VersionID) ([]ReviewResponse, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	return slices.Clone(ledger.reviewResponses[versionID]), nil
}

func (ledger *transientLedger) PendingReviews(_ context.Context, reviewer string, after ReviewCursor, limit int) ([]ReviewObligation, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	return pendingReviews(
		ledger.judgementIDs,
		ledger.pendingJudgements,
		ledger.concernAssessments,
		ledger.reviewResponses,
		ledger.current.ID,
		reviewer,
		after,
		limit,
	)
}

func (ledger *transientLedger) RecordReviewResponse(_ context.Context, key string, response ReviewResponse) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		if existing.operation == transientReviewResponseOperation && ledger.reviewResponseByID[existing.reviewID] == response {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if err := validateReviewResponseState(
		ledger.current.ID,
		ledger.versions,
		ledger.versionIDs,
		ledger.pendingJudgements,
		ledger.concernAssessments,
		ledger.reviewResponseByID,
		response,
	); err != nil {
		return err
	}
	ledger.reviewResponses[response.VersionID] = append(ledger.reviewResponses[response.VersionID], response)
	ledger.reviewResponseByID[response.ID] = response
	ledger.idempotency[key] = transientIdempotencyRecord{operation: transientReviewResponseOperation, reviewID: response.ID}
	return nil
}

func validateReviewResponseState(
	currentIntent RevisionID,
	versions map[VersionID]Version,
	versionIDs map[ChangeID][]VersionID,
	pending map[VersionID]struct{},
	assessments map[VersionID]ConcernAssessment,
	responses map[ReviewResponseID]ReviewResponse,
	response ReviewResponse,
) error {
	if response.ID == "" || response.VersionID == "" || response.Concern == "" || response.Reviewer == "" || response.Rationale == "" {
		return errors.New("review response requires identity, Version, concern, reviewer, and rationale")
	}
	if response.Decision != ReviewApproved && response.Decision != ReviewChangesRequested {
		return errors.New("invalid review response decision")
	}
	if _, exists := responses[response.ID]; exists {
		return errors.New("duplicate review response id")
	}
	version, found := versions[response.VersionID]
	if !found {
		return ErrVersionNotFound
	}
	ids := versionIDs[version.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != version.ID {
		return ErrVersionAdvanced
	}
	if _, found := pending[version.ID]; !found {
		return ErrVersionPromotionStarted
	}
	assessment, found := assessments[version.ID]
	if !found {
		return ErrReviewNotFound
	}
	if assessment.GoverningIntent != currentIntent {
		return ErrIntentAdvanced
	}
	for _, evaluation := range assessment.Evaluations {
		if evaluation.Concern != response.Concern || !evaluation.RequiresReview {
			continue
		}
		if evaluation.Reviewer != response.Reviewer {
			return ErrReviewNotAssigned
		}
		return nil
	}
	return ErrReviewNotFound
}

func pendingReviews(
	ordered []VersionID,
	pending map[VersionID]struct{},
	assessments map[VersionID]ConcernAssessment,
	responses map[VersionID][]ReviewResponse,
	currentIntent RevisionID,
	reviewer string,
	after ReviewCursor,
	limit int,
) ([]ReviewObligation, bool, error) {
	afterSet := after.VersionID != "" || after.Concern != ""
	if afterSet && (after.VersionID == "" || after.Concern == "") {
		return nil, false, ErrReviewNotFound
	}
	afterFound := !afterSet
	result := make([]ReviewObligation, 0, limit)
	for _, versionID := range ordered {
		assessment, found := assessments[versionID]
		if !found {
			continue
		}
		_, stillPending := pending[versionID]
		visible := stillPending && assessment.GoverningIntent == currentIntent
		unresolved := unresolvedReviewObligations(assessment, responses[versionID])
		unresolvedByConcern := make(map[string]ReviewObligation, len(unresolved))
		for _, obligation := range unresolved {
			unresolvedByConcern[obligation.Concern] = obligation
		}
		for _, assessed := range assessment.ReviewObligations() {
			if assessed.Reviewer != reviewer {
				continue
			}
			if !afterFound {
				if assessed.VersionID == after.VersionID && assessed.Concern == after.Concern {
					afterFound = true
				}
				continue
			}
			if !visible {
				continue
			}
			obligation, open := unresolvedByConcern[assessed.Concern]
			if !open {
				continue
			}
			if len(result) == limit {
				return cloneReviewObligations(result), true, nil
			}
			result = append(result, obligation)
		}
	}
	if !afterFound {
		return nil, false, ErrReviewNotFound
	}
	return cloneReviewObligations(result), false, nil
}
