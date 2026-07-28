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
	service := intentservice.NewWithPromotionDecider(staticResolver{repository: repository}, deferPromotionDecider{})
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

func TestServiceAmendmentUsesOrdinaryJudgementAndPromotion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
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
	service := intentservice.New(staticResolver{repository: repository})
	receipt, err := service.Amend(ctx, "repo_123", intentservice.AmendmentRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository",
		Rationale:       "timeout path could duplicate the operation",
	})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if receipt.Amended.Version.ChangeID != original.Change.ID || receipt.Amended.Amendment.FromVersion != original.Version.ID {
		t.Fatalf("amendment receipt = %#v, want next version of original change", receipt)
	}
	if receipt.Promotion == nil || receipt.Promotion.Promotion.VersionID != receipt.Amended.Version.ID {
		t.Fatalf("amendment promotion = %#v, want amended version %q", receipt.Promotion, receipt.Amended.Version.ID)
	}
	if got := repository.CurrentIntent().Content; got != receipt.Amended.Version.Content {
		t.Fatalf("current content = %#v, want amended content %#v", got, receipt.Amended.Version.Content)
	}
}

func TestServiceAcceptsParentAmendmentWithoutPromotingDependentOnSupersededVersion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})

	amended, err := service.Amend(ctx, "repo_123", intentservice.AmendmentRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: parent.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend parent through service: %v", err)
	}
	if amended.Promotion == nil || repository.CurrentIntent().Content != amended.Amended.Version.Content {
		t.Fatalf("accepted amendment = %#v, want B prime as current intent", amended)
	}
	inspection, err := repository.InspectChange(ctx, dependent.Change.ID)
	if err != nil {
		t.Fatalf("inspect dependent: %v", err)
	}
	if inspection.LatestVersion.ID != dependent.Version.ID || inspection.LatestPromotion != nil {
		t.Fatalf("dependent after parent amendment = %#v, want unchanged held C", inspection)
	}
	candidates, err := repository.DependentReconciliations(ctx)
	if err != nil {
		t.Fatalf("read dependent reconciliations: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Dependent.Version.ID != dependent.Version.ID {
		t.Fatalf("dependent reconciliations = %#v, want held C", candidates)
	}
}

func TestServiceSendsReconciledDependentThroughOrdinaryJudgement(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: parent.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend parent: %v", err)
	}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote amended parent: %v", err)
	}
	request := intentservice.DependentReconciliationRequest{
		IdempotencyKey:     "reconcile-c",
		ExpectedVersion:    dependent.Version.ID,
		ReplacedDependency: parent.Version.ID,
		AcceptedVersion:    amended.Version.ID,
		ExpectedIntent:     promoted.Intent.ID,
		Content:            intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:           "git-engine",
		Rationale:          "replay C onto accepted B prime",
	}

	heldService := intentservice.NewWithPromotionDecider(staticResolver{repository: repository}, deferPromotionDecider{})
	held, err := heldService.ReconcileDependent(ctx, "repo_123", request)
	if err != nil {
		t.Fatalf("reconcile held dependent: %v", err)
	}
	if held.Reconciled.Version.ChangeID != dependent.Change.ID || held.Promotion != nil {
		t.Fatalf("held reconciliation = %#v, want durable C prime awaiting judgement", held)
	}
	if got := repository.CurrentIntent(); got != promoted.Intent {
		t.Fatalf("intent after deferred judgement = %#v, want %#v", got, promoted.Intent)
	}

	promoteService := intentservice.New(staticResolver{repository: repository})
	accepted, err := promoteService.ReconcileDependent(ctx, "repo_123", request)
	if err != nil {
		t.Fatalf("reconsider reconciled dependent: %v", err)
	}
	if accepted.Reconciled.Version.ID != held.Reconciled.Version.ID {
		t.Fatalf("retried version = %q, want durable version %q", accepted.Reconciled.Version.ID, held.Reconciled.Version.ID)
	}
	if accepted.Promotion == nil || accepted.Promotion.Promotion.VersionID != held.Reconciled.Version.ID {
		t.Fatalf("accepted promotion = %#v, want reconciled dependent", accepted.Promotion)
	}
}

