package intent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
)

func TestRepositoryRecordsExactVersionConcernAssessmentAndDerivesReviewObligations(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForJudgement(t, repository, "bbbbbbbb")

	assessment := intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{
			{
				Concern:        "architecture-and-data",
				Prompt:         "Does this change modify architecture or data models?",
				Reviewer:       "noam@example.com",
				RequiresReview: true,
				Reason:         "the candidate adds a database-backed account model",
				Evidence:       []string{"internal/account/model.go", "migrations/001_accounts.sql"},
			},
			{
				Concern:  "copy-and-commercial-impact",
				Prompt:   "Does this change modify copy or commercial behavior?",
				Reviewer: "iris@example.com",
				Reason:   "no user-facing or commercial language changed",
				Evidence: []string{"only internal account storage changed"},
			},
		},
	}
	recorded, err := repository.RecordConcernAssessment(ctx, assessment)
	if err != nil {
		t.Fatalf("record assessment: %v", err)
	}
	if !reflect.DeepEqual(recorded, assessment) {
		t.Fatalf("recorded assessment = %#v, want %#v", recorded, assessment)
	}

	got, found, err := repository.ConcernAssessment(ctx, proposed.Version.ID)
	if err != nil || !found || !reflect.DeepEqual(got, assessment) {
		t.Fatalf("read assessment = %#v, %t, %v; want %#v, true, nil", got, found, err, assessment)
	}
	wantObligations := []intent.ReviewObligation{{
		VersionID: proposed.Version.ID,
		Concern:   "architecture-and-data",
		Reviewer:  "noam@example.com",
		Reason:    "the candidate adds a database-backed account model",
		Evidence:  []string{"internal/account/model.go", "migrations/001_accounts.sql"},
	}}
	if got := assessment.ReviewObligations(); !reflect.DeepEqual(got, wantObligations) {
		t.Fatalf("review obligations = %#v, want %#v", got, wantObligations)
	}

	pending, err := repository.PendingJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending judgement: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("pending judgement = %#v, want held Version %q", pending, proposed.Version.ID)
	}
	runnable, err := repository.RunnableJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable assessment: %v", err)
	}
	if len(runnable.Versions) != 0 {
		t.Fatalf("runnable assessment = %#v, want held Version excluded", runnable)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: proposed.Version.BaseIntent,
	}); !errors.Is(err, intent.ErrReviewRequired) {
		t.Fatalf("direct promotion while review is required error = %v, want ErrReviewRequired", err)
	}
}

func TestRepositoryKeepsClearConcernAssessmentRunnableUntilPromotion(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForJudgement(t, repository, "cccccccc")

	_, err := repository.RecordConcernAssessment(ctx, intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:  "architecture-and-data",
			Prompt:   "Does this change modify architecture or data models?",
			Reviewer: "noam@example.com",
			Reason:   "no architecture, data-model, or infrastructure change",
			Evidence: []string{"README.md typo only"},
		}},
	})
	if err != nil {
		t.Fatalf("record clear assessment: %v", err)
	}
	runnable, err := repository.RunnableJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable assessment: %v", err)
	}
	if len(runnable.Versions) != 1 || runnable.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("runnable assessment = %#v, want clear Version %q", runnable, proposed.Version.ID)
	}

	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: proposed.Version.BaseIntent,
	}); err != nil {
		t.Fatalf("promote clear Version: %v", err)
	}
	runnable, err = repository.RunnableJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable assessment after promotion: %v", err)
	}
	if len(runnable.Versions) != 0 {
		t.Fatalf("runnable assessment after promotion = %#v, want none", runnable)
	}
}

func TestRepositoryDoesNotCarryConcernAssessmentAcrossVersionReplacement(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForJudgement(t, repository, "dddddddd")
	first := intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:        "design-and-user-experience",
			Prompt:         "Does this change modify design or user experience?",
			Reviewer:       "ion@example.com",
			RequiresReview: true,
			Reason:         "the candidate changes the primary navigation",
			Evidence:       []string{"ui/navigation.go"},
		}},
	}
	if _, err := repository.RecordConcernAssessment(ctx, first); err != nil {
		t.Fatalf("record first assessment: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-after-review",
		ChangeID:        proposed.Change.ID,
		ExpectedVersion: proposed.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "eeeeeeee"},
		Producer:        "repository-agent",
		Rationale:       "restore the existing navigation",
	})
	if err != nil {
		t.Fatalf("amend reviewed Version: %v", err)
	}

	if got, found, err := repository.ConcernAssessment(ctx, proposed.Version.ID); err != nil || !found || !reflect.DeepEqual(got, first) {
		t.Fatalf("historical assessment = %#v, %t, %v; want %#v, true, nil", got, found, err, first)
	}
	if _, found, err := repository.ConcernAssessment(ctx, amended.Version.ID); err != nil || found {
		t.Fatalf("replacement assessment found = %t, error = %v; want false, nil", found, err)
	}
	runnable, err := repository.RunnableJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list replacement assessment: %v", err)
	}
	if len(runnable.Versions) != 1 || runnable.Versions[0].ID != amended.Version.ID {
		t.Fatalf("runnable replacement = %#v, want unjudged Version %q", runnable, amended.Version.ID)
	}

	retried, err := repository.RecordConcernAssessment(ctx, intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations:     first.Evaluations,
	})
	if err != nil || !reflect.DeepEqual(retried, first) {
		t.Fatalf("retry historical assessment = %#v, %v; want %#v, nil", retried, err, first)
	}
	conflicting := first
	conflicting.Evaluations = append([]intent.ConcernEvaluation(nil), first.Evaluations...)
	conflicting.Evaluations[0].Reason = "a different result arrived later"
	if _, err := repository.RecordConcernAssessment(ctx, conflicting); !errors.Is(err, intent.ErrConcernAssessmentAlreadyRecorded) {
		t.Fatalf("replace historical assessment error = %v, want ErrConcernAssessmentAlreadyRecorded", err)
	}
}

