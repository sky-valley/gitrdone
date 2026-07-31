package httpapi_test

import (
	"strings"
	"testing"
)

func TestJourneyRepositoryAmendmentUpdatesWorkspaceWithoutDescendants(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()

	originalRevision := ion.commitFile("feature.txt", "unsafe\n", "implement feature")
	if result := ion.run(grd, "submit"); result.err != nil {
		t.Fatalf("submit original change: %v\nstdout:\n%sstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	original := world.pendingProposal("ion")

	repositoryAgent := world.cloneWorkspace("repository-agent")
	amendedRevision := repositoryAgent.commitFile("feature.txt", "safe\n", "repair authorization boundary")
	requireGitSuccess(t, "publish amended content", "-C", repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/no-descendant-amendment")
	amended := amendJourneyChange(t, world, original, amendedRevision, "fixed the authorization boundary before integration", "journey-no-descendant-amendment")
	if amended.Promotion == nil {
		t.Fatal("repository amendment was not accepted")
	}

	result := ion.run(grd, "sync")
	if result.err != nil {
		t.Fatalf("sync accepted amendment: %v\nstdout:\n%sstderr:\n%s", result.err, result.stdout, result.stderr)
	}
	const wantOutput = "Synced: implement feature\n" +
		"Repository amendment: fixed the authorization boundary before integration\n" +
		"Workspace updated to accepted amendment.\n" +
		"Recovery: refs/grd/recovery/"
	if result.stdout != wantOutput+originalRevision+"\n" {
		t.Fatalf("sync stdout = %q, want %q", result.stdout, wantOutput+originalRevision+"\n")
	}
	if strings.Contains(result.stdout, "Replayed:") {
		t.Fatalf("sync stdout = %q, must not claim local work was replayed", result.stdout)
	}
	if got := ion.head(); got != amendedRevision {
		t.Fatalf("workspace HEAD = %q, want accepted amendment %q", got, amendedRevision)
	}
	if got := gitRevParse(t, ion.path, "refs/grd/recovery/"+originalRevision); got != originalRevision {
		t.Fatalf("recovery ref = %q, want original submission %q", got, originalRevision)
	}
	assertFileContent(t, ion.path+"/feature.txt", "safe\n")
	if !ion.isClean() {
		t.Fatal("workspace is dirty after accepting amendment")
	}

	status := ion.run(grd, "status")
	if status.err != nil || !strings.Contains(status.stdout, "Based on: accepted intent\n") {
		t.Fatalf("status after sync = (%v, %q, %q), want accepted intent", status.err, status.stdout, status.stderr)
	}
	retry := ion.run(grd, "sync")
	if retry.err == nil || retry.stderr != "grd sync: workspace has no submitted parent awaiting reconciliation\n" {
		t.Fatalf("second sync = (%v, %q, %q), want cleared continuation cursor", retry.err, retry.stdout, retry.stderr)
	}
}
