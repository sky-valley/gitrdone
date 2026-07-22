package intentfs_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentfs"
)

func TestLedgerRestoresAmendmentIdentityRationaleAndIdempotency(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	journalPath := filepath.Join(t.TempDir(), "intent.journal")
	ledger, err := intentfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	repository, err := intent.OpenRepository(ctx, initialContent, ledger, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose original: %v", err)
	}
	request := intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository",
		Rationale:       "timeout path could duplicate the operation",
	}
	amended, err := repository.Amend(ctx, request)
	if err != nil {
		t.Fatalf("amend original: %v", err)
	}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote amendment: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}

	reopened, err := intentfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := intent.OpenRepository(ctx, initialContent, reopened, &recordingAdmission{}, &recordingProjection{current: promoted.Intent.Content})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	inspection, err := restarted.InspectChange(ctx, original.Change.ID)
	if err != nil {
		t.Fatalf("inspect restored change: %v", err)
	}
	if inspection.LatestAmendment == nil || *inspection.LatestAmendment != amended.Amendment {
		t.Fatalf("restored amendment = %#v, want %#v", inspection.LatestAmendment, amended.Amendment)
	}
	if !reflect.DeepEqual(inspection.LatestVersion, amended.Version) {
		t.Fatalf("restored latest version = %#v, want %#v", inspection.LatestVersion, amended.Version)
	}
	if inspection.LatestPromotion == nil || !reflect.DeepEqual(*inspection.LatestPromotion, promoted) {
		t.Fatalf("restored promotion = %#v, want %#v", inspection.LatestPromotion, promoted)
	}
	if current := restarted.CurrentIntent(); current != promoted.Intent {
		t.Fatalf("restored current intent = %#v, want %#v", current, promoted.Intent)
	}
	retried, err := restarted.Amend(ctx, request)
	if err != nil {
		t.Fatalf("retry restored amendment: %v", err)
	}
	if !reflect.DeepEqual(retried, amended) {
		t.Fatalf("restored retry = %#v, want %#v", retried, amended)
	}
}

func TestLedgerRestoresOperationTypedIdempotency(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	journalPath := filepath.Join(t.TempDir(), "intent.journal")
	ledger, err := intentfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	repository, err := intent.OpenRepository(ctx, initialContent, ledger, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	amendment := intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        proposed.Change.ID,
		ExpectedVersion: proposed.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair the proposed change",
	}
	amended, err := repository.Amend(ctx, amendment)
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}

	reopened, err := intentfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := intent.OpenRepository(ctx, initialContent, reopened, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	proposalKeyAmendment := amendment
	proposalKeyAmendment.IdempotencyKey = "proposal-b"
	if _, err := restarted.Amend(ctx, proposalKeyAmendment); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("amendment using restored proposal key error = %v, want ErrIdempotencyConflict", err)
	}
	_, err = restarted.Propose(ctx, intent.Proposal{
		IdempotencyKey: amendment.IdempotencyKey,
		BaseIntent:     amended.Version.BaseIntent,
		Content:        amended.Version.Content,
		Producer:       amended.Version.Producer,
		Dependencies:   amended.Version.Dependencies,
	})
	if !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("proposal using restored amendment key error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestLedgerSerializesPromotionAgainstAmendment(t *testing.T) {
	t.Run("promotion prevents a later amendment", func(t *testing.T) {
		ctx := context.Background()
		initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
		ledger, err := intentfs.Open(filepath.Join(t.TempDir(), "intent.journal"))
		if err != nil {
			t.Fatalf("open ledger: %v", err)
		}
		t.Cleanup(func() { _ = ledger.Close() })
		repository, err := intent.OpenRepository(ctx, initialContent, ledger, &recordingAdmission{}, &recordingProjection{current: initialContent})
		if err != nil {
			t.Fatalf("open repository: %v", err)
		}
		original, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: "proposal-b",
			BaseIntent:     repository.CurrentIntent().ID,
			Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
			Producer:       "ion",
		})
		if err != nil {
			t.Fatalf("propose: %v", err)
		}
		if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: original.Version.ID, ExpectedIntent: original.Version.BaseIntent}); err != nil {
			t.Fatalf("promote: %v", err)
		}
		amendedVersion := original.Version
		amendedVersion.ID = "version_amended"
		amendedVersion.Content.Revision = "cccccccc"
		err = ledger.RecordAmendment(ctx, "amend-b", intent.Amendment{
			FromVersion: original.Version.ID,
			ToVersion:   amendedVersion.ID,
			Rationale:   "repair",
		}, amendedVersion)
		if !errors.Is(err, intent.ErrVersionPromotionStarted) {
			t.Fatalf("record amendment after promotion error = %v, want ErrVersionPromotionStarted", err)
		}
	})

	t.Run("amendment prevents promotion of its source", func(t *testing.T) {
		ctx := context.Background()
		initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
		ledger, err := intentfs.Open(filepath.Join(t.TempDir(), "intent.journal"))
		if err != nil {
			t.Fatalf("open ledger: %v", err)
		}
		t.Cleanup(func() { _ = ledger.Close() })
		repository, err := intent.OpenRepository(ctx, initialContent, ledger, &recordingAdmission{}, &recordingProjection{current: initialContent})
		if err != nil {
			t.Fatalf("open repository: %v", err)
		}
		original, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: "proposal-b",
			BaseIntent:     repository.CurrentIntent().ID,
			Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
			Producer:       "ion",
		})
		if err != nil {
			t.Fatalf("propose: %v", err)
		}
		if _, err := repository.Amend(ctx, intent.AmendRequest{
			IdempotencyKey:  "amend-b",
			ChangeID:        original.Change.ID,
			ExpectedVersion: original.Version.ID,
			Content:         intent.ContentRef{Engine: "git", Revision: "cccccccc"},
			Producer:        "repository-agent",
			Rationale:       "repair",
		}); err != nil {
			t.Fatalf("amend: %v", err)
		}
		current := repository.CurrentIntent()
		err = ledger.PreparePromotion(ctx, intent.PreparedPromotion{
			Promotion: intent.Promotion{
				ID:         "promotion_original",
				FromIntent: current.ID,
				ToIntent:   "intent_original",
				VersionID:  original.Version.ID,
			},
			Intent: intent.Revision{
				ID:         "intent_original",
				PreviousID: current.ID,
				Content:    original.Version.Content,
			},
		})
		if !errors.Is(err, intent.ErrVersionAdvanced) {
			t.Fatalf("prepare source promotion after amendment error = %v, want ErrVersionAdvanced", err)
		}
	})
}