func TestRepositoryRejectsNoncanonicalConcernIdentity(t *testing.T) {
	repository := newTestRepository(t)
	proposed := proposeForJudgement(t, repository, "abababab")
	_, err := repository.RecordConcernAssessment(context.Background(), intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:  " architecture ",
			Prompt:   "Does this change modify architecture?",
			Reviewer: "noam@example.com",
			Reason:   "architecture changed",
			Evidence: []string{"internal/model.go"},
		}},
	})
	if err == nil {
		t.Fatal("record assessment with noncanonical concern identity succeeded, want error")
	}
}

func TestRepositoryRejectsPartialEvaluatorProvenance(t *testing.T) {
	repository := newTestRepository(t)
	proposed := proposeForJudgement(t, repository, "acacacac")
	_, err := repository.RecordConcernAssessment(context.Background(), intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:    "architecture",
			Prompt:     "Does this change modify architecture?",
			Reviewer:   "noam@example.com",
			Provenance: intent.EvaluatorProvenance{Evaluator: "pi+anthropic://claude-sonnet-5"},
			Reason:     "architecture changed",
			Evidence:   []string{"internal/model.go"},
		}},
	})
	if err == nil {
		t.Fatal("record assessment with partial evaluator provenance succeeded, want error")
	}
}

func TestRepositoryRecordsAssignedHumanApprovalAndReleasesExactVersion(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForJudgement(t, repository, "cdcdcdcd")
	recordBlockingArchitectureAssessment(t, repository, proposed)

	approved, err := repository.RecordReviewResponse(ctx, intent.ReviewResponseRequest{
		IdempotencyKey: "approve-architecture",
		VersionID:      proposed.Version.ID,
		Concern:        "architecture-and-data",
		Reviewer:       "noam@example.com",
		Decision:       intent.ReviewApproved,
		Rationale:      "the migration and rollback plan are acceptable",
	})
	if err != nil {
		t.Fatalf("record approval: %v", err)
	}
	if approved.ID == "" || approved.VersionID != proposed.Version.ID || approved.Decision != intent.ReviewApproved {
		t.Fatalf("approval = %#v", approved)
	}
	retried, err := repository.RecordReviewResponse(ctx, intent.ReviewResponseRequest{
		IdempotencyKey: "approve-architecture",
		VersionID:      proposed.Version.ID,
		Concern:        "architecture-and-data",
		Reviewer:       "noam@example.com",
		Decision:       intent.ReviewApproved,
		Rationale:      "the migration and rollback plan are acceptable",
	})
	if err != nil || !reflect.DeepEqual(retried, approved) {
		t.Fatalf("retry approval = %#v, %v; want %#v, nil", retried, err, approved)
	}

	open, err := repository.PendingReviews(ctx, intent.PendingReviewQuery{Reviewer: "noam@example.com", Limit: 10})
	if err != nil {
		t.Fatalf("list open reviews: %v", err)
	}
	if len(open.Obligations) != 0 {
		t.Fatalf("open reviews after approval = %#v, want none", open)
	}
	runnable, err := repository.RunnableJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable judgements: %v", err)
	}
	if len(runnable.Versions) != 1 || runnable.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("runnable judgements = %#v, want approved Version", runnable)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: proposed.Version.BaseIntent}); err != nil {
		t.Fatalf("promote approved Version: %v", err)
	}
}

