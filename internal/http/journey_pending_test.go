package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
)

func TestStatusKeepsPendingChangeWhenDifferentChangePromotesTheSameContent(t *testing.T) {
	world := newJourneyWorld(t)
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
	world := newJourneyWorld(t)
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

	pending, err := world.server.judgement.PendingJudgements(context.Background(), world.server.repo.ID, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending judgements: %v", err)
	}
	if len(pending.Versions) != 2 {
		t.Fatalf("pending judgements = %#v, want parent and dependent once each", pending.Versions)
	}
	if len(pending.Versions[0].Dependencies) != 0 {
		t.Fatalf("parent dependencies = %q, want none", pending.Versions[0].Dependencies)
	}
	if !slices.Equal(pending.Versions[1].Dependencies, []intent.VersionID{pending.Versions[0].ID}) {
		t.Fatalf("dependent dependencies = %q, want parent version %q", pending.Versions[1].Dependencies, pending.Versions[0].ID)
	}
	if got := world.currentIntent(); got.ID != accepted.ID || got.ContentRef.Revision != accepted.ContentRef.Revision {
		t.Fatalf("accepted intent changed after dependent admission: %#v, want %#v", got, accepted)
	}
}

func TestRepeatedPendingSubmissionDoesNotLeaveAStaleWorkspaceDependency(t *testing.T) {
	world := newJourneyWorld(t)
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
	const wantPending = "Submitted: refactor authentication\nAdmitted; judgement pending\nContinue working on top of it.\n"
	if second.stdout != wantPending {
		t.Fatalf("pending retry stdout = %q, want %q", second.stdout, wantPending)
	}

	status := ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status after promotion: %v\nstderr:\n%s", status.err, status.stderr)
	}
	const wantStatus = "Working: new change\nBased on:\n  refactor authentication — judgement pending\n"
	if status.stdout != wantStatus {
		t.Fatalf("status after promotion = %q, want %q", status.stdout, wantStatus)
	}
}

func TestJourneyJ22ParentAndDependentRemainDistinctPendingJudgements(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()

	parentRevision := ion.commitFile("auth.txt", "shared authentication base\n", "refactor authentication")
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

	pending, err := world.server.judgement.PendingJudgements(context.Background(), world.server.repo.ID, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending judgements: %v", err)
	}
	if len(pending.Versions) != 2 || pending.Versions[0].Content.Revision != parentRevision || pending.Versions[1].Content.Revision != dependentRevision {
		t.Fatalf("pending judgements = %#v, want parent then dependent", pending.Versions)
	}
	if !slices.Equal(pending.Versions[1].Dependencies, []intent.VersionID{pending.Versions[0].ID}) {
		t.Fatalf("dependent dependencies = %q, want parent %q", pending.Versions[1].Dependencies, pending.Versions[0].ID)
	}
	status := ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status after dependent promotion: %v\nstderr:\n%s", status.err, status.stderr)
	}
	const wantStatus = "Working: new change\nBased on:\n  add passkey support — judgement pending\n"
	if status.stdout != wantStatus {
		t.Fatalf("status after dependent promotion = %q, want %q", status.stdout, wantStatus)
	}
}