func TestServiceSendsRebasedHeldVersionThroughOrdinaryJudgement(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	heldService := intentservice.NewWithPromotionDecider(staticResolver{repository: repository}, deferPromotionDecider{})
	proposed, err := heldService.Propose(ctx, "repo_123", intentservice.Proposal{
		IdempotencyKey: "proposal-held",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose held change: %v", err)
	}
	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-current",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "noam",
	})
	if err != nil {
		t.Fatalf("propose current change: %v", err)
	}
	current, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote current change: %v", err)
	}

	request := intentservice.HeldVersionRebaseRequest{
		IdempotencyKey:  "rebase-held",
		ExpectedVersion: proposed.Proposed.Version.ID,
		ExpectedIntent:  current.Intent.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:        "repository-engine",
		Rationale:       "replay held change onto current intent",
	}
	held, err := heldService.RebaseHeldVersion(ctx, "repo_123", request)
	if err != nil {
		t.Fatalf("rebase held version: %v", err)
	}
	if held.Rebased.Version.ChangeID != proposed.Proposed.Change.ID || held.Promotion != nil {
		t.Fatalf("held rebased version = %#v, want same Change awaiting judgement", held)
	}

	promoteService := intentservice.New(staticResolver{repository: repository})
	accepted, err := promoteService.RebaseHeldVersion(ctx, "repo_123", request)
	if err != nil {
		t.Fatalf("reconsider rebased held version: %v", err)
	}
	if accepted.Rebased.Version.ID != held.Rebased.Version.ID ||
		accepted.Promotion == nil ||
		accepted.Promotion.Promotion.VersionID != held.Rebased.Version.ID {
		t.Fatalf("accepted held version rebase = %#v, want same version promoted after ordinary judgement", accepted)
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
	decider := &deferParentOnceDecider{parentRevision: "bbbbbbbb"}
	service := intentservice.NewWithPromotionDecider(staticResolver{repository: repository}, decider)
	initial := repository.CurrentIntent()

	parentProposal := intentservice.Proposal{
		IdempotencyKey: "request-parent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: decider.parentRevision},
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
	if inspection.LatestPromotion == nil {
		t.Fatal("dependent was not reconsidered after its dependency promoted")
	}
	if got := repository.CurrentIntent().Content; got != dependent.Proposed.Version.Content {
		t.Fatalf("current content = %#v, want dependent content %#v", got, dependent.Proposed.Version.Content)
	}
}

func TestPromotingADeferredParentReconsidersOnlyTheLatestDependentVersion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	decider := &deferParentOnceDecider{parentRevision: "bbbbbbbb"}
	service := intentservice.NewWithPromotionDecider(staticResolver{repository: repository}, decider)
	initial := repository.CurrentIntent()
	parentProposal := intentservice.Proposal{
		IdempotencyKey: "request-parent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: decider.parentRevision},
		Producer:       "ion",
	}
	parent, err := service.Propose(ctx, "repo_123", parentProposal)
	if err != nil {
		t.Fatalf("propose deferred parent: %v", err)
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
	amended, err := service.Amend(ctx, "repo_123", intentservice.AmendmentRequest{
		IdempotencyKey:  "amend-dependent",
		ChangeID:        dependent.Proposed.Change.ID,
		ExpectedVersion: dependent.Proposed.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:        "repository-agent",
		Rationale:       "repair dependent",
	})
	if err != nil {
		t.Fatalf("amend waiting dependent: %v", err)
	}
	if amended.Promotion != nil {
		t.Fatalf("amended dependent promoted before parent: %#v", amended.Promotion)
	}

	if _, err := service.Propose(ctx, "repo_123", parentProposal); err != nil {
		t.Fatalf("promote parent and reconsider latest dependent: %v", err)
	}
	inspection, err := repository.InspectChange(ctx, dependent.Proposed.Change.ID)
	if err != nil {
		t.Fatalf("inspect amended dependent: %v", err)
	}
	if inspection.LatestVersion.ID != amended.Amended.Version.ID || inspection.LatestPromotion == nil {
		t.Fatalf("dependent inspection = %#v, want latest amended version promoted", inspection)
	}
	if got := repository.CurrentIntent().Content; got != amended.Amended.Version.Content {
		t.Fatalf("current content = %#v, want amended dependent %#v", got, amended.Amended.Version.Content)
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
	decisionErr := errors.New("stop after parent promotion")
	decider := &interruptDependentDecider{parentRevision: "bbbbbbbb", err: decisionErr}
	service := intentservice.NewWithPromotionDecider(staticResolver{repository: repository}, decider)
	initial := repository.CurrentIntent()
	parentProposal := intentservice.Proposal{
		IdempotencyKey: "request-parent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: decider.parentRevision},
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
	if _, err := service.Propose(ctx, "repo_123", parentProposal); !errors.Is(err, decisionErr) {
		t.Fatalf("interrupted parent reconsideration error = %v, want %v", err, decisionErr)
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
	if inspection.LatestPromotion == nil {
		t.Fatal("recovered dependent was not promoted")
	}
}

func TestServiceReturnsTheAdmissionWhenPromotionDecisionFails(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	decisionErr := errors.New("promotion decision unavailable")
	service := intentservice.NewWithPromotionDecider(staticResolver{repository: repository}, failingDecider{err: decisionErr})
	initial := repository.CurrentIntent()

	admission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-decision-failure",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if !errors.Is(err, decisionErr) {
		t.Fatalf("propose error = %v, want promotion decision failure", err)
	}
	if admission.Proposed.Version.ID == "" {
		t.Fatal("promotion decision failure discarded the durable admission")
	}
	if admission.Promotion != nil {
		t.Fatalf("decision failure promotion = %#v, want nil", admission.Promotion)
	}
	if got := repository.CurrentIntent(); got != initial {
		t.Fatalf("current intent = %#v, want unchanged %#v", got, initial)
	}
}

type deferPromotionDecider struct{}

func (deferPromotionDecider) DecidePromotion(context.Context, intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	return intentservice.DeferPromotion, nil
}

type deferParentOnceDecider struct {
	parentRevision string
	parentCalls    int
}

func (decider *deferParentOnceDecider) DecidePromotion(_ context.Context, subject intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	if subject.Version.Content.Revision == decider.parentRevision {
		decider.parentCalls++
		if decider.parentCalls == 1 {
			return intentservice.DeferPromotion, nil
		}
	}
	return intentservice.PromoteNow, nil
}

type interruptDependentDecider struct {
	parentRevision string
	parentCalls    int
	dependentCalls int
	err            error
}

func (decider *interruptDependentDecider) DecidePromotion(_ context.Context, subject intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	if subject.Version.Content.Revision == decider.parentRevision {
		decider.parentCalls++
		if decider.parentCalls == 1 {
			return intentservice.DeferPromotion, nil
		}
		return intentservice.PromoteNow, nil
	}
	decider.dependentCalls++
	if decider.dependentCalls > 1 {
		return "", decider.err
	}
	return intentservice.PromoteNow, nil
}

type failingDecider struct {
	err error
}

func (decider failingDecider) DecidePromotion(context.Context, intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	return "", decider.err
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
