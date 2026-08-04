package judgement_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/judgement"
)

func TestConcernProcessorRecordsAllMatchingHumanReviewObligationsAgainstExactVersion(t *testing.T) {
	version, governing := judgementFixture()
	service := &recordingConcernService{context: intent.ConcernAssessmentContext{Version: version, GoverningIntent: governing}}
	evaluator := &recordingEvaluator{results: map[string]judgement.ConcernResult{
		"architecture-data-infrastructure": {
			RequiresReview: true,
			Reason:         "adds a persistent reservation model and DATABASE_URL",
			Evidence:       []string{"internal/reservation/model.go", "cmd/app/config.go"},
		},
		"design-system-user-experience": {
			RequiresReview: true,
			Reason:         "changes the booking form interaction",
			Evidence:       []string{"web/booking-form.tsx"},
		},
		"copy-commercial-impact": {
			Reason:   "no copy or commercial behavior changed",
			Evidence: []string{"no customer-facing text in the candidate diff"},
		},
		"prompts-models": {
			Reason:   "no prompt, model, or LLM use changed",
			Evidence: []string{"no model integration files in the candidate diff"},
		},
	}}
	processor := newFourConcernProcessor(t, service, evaluator)

	if err := processor.Process(context.Background(), judgement.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("process Version: %v", err)
	}
	if len(evaluator.requests) != 4 {
		t.Fatalf("evaluation requests = %d, want 4", len(evaluator.requests))
	}
	for _, request := range evaluator.requests {
		if request.RepoID != "repo_app" || !reflect.DeepEqual(request.Version, version) || request.GoverningIntent != governing {
			t.Fatalf("evaluation request = %#v, want exact repo, Version, and governing Intent", request)
		}
	}
	want := intent.ConcernAssessment{
		VersionID:       version.ID,
		GoverningIntent: governing.ID,
		Evaluations: []intent.ConcernEvaluation{
			{
				Concern:        "architecture-data-infrastructure",
				Prompt:         "Does this change modify architecture, data models, or infrastructure requirements?",
				Reviewer:       "noam@example.com",
				RequiresReview: true,
				Reason:         "adds a persistent reservation model and DATABASE_URL",
				Evidence:       []string{"internal/reservation/model.go", "cmd/app/config.go"},
			},
			{
				Concern:        "design-system-user-experience",
				Prompt:         "Does this change modify the design system or user experience?",
				Reviewer:       "yon@example.com",
				RequiresReview: true,
				Reason:         "changes the booking form interaction",
				Evidence:       []string{"web/booking-form.tsx"},
			},
			{
				Concern:  "copy-commercial-impact",
				Prompt:   "Does this change modify copywriting or commercial behavior?",
				Reviewer: "iris@example.com",
				Reason:   "no copy or commercial behavior changed",
				Evidence: []string{"no customer-facing text in the candidate diff"},
			},
			{
				Concern:  "prompts-models",
				Prompt:   "Does this change modify prompts, LLM usage, or model selection?",
				Reviewer: "joule@example.com",
				Reason:   "no prompt, model, or LLM use changed",
				Evidence: []string{"no model integration files in the candidate diff"},
			},
		},
	}
	if !reflect.DeepEqual(service.recorded, want) {
		t.Fatalf("recorded assessment = %#v, want %#v", service.recorded, want)
	}
	if len(service.promotions) != 0 {
		t.Fatalf("promotions = %#v, want none while human reviews are required", service.promotions)
	}
	obligations := service.recorded.ReviewObligations()
	if len(obligations) != 2 || obligations[0].Reviewer != "noam@example.com" || obligations[1].Reviewer != "yon@example.com" {
		t.Fatalf("review obligations = %#v, want Noam and Yon", obligations)
	}
}

