package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestStatusKeepsPendingChangeWhenDifferentChangePromotesTheSameContent(t *testing.T) {
	world := newJourneyWorldWithDecider(t, &deferThenPromoteDecider{})
	ion := world.cloneWorkspace("ion")
	base := world.currentIntent()
	grd := world.buildGRD()
	revision := ion.commitFile("same-content.txt", "same content\n", "submit same content")
	if result := ion.run(grd, "submit"); result.err != nil {
		t.Fatalf("submit first change: %v\n%s", result.err, result.stderr)
	}

	body := fmt.Sprintf(`{"baseIntent":%q,"contentRef":{"engine":"git","revision":%q}}`, base.ID, revision)
	res, responseBody := requestWithHeaders(t, world.server.handler, http.MethodPost, "/v1/repos/"+world.server.repo.ID+"/proposals", map[string]string{
		"Authorization":   repoBasicAuthorization(ion.token),
		"Content-Type":    "application/json",
		"Idempotency-Key": "different-change-same-content",
	}, body)
	requireStatus(t, res, responseBody, http.StatusOK)
	if got := world.currentIntent().ContentRef.Revision; got != revision {
		t.Fatalf("accepted revision = %q, want duplicate content %q", got, revision)
	}

	status := ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("read first change status: %v\n%s", status.err, status.stderr)
	}
	const wantStatus = "Working: new change\nBased on:\n  submit same content — judgement pending\n"
	if status.stdout != wantStatus {
		t.Fatalf("first change status = %q, want %q", status.stdout, wantStatus)
	}
}

func TestJourneyJ20HoldContinueAndSubmitDependentChange(t *testing.T) {
	decider := &recordingDeferDecider{}
	world := newJourneyWorldWithDecider(t, decider)
	ion := world.cloneWorkspace("ion")
	accepted := world.currentIntent()
	grd := world.buildGRD()

	ion.commitFile("auth.txt", "shared authentication base\n", "refactor authentication")
	first := ion.run(grd, "submit")
	if first.err != nil {
		t.Fatalf("submit held parent: %v\nstderr:\n%s", first.err, first.stderr)
	}
	const wantFirst = "Submitted: refactor authentication\nAdmitted; judgement pending\nContinue working on top of it.\n"
	if first.stdout != wantFirst {
		t.Fatalf("first submit stdout = %q, want %q", first.stdout, wantFirst)
	}
	if got := world.currentIntent(); got.ID != accepted.ID || got.ContentRef.Revision != accepted.ContentRef.Revision {
		t.Fatalf("accepted intent changed while parent held: %#v, want %#v", got, accepted)
	}
	if got := world.canonicalHead(); got != accepted.ContentRef.Revision {
		t.Fatalf("canonical main = %q, want accepted revision %q", got, accepted.ContentRef.Revision)
	}
	retry := ion.run(grd, "submit")
	if retry.err != nil {
		t.Fatalf("retry held parent: %v\nstderr:\n%s", retry.err, retry.stderr)
	}
	if retry.stdout != wantFirst {
		t.Fatalf("held retry stdout = %q, want original %q", retry.stdout, wantFirst)
	}

	status := ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status after held submit: %v\nstderr:\n%s", status.err, status.stderr)
	}
	const wantEmptySuccessor = "Working: new change\nBased on:\n  refactor authentication — judgement pending\n"
	if status.stdout != wantEmptySuccessor {
		t.Fatalf("status stdout = %q, want %q", status.stdout, wantEmptySuccessor)
	}

	ion.commitFile("passkey.txt", "passkey support\n", "add passkey support")
	status = ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status with dependent work: %v\nstderr:\n%s", status.err, status.stderr)
	}
	const wantDependentStatus = "Working: add passkey support\nBased on:\n  refactor authentication — judgement pending\n"
	if status.stdout != wantDependentStatus {
		t.Fatalf("dependent status stdout = %q, want %q", status.stdout, wantDependentStatus)
	}

	second := ion.run(grd, "submit")
	if second.err != nil {
		t.Fatalf("submit dependent change: %v\nstderr:\n%s", second.err, second.stderr)
	}
	const wantSecond = "Submitted: add passkey support\nAdmitted; judgement pending\nWaiting on: refactor authentication\nContinue working on top of it.\n"
	if second.stdout != wantSecond {
		t.Fatalf("second submit stdout = %q, want %q", second.stdout, wantSecond)
	}

	observed := decider.observed()
	if len(observed) != 3 {
		t.Fatalf("observed proposals = %d, want 3 including pending retry", len(observed))
	}
	if len(observed[0].Version.Dependencies) != 0 {
		t.Fatalf("parent dependencies = %q, want none", observed[0].Version.Dependencies)
	}
	if observed[1].Version.ID != observed[0].Version.ID || observed[1].Change.ID != observed[0].Change.ID {
		t.Fatalf("held retry created a different proposal: first %#v, retry %#v", observed[0], observed[1])
	}
	if !slices.Equal(observed[2].Version.Dependencies, []intent.VersionID{observed[0].Version.ID}) {
		t.Fatalf("dependent dependencies = %q, want parent version %q", observed[2].Version.Dependencies, observed[0].Version.ID)
	}
	if got := world.currentIntent(); got.ID != accepted.ID || got.ContentRef.Revision != accepted.ContentRef.Revision {
		t.Fatalf("accepted intent changed after dependent admission: %#v, want %#v", got, accepted)
	}
}

