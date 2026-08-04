package intentfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentfs"
)

func TestLedgerRestoresConcernAssessmentAndHumanReviewWait(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "intent.journal")
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}

	firstLedger, err := intentfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	firstRepository, err := intent.OpenRepository(ctx, initial, firstLedger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("open first repository: %v", err)
	}
	proposed, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "assessed-after-restart",
		BaseIntent:     firstRepository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion@example.com",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	assessment := intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:        "prompts-and-models",
			Prompt:         "Does this change modify prompts, LLM usage, or model selection?",
			Reviewer:       "joule@example.com",
			RequiresReview: true,
			Reason:         "the candidate changes the model used for booking assistance",
			Evidence:       []string{"internal/booking/prompt.go", "config/models.go"},
		}},
	}
	if _, err := firstRepository.RecordConcernAssessment(ctx, assessment); err != nil {
		t.Fatalf("record assessment: %v", err)
	}
	if err := firstLedger.PreparePromotion(ctx, intent.PreparedPromotion{
		Promotion: intent.Promotion{
			ID:         "promotion_forbidden",
			FromIntent: proposed.Version.BaseIntent,
			ToIntent:   "intent_forbidden",
			VersionID:  proposed.Version.ID,
		},
		Intent: intent.Revision{
			ID:         "intent_forbidden",
			PreviousID: proposed.Version.BaseIntent,
			Content:    proposed.Version.Content,
		},
	}); !errors.Is(err, intent.ErrReviewRequired) {
		t.Fatalf("prepare promotion behind repository boundary error = %v, want ErrReviewRequired", err)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := intentfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	secondRepository, err := intent.OpenRepository(ctx, initial, secondLedger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	restored, found, err := secondRepository.ConcernAssessment(ctx, proposed.Version.ID)
	if err != nil || !found || !reflect.DeepEqual(restored, assessment) {
		t.Fatalf("restored assessment = %#v, %t, %v; want %#v, true, nil", restored, found, err, assessment)
	}
	pending, err := secondRepository.PendingJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list restored pending: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("restored pending = %#v, want held Version %q", pending, proposed.Version.ID)
	}
	runnable, err := secondRepository.RunnableJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list restored runnable: %v", err)
	}
	if len(runnable.Versions) != 0 {
		t.Fatalf("restored runnable = %#v, want held Version excluded", runnable)
	}
	beforeRetry, err := os.Stat(journalPath)
	if err != nil {
		t.Fatalf("stat journal before exact retry: %v", err)
	}
	if retried, err := secondRepository.RecordConcernAssessment(ctx, assessment); err != nil || !reflect.DeepEqual(retried, assessment) {
		t.Fatalf("retry restored assessment = %#v, %v; want %#v, nil", retried, err, assessment)
	}
	afterRetry, err := os.Stat(journalPath)
	if err != nil {
		t.Fatalf("stat journal after exact retry: %v", err)
	}
	if afterRetry.Size() != beforeRetry.Size() {
		t.Fatalf("journal size after exact retry = %d, want unchanged %d", afterRetry.Size(), beforeRetry.Size())
	}
}

func TestRunnableJudgementPaginationConformsAcrossTransientAndFilesystemLedgers(t *testing.T) {
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	t.Run("transient", func(t *testing.T) {
		repository, err := intent.NewRepository(initial, &recordingAdmission{}, &recordingProjection{current: initial})
		if err != nil {
			t.Fatalf("new transient repository: %v", err)
		}
		want := stageRunnableJudgementHoles(t, repository)
		assertRunnableJudgementPages(t, repository, want)
	})

	t.Run("filesystem before and after replay", func(t *testing.T) {
		ctx := context.Background()
		journalPath := filepath.Join(t.TempDir(), "intent.journal")
		firstLedger, err := intentfs.Open(journalPath)
		if err != nil {
			t.Fatalf("open first ledger: %v", err)
		}
		projection := &recordingProjection{current: initial}
		firstRepository, err := intent.OpenRepository(ctx, initial, firstLedger, &recordingAdmission{}, projection)
		if err != nil {
			t.Fatalf("open first repository: %v", err)
		}
		want := stageRunnableJudgementHoles(t, firstRepository)
		assertRunnableJudgementPages(t, firstRepository, want)
		current := firstRepository.CurrentIntent().Content
		if err := firstLedger.Close(); err != nil {
			t.Fatalf("close first ledger: %v", err)
		}

		secondLedger, err := intentfs.Open(journalPath)
		if err != nil {
			t.Fatalf("reopen ledger: %v", err)
		}
		t.Cleanup(func() { _ = secondLedger.Close() })
		secondRepository, err := intent.OpenRepository(ctx, initial, secondLedger, &recordingAdmission{}, &recordingProjection{current: current})
		if err != nil {
			t.Fatalf("reopen repository: %v", err)
		}
		assertRunnableJudgementPages(t, secondRepository, want)
	})
}