func TestConcernProcessorPromotesOnlyAfterDurableClearAssessment(t *testing.T) {
	version, governing := judgementFixture()
	service := &recordingConcernService{context: intent.ConcernAssessmentContext{Version: version, GoverningIntent: governing}}
	evaluator := &recordingEvaluator{defaultResult: judgement.ConcernResult{
		Reason:   "the concern does not apply",
		Evidence: []string{"candidate diff contains no matching semantic change"},
	}}
	processor := newFourConcernProcessor(t, service, evaluator)

	if err := processor.Process(context.Background(), judgement.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("process Version: %v", err)
	}
	if service.recordSequence != 1 || service.promoteSequence != 2 {
		t.Fatalf("effect order = record %d, promote %d; want record 1, promote 2", service.recordSequence, service.promoteSequence)
	}
	if len(service.promotions) != 1 || service.promotions[0] != (intent.PromoteRequest{VersionID: version.ID, ExpectedIntent: governing.ID}) {
		t.Fatalf("promotions = %#v, want exact governed Version promotion", service.promotions)
	}
}

func TestConcernProcessorResumesPersistedAssessmentWithoutReevaluating(t *testing.T) {
	version, governing := judgementFixture()
	existing := intent.ConcernAssessment{
		VersionID:       version.ID,
		GoverningIntent: governing.ID,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:  "architecture-data-infrastructure",
			Prompt:   "Does this change modify architecture, data models, or infrastructure requirements?",
			Reviewer: "noam@example.com",
			Reason:   "the concern does not apply",
			Evidence: []string{"README-only change"},
		}},
	}
	service := &recordingConcernService{existing: existing, existingFound: true}
	evaluator := &recordingEvaluator{err: errors.New("must not be called")}
	processor := newFourConcernProcessor(t, service, evaluator)

	if err := processor.Process(context.Background(), judgement.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("resume persisted assessment: %v", err)
	}
	if len(evaluator.requests) != 0 || service.recorded.VersionID != "" {
		t.Fatalf("resume reevaluated or rerecorded: requests %#v, recorded %#v", evaluator.requests, service.recorded)
	}
	if len(service.promotions) != 1 || service.promotions[0].ExpectedIntent != governing.ID {
		t.Fatalf("resumed promotions = %#v, want persisted governing Intent", service.promotions)
	}
}

func TestConcernProcessorLeavesVersionUnassessedWhenAnArmFails(t *testing.T) {
	version, governing := judgementFixture()
	service := &recordingConcernService{context: intent.ConcernAssessmentContext{Version: version, GoverningIntent: governing}}
	evaluator := &recordingEvaluator{err: errors.New("model unavailable")}
	processor := newFourConcernProcessor(t, service, evaluator)

	err := processor.Process(context.Background(), judgement.WorkItem{RepoID: "repo_app", VersionID: version.ID})
	if err == nil || !errors.Is(err, evaluator.err) {
		t.Fatalf("process error = %v, want model failure", err)
	}
	if service.recorded.VersionID != "" || len(service.promotions) != 0 {
		t.Fatalf("failed evaluation recorded or promoted: %#v, %#v", service.recorded, service.promotions)
	}
}

func TestConcernProcessorIsolatesExactVersionInputBetweenEvaluatorArms(t *testing.T) {
	version, governing := judgementFixture()
	version.Dependencies = []intent.VersionID{"version_parent"}
	wantDependencies := append([]intent.VersionID(nil), version.Dependencies...)
	service := &recordingConcernService{context: intent.ConcernAssessmentContext{Version: version, GoverningIntent: governing}}
	evaluator := &mutatingEvaluator{}
	processor, err := judgement.NewConcernProcessor(service, evaluator, []judgement.Concern{
		{Name: "first", Prompt: "Does the first concern apply?", Reviewer: "first@example.com"},
		{Name: "second", Prompt: "Does the second concern apply?", Reviewer: "second@example.com"},
	})
	if err != nil {
		t.Fatalf("new concern processor: %v", err)
	}

	if err := processor.Process(context.Background(), judgement.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("process Version: %v", err)
	}
	if !reflect.DeepEqual(evaluator.secondDependencies, wantDependencies) {
		t.Fatalf("second arm dependencies = %q, want exact original %q", evaluator.secondDependencies, wantDependencies)
	}
}

