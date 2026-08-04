package intent

import (
	"context"
	"errors"
	"testing"
)

func TestTransientLedgerRejectsPromotionPreparedUnderNewerIntentThanAssessment(t *testing.T) {
	ctx := context.Background()
	initial := ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	ledger := &transientLedger{}
	repository, err := OpenRepository(ctx, initial, ledger, acceptingLedgerTestAdmission{}, &ledgerTestProjection{current: initial})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	base := repository.CurrentIntent()
	parent, err := repository.Propose(ctx, Proposal{
		IdempotencyKey: "stale-parent",
		BaseIntent:     base.ID,
		Content:        ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion@example.com",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, Proposal{
		IdempotencyKey: "stale-child",
		BaseIntent:     base.ID,
		Content:        ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion@example.com",
		Dependencies:   []VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	if _, err := repository.RecordConcernAssessment(ctx, ConcernAssessment{
		VersionID:       dependent.Version.ID,
		GoverningIntent: base.ID,
		Evaluations: []ConcernEvaluation{{
			Concern:  "architecture",
			Prompt:   "Does this change modify architecture?",
			Reviewer: "noam@example.com",
			Reason:   "architecture did not change",
			Evidence: []string{"no matching semantic change"},
		}},
	}); err != nil {
		t.Fatalf("record dependent assessment: %v", err)
	}
	if _, err := repository.Promote(ctx, PromoteRequest{VersionID: parent.Version.ID, ExpectedIntent: base.ID}); err != nil {
		t.Fatalf("promote parent: %v", err)
	}
	current := repository.CurrentIntent()
	err = ledger.PreparePromotion(ctx, PreparedPromotion{
		Promotion: Promotion{ID: "promotion_stale", FromIntent: current.ID, ToIntent: "intent_stale", VersionID: dependent.Version.ID},
		Intent:    Revision{ID: "intent_stale", PreviousID: current.ID, Content: dependent.Version.Content},
	})
	if !errors.Is(err, ErrIntentAdvanced) {
		t.Fatalf("prepare promotion with stale assessment error = %v, want ErrIntentAdvanced", err)
	}
}
