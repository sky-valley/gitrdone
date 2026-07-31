package httpapi_test

import (
	"path/filepath"
	"testing"
)

func TestJourneyJ10SubmitCreatesPendingJudgementWithoutMovingIntent(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	before := world.currentIntent()
	proposedRevision := ion.commitFile("login.txt", "bounded timeout\n", "fix login timeout")

	result := ion.run(world.buildGRD(), "submit")
	if result.err != nil {
		t.Fatalf("grd submit failed: %v\nstderr:\n%s", result.err, result.stderr)
	}
	const wantOutput = "Submitted: fix login timeout\nAdmitted; judgement pending\nContinue working on top of it.\n"
	if result.stdout != wantOutput {
		t.Fatalf("grd submit stdout = %q, want %q", result.stdout, wantOutput)
	}
	if result.stderr != "" {
		t.Fatalf("grd submit stderr = %q, want empty", result.stderr)
	}

	after := world.currentIntent()
	if after.ID != before.ID || after.ContentRef.Revision != before.ContentRef.Revision {
		t.Fatalf("submission moved accepted intent from %#v to %#v", before, after)
	}
	if got := world.canonicalHead(); got != before.ContentRef.Revision {
		t.Fatalf("canonical main = %q, want accepted revision %q while %q awaits judgement", got, before.ContentRef.Revision, proposedRevision)
	}
	if !ion.isClean() {
		t.Fatal("Ion workspace is dirty after submission")
	}
}

func TestJourneyJ15RetryReturnsTheSamePendingAdmission(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	ion.commitFile("retry.txt", "retry safely\n", "make retry safe")
	grd := world.buildGRD()

	first := ion.run(grd, "submit")
	if first.err != nil {
		t.Fatalf("first grd submit failed: %v\nstderr:\n%s", first.err, first.stderr)
	}
	firstIntent := world.currentIntent()
	second := ion.run(grd, "submit")
	if second.err != nil {
		t.Fatalf("retried grd submit failed: %v\nstderr:\n%s", second.err, second.stderr)
	}
	secondIntent := world.currentIntent()
	if second.stdout != first.stdout {
		t.Fatalf("retry stdout = %q, want original %q", second.stdout, first.stdout)
	}
	if secondIntent.ID != firstIntent.ID {
		t.Fatalf("retry advanced intent from %q to %q", firstIntent.ID, secondIntent.ID)
	}
}

func TestJourneyJ12DirtyWorkspaceIsPreservedAndRefused(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	before := world.currentIntent()
	ion.commitFile("committed.txt", "ready\n", "ready change")
	writeGitFile(t, ion.path, "unfinished.txt", "do not lose me\n")

	result := ion.run(world.buildGRD(), "submit")
	if result.err == nil {
		t.Fatal("grd submit unexpectedly accepted a dirty workspace")
	}
	if result.stdout != "" {
		t.Fatalf("dirty submit stdout = %q, want empty", result.stdout)
	}
	const wantError = "grd submit: workspace has uncommitted changes; commit them before submitting\n"
	if result.stderr != wantError {
		t.Fatalf("dirty submit stderr = %q, want %q", result.stderr, wantError)
	}
	after := world.currentIntent()
	if after.ID != before.ID {
		t.Fatalf("dirty submit advanced intent from %q to %q", before.ID, after.ID)
	}
	if got := world.canonicalHead(); got != before.ContentRef.Revision {
		t.Fatalf("dirty submit moved canonical main to %q, want %q", got, before.ContentRef.Revision)
	}
	assertFileContent(t, filepath.Join(ion.path, "unfinished.txt"), "do not lose me\n")
}

func TestJourneyJ14DivergedWorkspaceIsPreservedAndRefused(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	before := world.currentIntent()
	requireGitSuccess(t, "create divergent history", "-C", ion.path, "checkout", "--orphan", "divergent")
	requireGitSuccess(t, "remove accepted files from divergent history", "-C", ion.path, "rm", "-rf", ".")
	writeGitFile(t, ion.path, "divergent.txt", "unrelated work\n")
	requireGitSuccess(t, "stage divergent content", "-C", ion.path, "add", "divergent.txt")
	requireGitSuccess(t, "commit divergent content", "-C", ion.path, "commit", "-m", "divergent work")
	divergentRevision := ion.head()

	result := ion.run(world.buildGRD(), "submit")
	if result.err == nil {
		t.Fatal("grd submit unexpectedly accepted a diverged workspace")
	}
	const wantError = "grd submit: workspace is not based on current intent; sync before submitting\n"
	if result.stderr != wantError {
		t.Fatalf("diverged submit stderr = %q, want %q", result.stderr, wantError)
	}
	after := world.currentIntent()
	if after.ID != before.ID || after.ContentRef.Revision != before.ContentRef.Revision {
		t.Fatalf("diverged submit changed intent from %#v to %#v", before, after)
	}
	if got := world.canonicalHead(); got != before.ContentRef.Revision {
		t.Fatalf("diverged submit moved canonical main to %q, want %q", got, before.ContentRef.Revision)
	}
	if got := ion.head(); got != divergentRevision {
		t.Fatalf("diverged workspace HEAD changed from %q to %q", divergentRevision, got)
	}
	status := ion.run(world.buildGRD(), "status")
	if status.err != nil {
		t.Fatalf("read diverged status: %v\n%s", status.err, status.stderr)
	}
	const wantStatus = "Working: divergent work\nBased on: unknown (workspace does not descend from accepted intent)\n"
	if status.stdout != wantStatus {
		t.Fatalf("diverged status = %q, want %q", status.stdout, wantStatus)
	}
}
