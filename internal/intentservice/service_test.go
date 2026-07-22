package intentservice_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentfs"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestApproveAllServiceAdmitsBeforeAttemptingPromotion(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})
	initial := repository.CurrentIntent()

	admission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "control-api",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if admission.Proposed.Change.ID == "" || admission.Proposed.Version.Producer != "control-api" {
		t.Fatalf("admitted proposal = %#v", admission.Proposed)
	}
	if admission.Promotion == nil || admission.Promotion.Promotion.VersionID != admission.Proposed.Version.ID {
		t.Fatalf("promotion = %#v, want admitted version", admission.Promotion)
	}

	staleAdmission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-stale",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "control-api",
	})
	if err != nil {
		t.Fatalf("propose stale change: %v", err)
	}
	if staleAdmission.Proposed.Change.ID == "" || staleAdmission.Promotion != nil {
		t.Fatalf("stale admission = %#v, want admitted without promotion", staleAdmission)
	}
}

func TestServiceCanHoldAnAdmittedProposal(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	service := intentservice.NewWithTriage(staticResolver{repository: repository}, holdingTriage{})
	initial := repository.CurrentIntent()

	admission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-held",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if admission.Proposed.Version.ID == "" {
		t.Fatal("held proposal was not admitted")
	}
	if admission.Promotion != nil {
		t.Fatalf("held proposal promotion = %#v, want nil", admission.Promotion)
	}
	if got := repository.CurrentIntent(); got != initial {
		t.Fatalf("current intent = %#v, want unchanged %#v", got, initial)
	}
	if projection.current != initialContent {
		t.Fatalf("trunk projection = %#v, want %#v", projection.current, initialContent)
	}
}

