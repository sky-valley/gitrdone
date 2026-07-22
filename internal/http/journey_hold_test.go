package httpapi_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestJourneyJ20HoldContinueAndSubmitDependentChange(t *testing.T) {
	triage := &recordingHoldTriage{}
	world := newJourneyWorldWithTriage(t, triage)
	ion := world.cloneWorkspace("ion")
	accepted := world.currentIntent()
	grd := world.buildGRD()

	ion.commitFile("auth.txt", "shared authentication base\n", "refactor authentication")
	first := ion.run(grd, "submit")
	if first.err != nil {
		t.Fatalf("submit held parent: %v\nstderr:\n%s", first.err, first.stderr)
	}
	const wantFirst = "Submitted: refactor authentication\nHeld\nStarted a new working change on top of it.\n"
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
	const wantEmptySuccessor = "Working: new change\nBased on:\n  refactor authentication — held\n"
	if status.stdout != wantEmptySuccessor {
		t.Fatalf("status stdout = %q, want %q", status.stdout, wantEmptySuccessor)
	}

	ion.commitFile("passkey.txt", "passkey support\n", "add passkey support")
	status = ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status with dependent work: %v\nstderr:\n%s", status.err, status.stderr)
	}
	const wantDependentStatus = "Working: add passkey support\nBased on:\n  refactor authentication — held\n"
	if status.stdout != wantDependentStatus {
		t.Fatalf("dependent status stdout = %q, want %q", status.stdout, wantDependentStatus)
	}

	second := ion.run(grd, "submit")
	if second.err != nil {
		t.Fatalf("submit dependent change: %v\nstderr:\n%s", second.err, second.stderr)
	}
	const wantSecond = "Submitted: add passkey support\nWaiting on: refactor authentication\nStarted a new working change on top of it.\n"
	if second.stdout != wantSecond {
		t.Fatalf("second submit stdout = %q, want %q", second.stdout, wantSecond)
	}

	observed := triage.observed()
	if len(observed) != 3 {
		t.Fatalf("triaged proposals = %d, want 3 including held retry", len(observed))
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
	world := newJourneyWorldWithTriage(t, &holdThenPromoteTriage{})
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

type recordingHoldTriage struct {
	mu        sync.Mutex
	proposals []intent.Proposed
}

func (triage *recordingHoldTriage) DecideNext(_ context.Context, proposed intent.Proposed) (intentservice.NextAction, error) {
	triage.mu.Lock()
	defer triage.mu.Unlock()
	triage.proposals = append(triage.proposals, proposed)
	return intentservice.Hold, nil
}

func (triage *recordingHoldTriage) observed() []intent.Proposed {
	triage.mu.Lock()
	defer triage.mu.Unlock()
	return slices.Clone(triage.proposals)
}

type holdThenPromoteTriage struct {
	mu    sync.Mutex
	calls int
}

func (triage *holdThenPromoteTriage) DecideNext(context.Context, intent.Proposed) (intentservice.NextAction, error) {
	triage.mu.Lock()
	defer triage.mu.Unlock()
	triage.calls++
	if triage.calls == 1 {
		return intentservice.Hold, nil
	}
	return intentservice.Promote, nil
}
