package intentfs

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/reviewidentity"
)

func (ledger *Ledger) ReviewResponseByIdempotencyKey(ctx context.Context, key string) (intent.ReviewResponse, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.ReviewResponse{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.ReviewResponse{}, false, errors.New("journal is closed")
	}
	record, found := ledger.state.idempotency[key]
	if !found {
		return intent.ReviewResponse{}, false, nil
	}
	if record.operation != reviewResponseOperation {
		return intent.ReviewResponse{}, false, intent.ErrIdempotencyConflict
	}
	response, found := ledger.state.reviewResponseByID[record.reviewID]
	return response, found, nil
}

func (ledger *Ledger) ReviewResponses(ctx context.Context, versionID intent.VersionID) ([]intent.ReviewResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, errors.New("journal is closed")
	}
	return slices.Clone(ledger.state.reviewResponses[versionID]), nil
}

func (ledger *Ledger) PendingReviews(ctx context.Context, reviewer string, after intent.ReviewCursor, limit int) ([]intent.ReviewObligation, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	return pendingReviews(ledger.state, reviewer, after, limit)
}

func (ledger *Ledger) RecordReviewResponse(ctx context.Context, key string, response intent.ReviewResponse) error {
	copy := response
	return ledger.append(ctx, journalRecord{
		Format:         journalFormat,
		Kind:           reviewResponseRecorded,
		IdempotencyKey: key,
		ReviewResponse: &copy,
	})
}

func validateReviewResponse(state *journalState, record journalRecord) error {
	if record.IdempotencyKey == "" || record.ReviewResponse == nil {
		return errors.New("invalid review response record")
	}
	response := *record.ReviewResponse
	if existing, found := state.idempotency[record.IdempotencyKey]; found {
		if existing.operation == reviewResponseOperation && state.reviewResponseByID[existing.reviewID] == response {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	if response.ID == "" || response.VersionID == "" || strings.TrimSpace(response.Concern) == "" || strings.TrimSpace(response.Rationale) == "" {
		return errors.New("invalid review response identity")
	}
	if reviewer, valid := reviewidentity.Canonical(response.Reviewer); !valid || reviewer != response.Reviewer {
		return errors.New("invalid review response reviewer")
	}
	if response.Decision != intent.ReviewApproved && response.Decision != intent.ReviewChangesRequested {
		return errors.New("invalid review response decision")
	}
	if _, exists := state.reviewResponseByID[response.ID]; exists {
		return errors.New("duplicate review response id")
	}
	version, found := state.versions[response.VersionID]
	if !found {
		return intent.ErrVersionNotFound
	}
	ids := state.versionIDs[version.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != version.ID {
		return intent.ErrVersionAdvanced
	}
	if _, found := state.pendingJudgements[version.ID]; !found {
		return intent.ErrVersionPromotionStarted
	}
	assessment, found := state.concernAssessments[version.ID]
	if !found {
		return intent.ErrReviewNotFound
	}
	if assessment.GoverningIntent != state.current.ID {
		return intent.ErrIntentAdvanced
	}
	for _, evaluation := range assessment.Evaluations {
		if evaluation.Concern != response.Concern || !evaluation.RequiresReview {
			continue
		}
		if evaluation.Reviewer != response.Reviewer {
			return intent.ErrReviewNotAssigned
		}
		return nil
	}
	return intent.ErrReviewNotFound
}

func pendingReviews(state journalState, reviewer string, after intent.ReviewCursor, limit int) ([]intent.ReviewObligation, bool, error) {
	afterSet := after.VersionID != "" || after.Concern != ""
	if afterSet && (after.VersionID == "" || after.Concern == "") {
		return nil, false, intent.ErrReviewNotFound
	}
	afterFound := !afterSet
	result := make([]intent.ReviewObligation, 0, limit)
	for _, versionID := range state.judgementIDs {
		assessment, found := state.concernAssessments[versionID]
		if !found {
			continue
		}
		_, stillPending := state.pendingJudgements[versionID]
		visible := stillPending && assessment.GoverningIntent == state.current.ID
		unresolved := unresolvedReviewObligations(assessment, state.reviewResponses[versionID])
		unresolvedByConcern := make(map[string]intent.ReviewObligation, len(unresolved))
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
		return nil, false, intent.ErrReviewNotFound
	}
	return cloneReviewObligations(result), false, nil
}

func unresolvedReviewObligations(assessment intent.ConcernAssessment, responses []intent.ReviewResponse) []intent.ReviewObligation {
	latest := make(map[string]intent.ReviewResponse, len(responses))
	for _, response := range responses {
		latest[response.Concern] = response
	}
	var result []intent.ReviewObligation
	for _, obligation := range assessment.ReviewObligations() {
		response, found := latest[obligation.Concern]
		if found && response.Decision == intent.ReviewApproved {
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

func cloneReviewObligations(obligations []intent.ReviewObligation) []intent.ReviewObligation {
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