func TestPromotingAHeldVersionReconsidersItsAdmittedDependent(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	triage := &holdParentOnceTriage{parentRevision: "bbbbbbbb"}
	service := intentservice.NewWithTriage(staticResolver{repository: repository}, triage)
	initial := repository.CurrentIntent()

	parentProposal := intentservice.Proposal{
		IdempotencyKey: "request-parent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: triage.parentRevision},
		Producer:       "ion",
	}
	parent, err := service.Propose(ctx, "repo_123", parentProposal)
	if err != nil {
		t.Fatalf("propose held parent: %v", err)
	}
	if parent.Promotion != nil {
		t.Fatalf("held parent promotion = %#v, want nil", parent.Promotion)
	}

	dependent, err := service.Propose(ctx, "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-dependent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
		Dependencies:   []intent.VersionID{parent.Proposed.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	if dependent.Promotion != nil {
		t.Fatalf("dependent promoted before its dependency: %#v", dependent.Promotion)
	}

	retriedParent, err := service.Propose(ctx, "repo_123", parentProposal)
	if err != nil {
		t.Fatalf("reconsider parent: %v", err)
	}
	if retriedParent.Promotion == nil {
		t.Fatal("reconsidered parent was not promoted")
	}
	inspection, err := repository.InspectChange(ctx, dependent.Proposed.Change.ID)
	if err != nil {
		t.Fatalf("inspect dependent: %v", err)
	}
	if inspection.Promotion == nil {
		t.Fatal("dependent was not reconsidered after its dependency promoted")
	}
	if got := repository.CurrentIntent().Content; got != dependent.Proposed.Version.Content {
		t.Fatalf("current content = %#v, want dependent content %#v", got, dependent.Proposed.Version.Content)
	}
}

func TestServiceRecoversAReadyDependentAfterRestart(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	journalPath := filepath.Join(t.TempDir(), "intent.journal")
	ledger, err := intentfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	repository, err := intent.OpenRepository(ctx, initialContent, ledger, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	triageErr := errors.New("stop after parent promotion")
	triage := &interruptDependentTriage{parentRevision: "bbbbbbbb", err: triageErr}
	service := intentservice.NewWithTriage(staticResolver{repository: repository}, triage)
	initial := repository.CurrentIntent()
	parentProposal := intentservice.Proposal{
		IdempotencyKey: "request-parent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: triage.parentRevision},
		Producer:       "ion",
	}
	parent, err := service.Propose(ctx, "repo_123", parentProposal)
	if err != nil {
		t.Fatalf("propose held parent: %v", err)
	}
	dependent, err := service.Propose(ctx, "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-dependent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
		Dependencies:   []intent.VersionID{parent.Proposed.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	if _, err := service.Propose(ctx, "repo_123", parentProposal); !errors.Is(err, triageErr) {
		t.Fatalf("interrupted parent reconsideration error = %v, want %v", err, triageErr)
	}
	if got := repository.CurrentIntent().Content; got != parent.Proposed.Version.Content {
		t.Fatalf("content before restart = %#v, want promoted parent %#v", got, parent.Proposed.Version.Content)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}

	reopened, err := intentfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedRepository, err := intent.OpenRepository(ctx, initialContent, reopened, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	restartedService := intentservice.New(staticResolver{repository: restartedRepository})
	current, err := restartedService.CurrentIntent(ctx, "repo_123")
	if err != nil {
		t.Fatalf("recover current intent: %v", err)
	}
	if current.Content != dependent.Proposed.Version.Content {
		t.Fatalf("recovered content = %#v, want dependent %#v", current.Content, dependent.Proposed.Version.Content)
	}
	inspection, err := restartedRepository.InspectChange(ctx, dependent.Proposed.Change.ID)
	if err != nil {
		t.Fatalf("inspect recovered dependent: %v", err)
	}
	if inspection.Promotion == nil {
		t.Fatal("recovered dependent was not promoted")
	}
}

func TestServiceReturnsTheAdmissionWhenTriageFails(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	triageErr := errors.New("triage unavailable")
	service := intentservice.NewWithTriage(staticResolver{repository: repository}, failingTriage{err: triageErr})
	initial := repository.CurrentIntent()

	admission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-triage-failure",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if !errors.Is(err, triageErr) {
		t.Fatalf("propose error = %v, want triage failure", err)
	}
	if admission.Proposed.Version.ID == "" {
		t.Fatal("triage failure discarded the durable admission")
	}
	if admission.Promotion != nil {
		t.Fatalf("triage failure promotion = %#v, want nil", admission.Promotion)
	}
	if got := repository.CurrentIntent(); got != initial {
		t.Fatalf("current intent = %#v, want unchanged %#v", got, initial)
	}
}

type holdingTriage struct{}

func (holdingTriage) DecideNext(context.Context, intent.Proposed) (intentservice.NextAction, error) {
	return intentservice.Hold, nil
}

type holdParentOnceTriage struct {
	parentRevision string
	parentCalls    int
}

func (triage *holdParentOnceTriage) DecideNext(_ context.Context, proposed intent.Proposed) (intentservice.NextAction, error) {
	if proposed.Version.Content.Revision == triage.parentRevision {
		triage.parentCalls++
		if triage.parentCalls == 1 {
			return intentservice.Hold, nil
		}
	}
	return intentservice.Promote, nil
}

type interruptDependentTriage struct {
	parentRevision string
	parentCalls    int
	dependentCalls int
	err            error
}

func (triage *interruptDependentTriage) DecideNext(_ context.Context, proposed intent.Proposed) (intentservice.NextAction, error) {
	if proposed.Version.Content.Revision == triage.parentRevision {
		triage.parentCalls++
		if triage.parentCalls == 1 {
			return intentservice.Hold, nil
		}
		return intentservice.Promote, nil
	}
	triage.dependentCalls++
	if triage.dependentCalls > 1 {
		return "", triage.err
	}
	return intentservice.Promote, nil
}

type failingTriage struct {
	err error
}

func (triage failingTriage) DecideNext(context.Context, intent.Proposed) (intentservice.NextAction, error) {
	return "", triage.err
}

type staticResolver struct {
	repository *intent.Repository
}

func (resolver staticResolver) Resolve(context.Context, string) (intentservice.Repository, error) {
	return resolver.repository, nil
}

func (resolver staticResolver) Bootstrap(_ context.Context, _ string, content intent.ContentRef) (intent.Revision, error) {
	current := resolver.repository.CurrentIntent()
	if current.Content != content {
		return intent.Revision{}, intentservice.ErrRepositoryAlreadyInitialized
	}
	return current, nil
}

type acceptingAdmission struct{}

func (acceptingAdmission) Admit(context.Context, intent.VersionID, intent.ContentRef) error {
	return nil
}

type recordingProjection struct {
	current intent.ContentRef
}

func (projection *recordingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *recordingProjection) Advance(_ context.Context, _ intent.ContentRef, next intent.ContentRef) error {
	projection.current = next
	return nil
}