func TestLedgerRejectsPromotionPreparedUnderNewerIntentThanAssessment(t *testing.T) {
	ctx := context.Background()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	ledger, err := intentfs.Open(filepath.Join(t.TempDir(), "intent.journal"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	repository, err := intent.OpenRepository(ctx, initial, ledger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	base := repository.CurrentIntent()
	parent, err := repository.Propose(ctx, intent.Proposal{IdempotencyKey: "stale-parent", BaseIntent: base.ID, Content: intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}, Producer: "ion@example.com"})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{IdempotencyKey: "stale-child", BaseIntent: base.ID, Content: intent.ContentRef{Engine: "git", Revision: "cccccccc"}, Producer: "ion@example.com", Dependencies: []intent.VersionID{parent.Version.ID}})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	if _, err := repository.RecordConcernAssessment(ctx, intent.ConcernAssessment{
		VersionID: dependent.Version.ID, GoverningIntent: base.ID,
		Evaluations: []intent.ConcernEvaluation{{Concern: "architecture", Prompt: "Does this change modify architecture?", Reviewer: "noam@example.com", Reason: "architecture did not change", Evidence: []string{"no matching semantic change"}}},
	}); err != nil {
		t.Fatalf("record dependent assessment: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: parent.Version.ID, ExpectedIntent: base.ID}); err != nil {
		t.Fatalf("promote parent: %v", err)
	}
	current := repository.CurrentIntent()
	err = ledger.PreparePromotion(ctx, intent.PreparedPromotion{
		Promotion: intent.Promotion{ID: "promotion_stale", FromIntent: current.ID, ToIntent: "intent_stale", VersionID: dependent.Version.ID},
		Intent:    intent.Revision{ID: "intent_stale", PreviousID: current.ID, Content: dependent.Version.Content},
	})
	if !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("prepare promotion with stale assessment error = %v, want ErrIntentAdvanced", err)
	}
}

func stageRunnableJudgementHoles(t *testing.T, repository *intent.Repository) []intent.VersionID {
	t.Helper()
	ctx := context.Background()
	base := repository.CurrentIntent()
	propose := func(name, revision string) intent.Proposed {
		t.Helper()
		proposed, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: "runnable-" + name,
			BaseIntent:     base.ID,
			Content:        intent.ContentRef{Engine: "git", Revision: revision},
			Producer:       "ion@example.com",
		})
		if err != nil {
			t.Fatalf("propose %s: %v", name, err)
		}
		return proposed
	}
	promoted := propose("promoted", "bbbbbbbb")
	held := propose("held", "cccccccc")
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: promoted.Version.ID, ExpectedIntent: base.ID}); err != nil {
		t.Fatalf("promote Version creating pagination hole: %v", err)
	}
	base = repository.CurrentIntent()
	clear := propose("clear", "dddddddd")
	superseded := propose("superseded", "eeeeeeee")
	unjudged := propose("unjudged", "ffffffff")
	replacement, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "runnable-replacement",
		ChangeID:        superseded.Change.ID,
		ExpectedVersion: superseded.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "e2e2e2e2"},
		Producer:        "repository-agent@example.com",
		Rationale:       "replace the superseded candidate",
	})
	if err != nil {
		t.Fatalf("replace superseded Version: %v", err)
	}
	record := func(version intent.Version, requiresReview bool, reason string) {
		t.Helper()
		_, err := repository.RecordConcernAssessment(ctx, intent.ConcernAssessment{
			VersionID:       version.ID,
			GoverningIntent: version.BaseIntent,
			Evaluations: []intent.ConcernEvaluation{{
				Concern:        "architecture",
				Prompt:         "Does this change modify architecture?",
				Reviewer:       "noam@example.com",
				RequiresReview: requiresReview,
				Reason:         reason,
				Evidence:       []string{"candidate semantic diff"},
			}},
		})
		if err != nil {
			t.Fatalf("record assessment for %s: %v", version.ID, err)
		}
	}
	record(held.Version, true, "architecture changed")
	record(clear.Version, false, "architecture did not change")
	return []intent.VersionID{clear.Version.ID, unjudged.Version.ID, replacement.Version.ID}
}

func assertRunnableJudgementPages(t *testing.T, repository *intent.Repository, want []intent.VersionID) {
	t.Helper()
	ctx := context.Background()
	var got []intent.VersionID
	var cursor intent.VersionID
	for {
		page, err := repository.RunnableJudgements(ctx, intent.PendingJudgementQuery{After: cursor, Limit: 1})
		if err != nil {
			t.Fatalf("list runnable judgements after %q: %v", cursor, err)
		}
		if len(page.Versions) != 1 {
			t.Fatalf("runnable page after %q = %#v, want one Version", cursor, page)
		}
		got = append(got, page.Versions[0].ID)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("runnable pages = %q, want %q", got, want)
	}
	if _, err := repository.RunnableJudgements(ctx, intent.PendingJudgementQuery{After: "version_missing", Limit: 1}); !errors.Is(err, intent.ErrVersionNotFound) {
		t.Fatalf("invalid runnable cursor error = %v, want ErrVersionNotFound", err)
	}
}