func TestRepositoryEnforcesAssignedReviewerAndKeepsChangesRequestedHeld(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForJudgement(t, repository, "dededede")
	recordBlockingArchitectureAssessment(t, repository, proposed)

	_, err := repository.RecordReviewResponse(ctx, intent.ReviewResponseRequest{
		IdempotencyKey: "wrong-reviewer",
		VersionID:      proposed.Version.ID,
		Concern:        "architecture-and-data",
		Reviewer:       "ion@example.com",
		Decision:       intent.ReviewApproved,
		Rationale:      "looks fine",
	})
	if !errors.Is(err, intent.ErrReviewNotAssigned) {
		t.Fatalf("wrong reviewer error = %v, want ErrReviewNotAssigned", err)
	}
	response, err := repository.RecordReviewResponse(ctx, intent.ReviewResponseRequest{
		IdempotencyKey: "request-architecture-changes",
		VersionID:      proposed.Version.ID,
		Concern:        "architecture-and-data",
		Reviewer:       "noam@example.com",
		Decision:       intent.ReviewChangesRequested,
		Rationale:      "add a rollback path before this becomes intent",
	})
	if err != nil {
		t.Fatalf("request changes: %v", err)
	}
	if response.Decision != intent.ReviewChangesRequested {
		t.Fatalf("decision = %q", response.Decision)
	}
	open, err := repository.PendingReviews(ctx, intent.PendingReviewQuery{Reviewer: "noam@example.com", Limit: 10})
	if err != nil {
		t.Fatalf("list open reviews: %v", err)
	}
	if len(open.Obligations) != 1 || open.Obligations[0].Concern != "architecture-and-data" || open.Obligations[0].LatestResponse == nil {
		t.Fatalf("open reviews = %#v, want changes-requested obligation", open)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: proposed.Version.BaseIntent}); !errors.Is(err, intent.ErrReviewRequired) {
		t.Fatalf("promotion after changes requested error = %v, want ErrReviewRequired", err)
	}
	if _, err := repository.RecordReviewResponse(ctx, intent.ReviewResponseRequest{
		IdempotencyKey: "approve-after-changes-requested",
		VersionID:      proposed.Version.ID,
		Concern:        "architecture-and-data",
		Reviewer:       "noam@example.com",
		Decision:       intent.ReviewApproved,
		Rationale:      "rollback path is now documented",
	}); err != nil {
		t.Fatalf("approve after changes requested: %v", err)
	}
	open, err = repository.PendingReviews(ctx, intent.PendingReviewQuery{Reviewer: "noam@example.com", Limit: 10})
	if err != nil || len(open.Obligations) != 0 {
		t.Fatalf("open reviews after later approval = %#v, %v; want none", open, err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: proposed.Version.BaseIntent}); err != nil {
		t.Fatalf("promote after later approval: %v", err)
	}
}

func TestPendingReviewCursorDistinguishesConcernsOnOneVersion(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForJudgement(t, repository, "efefefef")
	_, err := repository.RecordConcernAssessment(ctx, intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{
			{Concern: "architecture", Prompt: "Architecture change?", Reviewer: "noam@example.com", RequiresReview: true, Reason: "architecture changed", Evidence: []string{"architecture.go"}},
			{Concern: "infrastructure", Prompt: "Infrastructure change?", Reviewer: "noam@example.com", RequiresReview: true, Reason: "infrastructure changed", Evidence: []string{"deploy.go"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.PendingReviews(ctx, intent.PendingReviewQuery{Reviewer: "noam@example.com", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Obligations) != 1 || first.Obligations[0].Concern != "architecture" || first.NextCursor.Concern != "architecture" {
		t.Fatalf("first page = %#v", first)
	}
	if _, err := repository.RecordReviewResponse(ctx, intent.ReviewResponseRequest{
		IdempotencyKey: "approve-first-page",
		VersionID:      proposed.Version.ID,
		Concern:        "architecture",
		Reviewer:       "noam@example.com",
		Decision:       intent.ReviewApproved,
		Rationale:      "architecture reviewed",
	}); err != nil {
		t.Fatal(err)
	}
	second, err := repository.PendingReviews(ctx, intent.PendingReviewQuery{Reviewer: "noam@example.com", After: first.NextCursor, Limit: 1})
	if err != nil {
		t.Fatalf("page after resolved cursor: %v", err)
	}
	if len(second.Obligations) != 1 || second.Obligations[0].Concern != "infrastructure" || second.NextCursor.VersionID != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func recordBlockingArchitectureAssessment(t *testing.T, repository *intent.Repository, proposed intent.Proposed) {
	t.Helper()
	_, err := repository.RecordConcernAssessment(context.Background(), intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:        "architecture-and-data",
			Prompt:         "Does this change modify architecture or data models?",
			Reviewer:       "noam@example.com",
			RequiresReview: true,
			Reason:         "the candidate adds a database-backed account model",
			Evidence:       []string{"internal/account/model.go"},
		}},
	})
	if err != nil {
		t.Fatalf("record blocking assessment: %v", err)
	}
}

func newTestRepository(t *testing.T) *intent.Repository {
	t.Helper()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initial, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return repository
}

func proposeForJudgement(t *testing.T, repository *intent.Repository, revision string) intent.Proposed {
	t.Helper()
	proposed, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "propose-" + revision,
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: revision},
		Producer:       "ion@example.com",
	})
	if err != nil {
		t.Fatalf("propose %s: %v", revision, err)
	}
	return proposed
}
