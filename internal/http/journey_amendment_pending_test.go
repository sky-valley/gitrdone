package httpapi_test

import "testing"

func TestJourneyRepositoryAmendmentRemainsPendingJudgement(t *testing.T) {
	decider := &recordingDeferDecider{}
	world := newJourneyWorldWithDecider(t, decider)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()
	acceptedRevision := world.currentIntent().ContentRef.Revision

	ion.commitFile("feature.txt", "unsafe\n", "implement feature")
	if result := ion.run(grd, "submit"); result.err != nil {
		t.Fatalf("submit original change: %v\nstdout:\n%sstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	observed := decider.observed()
	if len(observed) == 0 {
		t.Fatal("decider did not observe Ion's original change")
	}
	original := observed[0]
	continuedRevision := ion.commitFile("local.txt", "Ion kept working\n", "continue locally")

	repositoryAgent := world.cloneWorkspace("repository-agent")
	amendedRevision := repositoryAgent.commitFile("feature.txt", "safe\n", "repair authorization boundary")
	requireGitSuccess(t, "publish pending amendment", "-C", repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/pending-amendment")
	amended := amendJourneyChange(t, world, original, amendedRevision, "fixed the authorization boundary before integration", "journey-pending-amendment")
	if amended.Promotion != nil {
		t.Fatalf("pending amendment promotion = %#v, want none", amended.Promotion)
	}
	if amended.Amended.Version.ID == original.Version.ID {
		t.Fatal("repository amendment did not create a new version")
	}

	status := ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status pending amendment: %v\nstdout:\n%sstderr:\n%s", status.err, status.stdout, status.stderr)
	}
	const wantStatus = "Working: continue locally\n" +
		"Based on:\n" +
		"  implement feature — amended; judgement pending\n" +
		"Repository amendment: fixed the authorization boundary before integration\n"
	if status.stdout != wantStatus {
		t.Fatalf("pending amendment status = %q, want %q", status.stdout, wantStatus)
	}

	syncResult := ion.run(grd, "sync")
	if syncResult.err == nil || syncResult.stderr != "grd sync: amended change has not been accepted yet\n" {
		t.Fatalf("pending amendment sync = (%v, %q, %q), want refusal", syncResult.err, syncResult.stdout, syncResult.stderr)
	}
	statusAfterRefusal := ion.run(grd, "status")
	if statusAfterRefusal.err != nil || statusAfterRefusal.stdout != wantStatus {
		t.Fatalf("status after refused sync = (%v, %q, %q), want unchanged pending amendment", statusAfterRefusal.err, statusAfterRefusal.stdout, statusAfterRefusal.stderr)
	}
	if got := ion.head(); got != continuedRevision {
		t.Fatalf("pending amendment moved workspace to %q, want continuation %q", got, continuedRevision)
	}
	if got := world.currentIntent().ContentRef.Revision; got != acceptedRevision {
		t.Fatalf("pending amendment moved accepted intent to %q, want %q", got, acceptedRevision)
	}
	if got := world.canonicalHead(); got != acceptedRevision {
		t.Fatalf("pending amendment moved canonical main to %q, want %q", got, acceptedRevision)
	}
	if output, err := runGitForTest("-C", ion.path, "rev-parse", "--verify", "--quiet", "refs/grd/recovery/"+continuedRevision); err == nil {
		t.Fatalf("pending amendment created a recovery ref before reconciliation: %q", output)
	}
	assertFileContent(t, ion.path+"/feature.txt", "unsafe\n")
	assertFileContent(t, ion.path+"/local.txt", "Ion kept working\n")
}