func TestConcernProcessorCanonicalizesReviewerAuthority(t *testing.T) {
	version, governing := judgementFixture()
	service := &recordingConcernService{context: intent.ConcernAssessmentContext{Version: version, GoverningIntent: governing}}
	processor, err := judgement.NewConcernProcessor(service, &recordingEvaluator{defaultResult: judgement.ConcernResult{
		RequiresReview: true,
		Reason:         "the candidate changes architecture",
		Evidence:       []string{"internal/model.go"},
	}}, []judgement.Concern{{
		Name:     "architecture",
		Prompt:   "Does this change modify architecture?",
		Reviewer: " Noam+GitRDone@Company.Example ",
	}})
	if err != nil {
		t.Fatalf("new concern processor: %v", err)
	}
	if err := processor.Process(context.Background(), judgement.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("process Version: %v", err)
	}
	obligations := service.recorded.ReviewObligations()
	if len(obligations) != 1 || obligations[0].Reviewer != "noam+gitrdone@company.example" {
		t.Fatalf("review obligations = %#v, want canonical reviewer authority", obligations)
	}
}

func TestConcernProcessorDoesNotTreatOldAssessmentAsAuthorityAfterParentAdvancesIntent(t *testing.T) {
	ctx := context.Background()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &processorProjection{current: initial}
	repository, err := intent.NewRepository(initial, acceptingContent{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	base := repository.CurrentIntent()
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "dependent-parent",
		BaseIntent:     base.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion@example.com",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "dependent-child",
		BaseIntent:     base.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion@example.com",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	service := &repositoryConcernService{repository: repository}
	processor, err := judgement.NewConcernProcessor(service, &recordingEvaluator{defaultResult: judgement.ConcernResult{
		Reason:   "the concern does not apply",
		Evidence: []string{"no matching semantic change"},
	}}, []judgement.Concern{{
		Name:     "architecture-data-infrastructure",
		Prompt:   "Does this change modify architecture, data models, or infrastructure requirements?",
		Reviewer: "noam@example.com",
	}})
	if err != nil {
		t.Fatalf("new concern processor: %v", err)
	}
	item := judgement.WorkItem{RepoID: "repo_app", VersionID: dependent.Version.ID}
	if err := processor.Process(ctx, item); err != nil {
		t.Fatalf("assess dependent before parent promotion: %v", err)
	}
	if _, found, err := repository.ConcernAssessment(ctx, dependent.Version.ID); err != nil || !found {
		t.Fatalf("dependent assessment found = %t, error = %v; want true, nil", found, err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: parent.Version.ID, ExpectedIntent: base.ID}); err != nil {
		t.Fatalf("promote parent: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      dependent.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	}); !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("direct promotion with stale assessment error = %v, want ErrIntentAdvanced", err)
	}
	if err := processor.Process(ctx, item); err != nil {
		t.Fatalf("reconsider dependent after parent promotion: %v", err)
	}
	if got := repository.CurrentIntent().Content; got != parent.Version.Content {
		t.Fatalf("current content = %#v, want parent content %#v until re-triage", got, parent.Version.Content)
	}
	runnable, err := repository.RunnableJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable judgements: %v", err)
	}
	if len(runnable.Versions) != 0 {
		t.Fatalf("runnable judgements = %#v, want old-governance assessment deferred", runnable)
	}
}

func newFourConcernProcessor(t *testing.T, service judgement.ConcernService, evaluator judgement.ConcernEvaluator) *judgement.ConcernProcessor {
	t.Helper()
	processor, err := judgement.NewConcernProcessor(service, evaluator, []judgement.Concern{
		{Name: "architecture-data-infrastructure", Prompt: "Does this change modify architecture, data models, or infrastructure requirements?", Reviewer: "noam@example.com"},
		{Name: "design-system-user-experience", Prompt: "Does this change modify the design system or user experience?", Reviewer: "yon@example.com"},
		{Name: "copy-commercial-impact", Prompt: "Does this change modify copywriting or commercial behavior?", Reviewer: "iris@example.com"},
		{Name: "prompts-models", Prompt: "Does this change modify prompts, LLM usage, or model selection?", Reviewer: "joule@example.com"},
	})
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}
	return processor
}

func judgementFixture() (intent.Version, intent.Revision) {
	governing := intent.Revision{
		ID:      "intent_a",
		Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"},
	}
	version := intent.Version{
		ID:         "version_b",
		ChangeID:   "change_b",
		BaseIntent: governing.ID,
		Content:    intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:   "ion@example.com",
	}
	return version, governing
}

type recordingConcernService struct {
	context         intent.ConcernAssessmentContext
	existing        intent.ConcernAssessment
	existingFound   bool
	recorded        intent.ConcernAssessment
	promotions      []intent.PromoteRequest
	sequence        int
	recordSequence  int
	promoteSequence int
}

type repositoryConcernService struct {
	repository *intent.Repository
}

func (service *repositoryConcernService) ConcernAssessment(ctx context.Context, _ string, versionID intent.VersionID) (intent.ConcernAssessment, bool, error) {
	return service.repository.ConcernAssessment(ctx, versionID)
}

func (service *repositoryConcernService) ConcernAssessmentContext(ctx context.Context, _ string, versionID intent.VersionID) (intent.ConcernAssessmentContext, error) {
	return service.repository.ConcernAssessmentContext(ctx, versionID)
}

func (service *repositoryConcernService) RecordConcernAssessment(ctx context.Context, _ string, assessment intent.ConcernAssessment) (intent.ConcernAssessment, error) {
	return service.repository.RecordConcernAssessment(ctx, assessment)
}

func (service *repositoryConcernService) Promote(ctx context.Context, _ string, request intent.PromoteRequest) (intent.Promoted, error) {
	return service.repository.Promote(ctx, request)
}

type acceptingContent struct{}

func (acceptingContent) Admit(context.Context, intent.VersionID, intent.ContentRef) error { return nil }

type processorProjection struct {
	current intent.ContentRef
}

func (projection *processorProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *processorProjection) Advance(_ context.Context, expected, next intent.ContentRef) error {
	if projection.current != expected {
		return errors.New("projection advanced")
	}
	projection.current = next
	return nil
}

func (service *recordingConcernService) ConcernAssessment(context.Context, string, intent.VersionID) (intent.ConcernAssessment, bool, error) {
	return service.existing, service.existingFound, nil
}

func (service *recordingConcernService) ConcernAssessmentContext(context.Context, string, intent.VersionID) (intent.ConcernAssessmentContext, error) {
	return service.context, nil
}

func (service *recordingConcernService) RecordConcernAssessment(_ context.Context, _ string, recorded intent.ConcernAssessment) (intent.ConcernAssessment, error) {
	service.sequence++
	service.recordSequence = service.sequence
	service.recorded = recorded
	service.existing = recorded
	service.existingFound = true
	return recorded, nil
}

func (service *recordingConcernService) Promote(_ context.Context, _ string, request intent.PromoteRequest) (intent.Promoted, error) {
	service.sequence++
	service.promoteSequence = service.sequence
	service.promotions = append(service.promotions, request)
	return intent.Promoted{}, nil
}

type recordingEvaluator struct {
	results       map[string]judgement.ConcernResult
	defaultResult judgement.ConcernResult
	requests      []judgement.ConcernRequest
	err           error
}

type mutatingEvaluator struct {
	calls              int
	secondDependencies []intent.VersionID
}

func (evaluator *mutatingEvaluator) Evaluate(_ context.Context, request judgement.ConcernRequest) (judgement.ConcernResult, error) {
	evaluator.calls++
	if evaluator.calls == 1 {
		request.Version.Dependencies[0] = "version_mutated"
	} else {
		evaluator.secondDependencies = append([]intent.VersionID(nil), request.Version.Dependencies...)
	}
	return judgement.ConcernResult{Reason: "the concern does not apply", Evidence: []string{"no matching change"}}, nil
}

func (evaluator *recordingEvaluator) Evaluate(_ context.Context, request judgement.ConcernRequest) (judgement.ConcernResult, error) {
	evaluator.requests = append(evaluator.requests, request)
	if evaluator.err != nil {
		return judgement.ConcernResult{}, evaluator.err
	}
	if result, found := evaluator.results[request.Concern.Name]; found {
		return result, nil
	}
	return evaluator.defaultResult, nil
}