func TestHeldSubmissionCanPromoteWithoutLeavingAStaleWorkspaceDependency(t *testing.T) {
	world := newJourneyWorldWithDecider(t, &deferThenPromoteDecider{})
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()
	ion.commitFile("auth.txt", "shared authentication base\n", "refactor authentication")

	first := ion.run(grd, "submit")
	if first.err != nil {
		t.Fatalf("submit held change: %v\nstderr:\n%s", first.err, first.stderr)
	}
	second := ion.run(grd, "submit")
	if second.err != nil {
		t.Fatalf("retry promoted change: %v\nstderr:\n%s", second.err, second.stderr)
	}
	const wantPromotion = "Submitted: refactor authentication\nPromoted\nYou can keep working.\n"
	if second.stdout != wantPromotion {
		t.Fatalf("promoted retry stdout = %q, want %q", second.stdout, wantPromotion)
	}

	status := ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status after promotion: %v\nstderr:\n%s", status.err, status.stderr)
	}
	const wantStatus = "Working: new change\nBased on: accepted intent\n"
	if status.stdout != wantStatus {
		t.Fatalf("status after promotion = %q, want %q", status.stdout, wantStatus)
	}
}

func TestJourneyJ22PromotingParentReconsidersSubmittedDependent(t *testing.T) {
	decider := &journeyDependencyDecider{}
	world := newJourneyWorldWithDecider(t, decider)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()

	parentRevision := ion.commitFile("auth.txt", "shared authentication base\n", "refactor authentication")
	decider.parentRevision = parentRevision
	parent := ion.run(grd, "submit")
	if parent.err != nil {
		t.Fatalf("submit held parent: %v\nstderr:\n%s", parent.err, parent.stderr)
	}
	dependentRevision := ion.commitFile("passkey.txt", "passkey support\n", "add passkey support")
	dependent := ion.run(grd, "submit")
	if dependent.err != nil {
		t.Fatalf("submit waiting dependent: %v\nstderr:\n%s", dependent.err, dependent.stderr)
	}
	const wantWaiting = "Submitted: add passkey support\nAdmitted; judgement pending\nWaiting on: refactor authentication\nContinue working on top of it.\n"
	if dependent.stdout != wantWaiting {
		t.Fatalf("dependent submit stdout = %q, want %q", dependent.stdout, wantWaiting)
	}
	if got := world.canonicalHead(); got == dependentRevision {
		t.Fatal("dependent reached canonical main before its parent promoted")
	}

	// Retrying the frozen parent stands in for the later judgement event. The
	// dependent itself is not resubmitted.
	reconsiderPath := filepath.Join(t.TempDir(), "parent-reconsideration")
	requireGitSuccess(t, "create parent reconsideration workspace", "-C", ion.path, "worktree", "add", "--detach", reconsiderPath, parentRevision)
	reconsideration := (&journeyWorkspace{t: t, path: reconsiderPath, token: ion.token}).run(grd, "submit")
	if reconsideration.err != nil {
		t.Fatalf("reconsider parent: %v\nstderr:\n%s", reconsideration.err, reconsideration.stderr)
	}
	const wantParentPromotion = "Submitted: refactor authentication\nPromoted\nYou can keep working.\n"
	if reconsideration.stdout != wantParentPromotion {
		t.Fatalf("parent reconsideration stdout = %q, want %q", reconsideration.stdout, wantParentPromotion)
	}
	if got := world.canonicalHead(); got != dependentRevision {
		t.Fatalf("canonical main = %q, want automatically reconsidered dependent %q", got, dependentRevision)
	}
	if got := world.currentIntent().ContentRef.Revision; got != dependentRevision {
		t.Fatalf("current intent revision = %q, want dependent %q", got, dependentRevision)
	}
	parentCalls, dependentCalls := decider.calls()
	if parentCalls != 2 || dependentCalls != 2 {
		t.Fatalf("decision calls = parent %d, dependent %d; want 2 initial/reconsideration calls each", parentCalls, dependentCalls)
	}
	status := ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status after dependent promotion: %v\nstderr:\n%s", status.err, status.stderr)
	}
	const wantStatus = "Working: new change\nBased on: accepted intent\n"
	if status.stdout != wantStatus {
		t.Fatalf("status after dependent promotion = %q, want %q", status.stdout, wantStatus)
	}
}

type recordingDeferDecider struct {
	mu        sync.Mutex
	proposals []intent.Proposed
}

func (decider *recordingDeferDecider) DecidePromotion(_ context.Context, subject intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	proposed := intent.Proposed{Change: subject.Change, Version: subject.Version}
	decider.proposals = append(decider.proposals, proposed)
	return intentservice.DeferPromotion, nil
}

func (decider *recordingDeferDecider) observed() []intent.Proposed {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	return slices.Clone(decider.proposals)
}

type deferThenPromoteDecider struct {
	mu    sync.Mutex
	calls int
}

func (decider *deferThenPromoteDecider) DecidePromotion(context.Context, intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	decider.calls++
	if decider.calls == 1 {
		return intentservice.DeferPromotion, nil
	}
	return intentservice.PromoteNow, nil
}

type journeyDependencyDecider struct {
	mu             sync.Mutex
	parentRevision string
	parentCalls    int
	dependentCalls int
}

func (decider *journeyDependencyDecider) DecidePromotion(_ context.Context, subject intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	proposed := intent.Proposed{Change: subject.Change, Version: subject.Version}
	if proposed.Version.Content.Revision == decider.parentRevision {
		decider.parentCalls++
		if decider.parentCalls == 1 {
			return intentservice.DeferPromotion, nil
		}
		return intentservice.PromoteNow, nil
	}
	decider.dependentCalls++
	return intentservice.PromoteNow, nil
}

func (decider *journeyDependencyDecider) calls() (int, int) {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	return decider.parentCalls, decider.dependentCalls
}
