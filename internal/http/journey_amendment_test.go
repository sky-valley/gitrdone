package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestJourneyRepositoryAmendsHeldChangeAndSyncReplaysLocalContinuation(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()

	originalRevision := ion.commitFile("feature.txt", "unsafe\n", "implement feature")
	submit := ion.run(grd, "submit")
	if submit.err != nil {
		t.Fatalf("submit original change: %v\nstdout:\n%sstderr:\n%s", submit.err, submit.stdout, submit.stderr)
	}
	original := world.pendingProposal("ion")
	if original.Version.Content.Revision != originalRevision {
		t.Fatalf("pending original = %#v, want revision %q", original, originalRevision)
	}

	continuedRevision := ion.commitFile("local.txt", "Ion kept working\n", "continue locally")
	writeGitFile(t, ion.path, "dirty.txt", "not committed\n")
	dirtySync := ion.run(grd, "sync")
	if dirtySync.err == nil || dirtySync.stderr != "grd sync: workspace has uncommitted changes; commit or discard them before syncing\n" {
		t.Fatalf("dirty sync = (%v, %q), want safe refusal", dirtySync.err, dirtySync.stderr)
	}
	if got := ion.head(); got != continuedRevision {
		t.Fatalf("dirty sync moved HEAD to %q, want %q", got, continuedRevision)
	}
	if err := os.Remove(ion.path + "/dirty.txt"); err != nil {
		t.Fatalf("remove dirty fixture: %v", err)
	}

	repositoryAgent := world.cloneWorkspace("repository-agent")
	amendedRevision := repositoryAgent.commitFile("feature.txt", "safe\n", "repair authorization boundary")
	requireGitSuccess(t, "publish amended content", "-C", repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/amendment")
	amended := amendJourneyChange(t, world, original, amendedRevision, "fixed the authorization boundary before integration", "journey-amendment")
	if amended.Amended.Version.ChangeID != original.Change.ID || amended.Amended.Version.ID == original.Version.ID {
		t.Fatalf("amended identity = %#v, want same change and a new version", amended)
	}
	if amended.Amended.Amendment.FromVersion != original.Version.ID || amended.Amended.Amendment.ToVersion != amended.Amended.Version.ID {
		t.Fatalf("amendment lineage = %#v, want original to amended", amended.Amended.Amendment)
	}
	if amended.Promotion == nil || amended.Promotion.Promotion.VersionID != amended.Amended.Version.ID {
		t.Fatalf("promotion = %#v, want amended version", amended.Promotion)
	}
	if got := world.currentIntent().ContentRef.Revision; got != amendedRevision {
		t.Fatalf("accepted intent = %q, want amended revision %q", got, amendedRevision)
	}
	if got := world.canonicalHead(); got != amendedRevision {
		t.Fatalf("canonical main = %q, want amended revision %q", got, amendedRevision)
	}
	statusBeforeSync := ion.run(grd, "status")
	if statusBeforeSync.err != nil {
		t.Fatalf("status before amendment sync: %v\n%s", statusBeforeSync.err, statusBeforeSync.stderr)
	}
	const wantStatusBeforeSync = "Working: continue locally\nBased on:\n  implement feature — amended and accepted; run grd sync\n"
	if statusBeforeSync.stdout != wantStatusBeforeSync {
		t.Fatalf("status before sync = %q, want %q", statusBeforeSync.stdout, wantStatusBeforeSync)
	}

	syncResult := ion.run(grd, "sync")
	if syncResult.err != nil {
		t.Fatalf("sync amended change: %v\nstdout:\n%sstderr:\n%s", syncResult.err, syncResult.stdout, syncResult.stderr)
	}
	for _, want := range []string{
		"Synced: implement feature\n",
		"Repository amendment: fixed the authorization boundary before integration\n",
		"Replayed: 1 local commit\n",
		"Recovery: refs/grd/recovery/" + continuedRevision + "\n",
	} {
		if !strings.Contains(syncResult.stdout, want) {
			t.Fatalf("sync stdout = %q, want to contain %q", syncResult.stdout, want)
		}
	}
	if got := gitRevParse(t, ion.path, "refs/grd/recovery/"+continuedRevision); got != continuedRevision {
		t.Fatalf("recovery ref = %q, want original continuation %q", got, continuedRevision)
	}
	if got := ion.head(); got == continuedRevision {
		t.Fatalf("sync left HEAD at unreplayed continuation %q", got)
	}
	if output, err := runGitForTest("-C", ion.path, "merge-base", "--is-ancestor", amendedRevision, ion.head()); err != nil {
		t.Fatalf("replayed work is not based on amendment: %v\n%s", err, output)
	}
	assertFileContent(t, ion.path+"/feature.txt", "safe\n")
	assertFileContent(t, ion.path+"/local.txt", "Ion kept working\n")
	if !ion.isClean() {
		t.Fatal("workspace is dirty after successful sync")
	}
	status := ion.run(grd, "status")
	if status.err != nil || !strings.Contains(status.stdout, "Based on: accepted intent\n") {
		t.Fatalf("status after sync = (%v, %q, %q), want accepted intent", status.err, status.stdout, status.stderr)
	}

	res, body := request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/changes/"+string(original.Change.ID)+"/versions", controlAuthorization, "", "")
	requireStatus(t, res, body, http.StatusOK)
	var versions struct {
		Versions []struct {
			ID     string `json:"id"`
			Change string `json:"change"`
		} `json:"versions"`
	}
	decodeJSON(t, res, body, &versions)
	if len(versions.Versions) != 2 || versions.Versions[0].ID != string(original.Version.ID) || versions.Versions[1].ID != string(amended.Amended.Version.ID) {
		t.Fatalf("versions = %#v, want immutable original and amendment", versions.Versions)
	}
	for _, version := range versions.Versions {
		if version.Change != string(original.Change.ID) {
			t.Fatalf("version change = %q, want %q", version.Change, original.Change.ID)
		}
	}
}

func TestJourneyConflictingReplayPreservesTheOriginalWorkspace(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()

	ion.commitFile("feature.txt", "unsafe\n", "implement feature")
	if result := ion.run(grd, "submit"); result.err != nil {
		t.Fatalf("submit original change: %v\n%s", result.err, result.stderr)
	}
	original := world.pendingProposal("ion")
	continuedRevision := ion.commitFile("feature.txt", "Ion's competing fix\n", "continue on the same code")

	repositoryAgent := world.cloneWorkspace("repository-agent")
	amendedRevision := repositoryAgent.commitFile("feature.txt", "repository fix\n", "repair feature")
	requireGitSuccess(t, "publish conflicting amendment", "-C", repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/conflicting-amendment")
	amended := amendJourneyChange(t, world, original, amendedRevision, "repository chose a different repair", "journey-conflicting-amendment")

	result := ion.run(grd, "sync")
	if result.err == nil || result.stderr != "grd sync: reconciliation conflict is awaiting judgement\n" {
		t.Fatalf("conflicting sync = (%v, %q, %q), want recorded-conflict refusal", result.err, result.stdout, result.stderr)
	}
	if strings.Contains(result.stdout, "conflict_") {
		t.Fatalf("conflicting sync exposed machinery identity in normal UX: %q", result.stdout)
	}
	for _, want := range []string{
		"Sync needs judgement: implement feature\n",
		"Repository amendment: repository chose a different repair\n",
		"Reconciliation recorded; judgement pending.\n",
		"Affected:\n  feature.txt\n",
		"Workspace restored: refs/grd/recovery/" + continuedRevision + "\n",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Fatalf("conflicting sync stdout = %q, want %q", result.stdout, want)
		}
	}
	if got := ion.head(); got != continuedRevision {
		t.Fatalf("conflicting sync left HEAD at %q, want original %q", got, continuedRevision)
	}
	if got := gitRevParse(t, ion.path, "refs/grd/recovery/"+continuedRevision); got != continuedRevision {
		t.Fatalf("recovery ref = %q, want %q", got, continuedRevision)
	}
	assertFileContent(t, ion.path+"/feature.txt", "Ion's competing fix\n")
	if !ion.isClean() {
		t.Fatal("conflicting sync left Git conflict state in the workspace")
	}
	if got := world.currentIntent().ContentRef.Revision; got != amendedRevision {
		t.Fatalf("conflict moved accepted intent to %q, want amended B prime %q", got, amendedRevision)
	}
	if got := world.canonicalHead(); got != amendedRevision {
		t.Fatalf("conflict moved canonical main to %q, want amended B prime %q", got, amendedRevision)
	}

	res, body := request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/reconciliation-conflicts?limit=1", controlAuthorization, "", "")
	requireStatus(t, res, body, http.StatusOK)
	var recordedPage struct {
		Conflicts []struct {
			ID string `json:"id"`
		} `json:"conflicts"`
	}
	decodeJSON(t, res, body, &recordedPage)
	if len(recordedPage.Conflicts) != 1 || !strings.HasPrefix(recordedPage.Conflicts[0].ID, "conflict_") {
		t.Fatalf("recorded conflict page = %#v, want one durable conflict", recordedPage)
	}
	conflictID := recordedPage.Conflicts[0].ID

	res, body = request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/reconciliation-conflicts/"+conflictID, controlAuthorization, "", "")
	requireStatus(t, res, body, http.StatusOK)
	var conflict struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Change struct {
			ID string `json:"id"`
		} `json:"change"`
		Version struct {
			ID           string   `json:"id"`
			Change       string   `json:"change"`
			BaseIntent   string   `json:"baseIntent"`
			Producer     string   `json:"producer"`
			Dependencies []string `json:"dependencies"`
			ContentRef   struct {
				Engine   string `json:"engine"`
				Revision string `json:"revision"`
			} `json:"contentRef"`
		} `json:"version"`
		FromVersion   string   `json:"fromVersion"`
		ToVersion     string   `json:"toVersion"`
		BaseIntent    string   `json:"baseIntent"`
		ReportedBy    string   `json:"reportedBy"`
		AffectedPaths []string `json:"affectedPaths"`
	}
	decodeJSON(t, res, body, &conflict)
	if conflict.ID != conflictID || conflict.State != "awaiting_judgement" ||
		conflict.Change.ID == "" || conflict.Version.ID == "" || conflict.Version.Change != conflict.Change.ID {
		t.Fatalf("recorded conflict identity = %#v, want durable C change/version", conflict)
	}
	if conflict.Version.ContentRef.Engine != "git" || conflict.Version.ContentRef.Revision != continuedRevision ||
		conflict.Version.Producer != "ion" {
		t.Fatalf("recorded descendant = %#v, want Ion's immutable C %q", conflict.Version, continuedRevision)
	}
	if conflict.ReportedBy != "ion" {
		t.Fatalf("conflict reporter = %q, want authenticated ion", conflict.ReportedBy)
	}
	if len(conflict.Version.Dependencies) != 0 ||
		conflict.FromVersion != string(original.Version.ID) ||
		conflict.ToVersion != string(amended.Amended.Version.ID) {
		t.Fatalf("recorded lineage = %#v, want explicit B -> B prime conflict without an impossible promotion dependency", conflict)
	}
	if len(conflict.AffectedPaths) != 1 || conflict.AffectedPaths[0] != "feature.txt" {
		t.Fatalf("affected paths = %q, want feature.txt", conflict.AffectedPaths)
	}
	readToken := createRepoTokenFixture(t, world.server.handler, world.server.repo.ID, "read", "conflict-reader")
	res, body = request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/reconciliation-conflicts/"+conflictID, repoBasicAuthorization(readToken.Token), "", "")
	requireStatus(t, res, body, http.StatusOK)
	res, body = request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/reconciliation-conflicts?limit=1", repoBasicAuthorization(readToken.Token), "", "")
	requireStatus(t, res, body, http.StatusOK)
	var conflictPage struct {
		Conflicts []struct {
			ID string `json:"id"`
		} `json:"conflicts"`
		NextCursor string `json:"nextCursor"`
	}
	decodeJSON(t, res, body, &conflictPage)
	if len(conflictPage.Conflicts) != 1 || conflictPage.Conflicts[0].ID != conflictID || conflictPage.NextCursor != "" {
		t.Fatalf("read-token conflict discovery = %#v, want recorded conflict and no cursor", conflictPage)
	}
	readOnlyRecordBody := fmt.Sprintf(
		`{"fromVersion":%q,"toVersion":%q,"descendantVersion":%q}`,
		conflict.FromVersion,
		conflict.ToVersion,
		conflict.Version.ID,
	)
	res, body = requestWithHeaders(t, world.server.handler, http.MethodPost, "/v1/repos/"+world.server.repo.ID+"/reconciliation-conflicts", map[string]string{
		"Authorization":   repoBasicAuthorization(readToken.Token),
		"Content-Type":    "application/json",
		"Idempotency-Key": "read-only-conflict-record",
	}, readOnlyRecordBody)
	requireStatus(t, res, body, http.StatusForbidden)

	status := ion.run(grd, "status")
	if status.err != nil {
		t.Fatalf("status after conflict: %v\n%s", status.err, status.stderr)
	}
	wantStatus := "Working: continue on the same code\n" +
		"Based on:\n" +
		"  implement feature — amended and accepted\n" +
		"Reconciliation: awaiting judgement\n" +
		"Affected:\n" +
		"  feature.txt\n"
	if status.stdout != wantStatus {
		t.Fatalf("status after conflict = %q, want %q", status.stdout, wantStatus)
	}

	retry := ion.run(grd, "sync")
	if retry.err == nil || retry.stdout != "" || retry.stderr != "grd sync: reconciliation conflict is awaiting judgement\n" {
		t.Fatalf("conflict retry = (%v, %q, %q), want same durable wait without replay", retry.err, retry.stdout, retry.stderr)
	}
	if got := ion.head(); got != continuedRevision {
		t.Fatalf("conflict retry moved HEAD to %q, want %q", got, continuedRevision)
	}

	resolvedRevision := repositoryAgent.commitFile("feature.txt", "combined repository and Ion fix\n", "resolve competing feature edits")
	requireGitSuccess(t, "publish resolved content", "-C", repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/resolution")
	resolved := resolveJourneyConflict(
		t,
		world,
		conflictID,
		intent.VersionID(conflict.Version.ID),
		amended.Promotion.Intent.ID,
		resolvedRevision,
		"combined Ion's intent with the repository repair",
	)
	if resolved.Resolved.Change.ID != intent.ChangeID(conflict.Change.ID) ||
		resolved.Resolved.Version.ID == intent.VersionID(conflict.Version.ID) {
		t.Fatalf("resolved identity = %#v, want a new version of C", resolved.Resolved)
	}
	if resolved.Promotion == nil || resolved.Promotion.Promotion.VersionID != resolved.Resolved.Version.ID {
		t.Fatalf("resolved promotion = %#v, want accepted C prime", resolved.Promotion)
	}

	resolvedStatus := ion.run(grd, "status")
	if resolvedStatus.err != nil {
		t.Fatalf("status after repository resolution: %v\n%s", resolvedStatus.err, resolvedStatus.stderr)
	}
	wantResolvedStatus := "Working: continue on the same code\n" +
		"Based on:\n" +
		"  implement feature — amended and accepted\n" +
		"Reconciliation: resolved; run grd sync\n" +
		"Repository resolution: combined Ion's intent with the repository repair\n"
	if resolvedStatus.stdout != wantResolvedStatus {
		t.Fatalf("status after repository resolution = %q, want %q", resolvedStatus.stdout, wantResolvedStatus)
	}

	resolvedSync := ion.run(grd, "sync")
	if resolvedSync.err != nil {
		t.Fatalf("sync resolved conflict: %v\nstdout:\n%sstderr:\n%s", resolvedSync.err, resolvedSync.stdout, resolvedSync.stderr)
	}
	for _, want := range []string{
		"Synced: implement feature\n",
		"Repository resolution: combined Ion's intent with the repository repair\n",
		"Workspace updated to accepted resolution.\n",
		"Recovery: refs/grd/recovery/" + continuedRevision + "\n",
	} {
		if !strings.Contains(resolvedSync.stdout, want) {
			t.Fatalf("resolved sync stdout = %q, want to contain %q", resolvedSync.stdout, want)
		}
	}
	if got := ion.head(); got != resolvedRevision {
		t.Fatalf("resolved sync moved HEAD to %q, want C prime %q", got, resolvedRevision)
	}
	assertFileContent(t, ion.path+"/feature.txt", "combined repository and Ion fix\n")
	if !ion.isClean() {
		t.Fatal("resolved sync left the workspace dirty")
	}
}

func TestJourneyResolvedConflictReplaysNewerLocalWorkOntoAcceptedResolution(t *testing.T) {
	journey := stageConflictingReconciliationJourney(t)
	newerRevision := journey.ion.commitFile("notes.txt", "work after conflict capture\n", "keep working after conflict")
	resolvedRevision := journey.repositoryAgent.commitFile("feature.txt", "combined repository and Ion fix\n", "resolve competing feature edits")
	requireGitSuccess(t, "publish resolved content", "-C", journey.repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/resolution")
	resolveJourneyConflict(
		t,
		journey.world,
		journey.conflictID,
		journey.conflictVersion,
		journey.amended.Promotion.Intent.ID,
		resolvedRevision,
		"combined Ion's intent with the repository repair",
	)

	continuationConfig, err := runGitForTest("-C", journey.ion.path, "config", "--local", "--get-regexp", `^grd-workspace\..*\.state$`)
	if err != nil {
		t.Fatalf("read pre-sync continuation state: %v\n%s", err, continuationConfig)
	}
	continuationKey, continuationValue, ok := strings.Cut(strings.TrimSpace(continuationConfig), " ")
	if !ok || continuationKey == "" || continuationValue == "" {
		t.Fatalf("continuation config = %q, want key and durable state", continuationConfig)
	}

	resolvedSync := journey.ion.run(journey.grd, "sync")
	if resolvedSync.err != nil {
		t.Fatalf("sync resolved conflict with newer work: %v\nstdout:\n%sstderr:\n%s", resolvedSync.err, resolvedSync.stdout, resolvedSync.stderr)
	}
	for _, want := range []string{
		"Repository resolution: combined Ion's intent with the repository repair\n",
		"Replayed: 1 newer local commit\n",
		"Recovery: refs/grd/recovery/" + newerRevision + "\n",
	} {
		if !strings.Contains(resolvedSync.stdout, want) {
			t.Fatalf("resolved sync stdout = %q, want to contain %q", resolvedSync.stdout, want)
		}
	}
	if got := gitRevParse(t, journey.ion.path, "refs/grd/recovery/"+newerRevision); got != newerRevision {
		t.Fatalf("recovery ref = %q, want newer local work %q", got, newerRevision)
	}
	if output, err := runGitForTest("-C", journey.ion.path, "merge-base", "--is-ancestor", resolvedRevision, journey.ion.head()); err != nil {
		t.Fatalf("newer work is not based on accepted resolution: %v\n%s", err, output)
	}
	if got := journey.ion.head(); got == newerRevision || got == journey.capturedRevision {
		t.Fatalf("resolved sync left HEAD at pre-resolution revision %q", got)
	}
	assertFileContent(t, journey.ion.path+"/feature.txt", "combined repository and Ion fix\n")
	assertFileContent(t, journey.ion.path+"/notes.txt", "work after conflict capture\n")
	if !journey.ion.isClean() {
		t.Fatal("resolved sync left replayed workspace dirty")
	}

	appliedHead := journey.ion.head()
	requireGitSuccess(t, "restore continuation state after simulated interruption", "-C", journey.ion.path, "config", "--local", continuationKey, continuationValue)
	retry := journey.ion.run(journey.grd, "sync")
	if retry.err != nil {
		t.Fatalf("retry after workspace update and interrupted state cleanup: %v\nstdout:\n%sstderr:\n%s", retry.err, retry.stdout, retry.stderr)
	}
	if got := journey.ion.head(); got != appliedHead {
		t.Fatalf("interrupted retry moved HEAD to %q, want already-replayed %q", got, appliedHead)
	}
	for _, want := range []string{
		"Repository resolution: combined Ion's intent with the repository repair\n",
		"Workspace already contains the accepted resolution.\n",
	} {
		if !strings.Contains(retry.stdout, want) {
			t.Fatalf("interrupted retry stdout = %q, want to contain %q", retry.stdout, want)
		}
	}
	status := journey.ion.run(journey.grd, "status")
	if status.err != nil || !strings.Contains(status.stdout, "Based on: accepted intent\n") {
		t.Fatalf("status after interrupted retry = (%v, %q, %q), want accepted intent", status.err, status.stdout, status.stderr)
	}
}

func TestJourneySupersededConflictReentersReconciliationAgainstCurrentIntent(t *testing.T) {
	journey := stageConflictingReconciliationJourney(t)
	current := journey.world.currentIntent()
	advancedRevision := journey.repositoryAgent.commitFile("unrelated.txt", "later intent\n", "advance unrelated intent")
	requireGitSuccess(
		t,
		"publish later intent",
		"-C",
		journey.repositoryAgent.path,
		"push",
		"origin",
		"HEAD:refs/candidates/later-intent",
	)
	advanced := admitAndAcceptJourneyChange(t, journey.world, intentservice.Proposal{
		IdempotencyKey: "journey-later-intent",
		BaseIntent:     intent.RevisionID(current.ID),
		Content:        intent.ContentRef{Engine: "git", Revision: advancedRevision},
		Producer:       "noam",
	})
	if advanced.Promotion == nil {
		t.Fatal("later intent was not promoted")
	}

	res, body := request(
		t,
		journey.world.server.handler,
		http.MethodGet,
		"/v1/repos/"+journey.world.server.repo.ID+"/reconciliation-conflicts/"+journey.conflictID,
		controlAuthorization,
		"",
		"",
	)
	requireStatus(t, res, body, http.StatusOK)
	var stale struct {
		State string `json:"state"`
	}
	decodeJSON(t, res, body, &stale)
	if stale.State != "superseded" {
		t.Fatalf("old conflict state = %q, want superseded", stale.State)
	}
	status := journey.ion.run(journey.grd, "status")
	if status.err != nil {
		t.Fatalf("status with superseded conflict: %v\n%s", status.err, status.stderr)
	}
	if !strings.Contains(status.stdout, "Reconciliation: superseded by newer accepted intent; run grd sync\n") {
		t.Fatalf("status with superseded conflict = %q, want actionable sync guidance", status.stdout)
	}

	sync := journey.ion.run(journey.grd, "sync")
	if sync.err == nil || sync.stderr != "grd sync: reconciliation conflict is awaiting judgement\n" {
		t.Fatalf("sync after intent advance = (%v, %q, %q), want a fresh durable conflict", sync.err, sync.stdout, sync.stderr)
	}
	if got := journey.ion.head(); got != journey.capturedRevision {
		t.Fatalf("fresh reconciliation attempt moved workspace to %q, want restored %q", got, journey.capturedRevision)
	}
	res, body = request(
		t,
		journey.world.server.handler,
		http.MethodGet,
		"/v1/repos/"+journey.world.server.repo.ID+"/reconciliation-conflicts?limit=10",
		controlAuthorization,
		"",
		"",
	)
	requireStatus(t, res, body, http.StatusOK)
	var page struct {
		Conflicts []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"conflicts"`
	}
	decodeJSON(t, res, body, &page)
	if len(page.Conflicts) != 2 ||
		page.Conflicts[0].ID != journey.conflictID ||
		page.Conflicts[0].State != "superseded" ||
		page.Conflicts[1].ID == journey.conflictID ||
		page.Conflicts[1].State != "awaiting_judgement" {
		t.Fatalf("conflicts after re-entry = %#v, want superseded attempt then fresh awaiting attempt", page.Conflicts)
	}
}

func TestJourneyResolvedConflictRestoresNewerWorkWhenReplayStillConflicts(t *testing.T) {
	journey := stageConflictingReconciliationJourney(t)
	newerRevision := journey.ion.commitFile("feature.txt", "Ion revised the feature again\n", "keep changing conflicted code")
	resolvedRevision := journey.repositoryAgent.commitFile("feature.txt", "combined repository and Ion fix\n", "resolve competing feature edits")
	requireGitSuccess(t, "publish resolved content", "-C", journey.repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/resolution")
	resolveJourneyConflict(
		t,
		journey.world,
		journey.conflictID,
		journey.conflictVersion,
		journey.amended.Promotion.Intent.ID,
		resolvedRevision,
		"combined Ion's captured intent with the repository repair",
	)

	result := journey.ion.run(journey.grd, "sync")
	wantError := "grd sync: resolved-work replay conflicted; workspace restored from refs/grd/recovery/" + newerRevision + "\n"
	if result.err == nil || result.stderr != wantError {
		t.Fatalf("conflicting resolved-work sync = (%v, %q, %q), want restored refusal", result.err, result.stdout, result.stderr)
	}
	if got := journey.ion.head(); got != newerRevision {
		t.Fatalf("conflicting resolved-work sync left HEAD at %q, want %q", got, newerRevision)
	}
	assertFileContent(t, journey.ion.path+"/feature.txt", "Ion revised the feature again\n")
	if !journey.ion.isClean() {
		t.Fatal("conflicting resolved-work sync did not restore a clean workspace")
	}
	status := journey.ion.run(journey.grd, "status")
	if status.err != nil || !strings.Contains(status.stdout, "Reconciliation: resolved; run grd sync\n") {
		t.Fatalf("status after restored resolved-work conflict = (%v, %q, %q), want unresolved portal work", status.err, status.stdout, status.stderr)
	}
}

func TestJourneyResolvedConflictWaitsForResolutionJudgementBeforePortalSync(t *testing.T) {
	journey := stageConflictingReconciliationJourney(t)
	resolvedRevision := journey.repositoryAgent.commitFile("feature.txt", "combined repository and Ion fix\n", "resolve competing feature edits")
	requireGitSuccess(t, "publish resolved content", "-C", journey.repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/resolution")
	resolved := resolveJourneyConflictPending(
		t,
		journey.world,
		journey.conflictID,
		journey.conflictVersion,
		journey.amended.Promotion.Intent.ID,
		resolvedRevision,
		"combined Ion's captured intent with the repository repair",
	)
	if resolved.Promotion != nil {
		t.Fatalf("resolution promotion = %#v, want judgement pending", resolved.Promotion)
	}

	status := journey.ion.run(journey.grd, "status")
	if status.err != nil {
		t.Fatalf("status with pending resolution judgement: %v\n%s", status.err, status.stderr)
	}
	wantStatus := "Working: continue on the same code\n" +
		"Based on:\n" +
		"  implement feature — amended and accepted\n" +
		"Reconciliation: resolution awaiting judgement\n" +
		"Repository resolution: combined Ion's captured intent with the repository repair\n"
	if status.stdout != wantStatus {
		t.Fatalf("pending resolution status = %q, want %q", status.stdout, wantStatus)
	}

	syncResult := journey.ion.run(journey.grd, "sync")
	if syncResult.err == nil || syncResult.stderr != "grd sync: reconciliation resolution is awaiting judgement\n" {
		t.Fatalf("pending resolution sync = (%v, %q, %q), want truthful refusal", syncResult.err, syncResult.stdout, syncResult.stderr)
	}
	if got := journey.ion.head(); got != journey.capturedRevision {
		t.Fatalf("pending resolution sync moved HEAD to %q, want captured C %q", got, journey.capturedRevision)
	}
	if got := journey.world.currentIntent().ContentRef.Revision; got == resolvedRevision {
		t.Fatalf("pending resolution moved accepted intent to unpromoted C prime %q", got)
	}
}

func TestJourneyDeferredResolutionFollowsEffectiveRebaseWhenIntentAdvances(t *testing.T) {
	journey := stageConflictingReconciliationJourney(t)
	resolvedRevision := journey.repositoryAgent.commitFile("feature.txt", "combined repository and Ion fix\n", "resolve competing feature edits")
	requireGitSuccess(t, "publish resolved content", "-C", journey.repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/resolution")
	resolved := resolveJourneyConflictPending(
		t,
		journey.world,
		journey.conflictID,
		journey.conflictVersion,
		journey.amended.Promotion.Intent.ID,
		resolvedRevision,
		"combined Ion's captured intent with the repository repair",
	)
	if resolved.Promotion != nil {
		t.Fatalf("resolution promotion = %#v, want judgement pending", resolved.Promotion)
	}

	advancer := journey.world.cloneWorkspace("advancer")
	advancedRevision := advancer.commitFile("unrelated.txt", "later intent\n", "advance unrelated intent")
	requireGitSuccess(t, "publish later intent", "-C", advancer.path, "push", "origin", "HEAD:refs/candidates/later-intent")
	current := journey.world.currentIntent()
	advanced := admitAndAcceptJourneyChange(t, journey.world, intentservice.Proposal{
		IdempotencyKey: "journey-later-intent-after-resolution",
		BaseIntent:     intent.RevisionID(current.ID),
		Content:        intent.ContentRef{Engine: "git", Revision: advancedRevision},
		Producer:       "noam",
	})
	if advanced.Promotion == nil {
		t.Fatal("later intent was not promoted")
	}

	status := journey.ion.run(journey.grd, "status")
	if status.err != nil {
		t.Fatalf("status with stale deferred resolution: %v\n%s", status.err, status.stderr)
	}
	if !strings.Contains(status.stdout, "Reconciliation: resolution superseded; repository rebase pending\n") {
		t.Fatalf("status with stale deferred resolution = %q, want repository rebase pending", status.stdout)
	}
	syncResult := journey.ion.run(journey.grd, "sync")
	if syncResult.err == nil || syncResult.stderr != "grd sync: reconciliation resolution requires repository rebase\n" {
		t.Fatalf("sync after deferred resolution became stale = (%v, %q, %q), want honest wait", syncResult.err, syncResult.stdout, syncResult.stderr)
	}

	rebaseEngine := journey.world.cloneWorkspace("rebase-engine")
	requireGitSuccess(t, "fetch resolved candidate", "-C", rebaseEngine.path, "fetch", "origin", "refs/candidates/resolution")
	requireGitSuccess(t, "rebase resolved content onto later intent", "-C", rebaseEngine.path, "cherry-pick", resolvedRevision)
	effectiveRevision := rebaseEngine.head()
	requireGitSuccess(t, "publish effective resolution", "-C", rebaseEngine.path, "push", "origin", "HEAD:refs/candidates/effective-resolution")
	rebased, err := journey.world.server.judgement.RebaseHeldVersion(
		context.Background(),
		journey.world.server.repo.ID,
		intentservice.HeldVersionRebaseRequest{
			IdempotencyKey:  "journey-rebase-held-resolution",
			ExpectedVersion: resolved.Resolved.Version.ID,
			ExpectedIntent:  advanced.Promotion.Intent.ID,
			Content:         intent.ContentRef{Engine: "git", Revision: effectiveRevision},
			Producer:        "rebase-engine",
			Rationale:       "replay resolved change onto later accepted intent",
		},
	)
	if err != nil {
		t.Fatalf("record effective resolution rebase: %v", err)
	}
	finalRevision := rebaseEngine.commitFile("feature.txt", "combined repository and Ion fix, linted\n", "finish effective resolution")
	requireGitSuccess(t, "publish amended effective resolution", "-C", rebaseEngine.path, "push", "origin", "HEAD:refs/candidates/final-effective-resolution")
	final, err := journey.world.server.judgement.Amend(
		context.Background(),
		journey.world.server.repo.ID,
		intentservice.AmendmentRequest{
			IdempotencyKey:  "journey-amend-effective-resolution",
			ChangeID:        rebased.Change.ID,
			ExpectedVersion: rebased.Version.ID,
			Content:         intent.ContentRef{Engine: "git", Revision: finalRevision},
			Producer:        "final-fix-engine",
			Rationale:       "finish the rebased resolution",
		},
	)
	if err != nil {
		t.Fatalf("amend effective resolution: %v", err)
	}
	finalPromotion, err := journey.world.server.judgement.Promote(context.Background(), journey.world.server.repo.ID, intent.PromoteRequest{
		VersionID:      final.Version.ID,
		ExpectedIntent: advanced.Promotion.Intent.ID,
	})
	if err != nil {
		t.Fatalf("promote amended effective resolution: %v", err)
	}
	if finalPromotion.Promotion.VersionID != final.Version.ID {
		t.Fatalf("amended effective resolution promotion = %#v, want final version", finalPromotion)
	}

	status = journey.ion.run(journey.grd, "status")
	if status.err != nil || !strings.Contains(status.stdout, "Reconciliation: resolved; run grd sync\n") {
		t.Fatalf("status with accepted effective resolution = (%v, %q, %q), want portal ready", status.err, status.stdout, status.stderr)
	}
	syncResult = journey.ion.run(journey.grd, "sync")
	if syncResult.err != nil {
		t.Fatalf("sync accepted effective resolution: %v\nstdout:\n%sstderr:\n%s", syncResult.err, syncResult.stdout, syncResult.stderr)
	}
	if got := journey.ion.head(); got != finalRevision {
		t.Fatalf("effective resolution sync head = %q, want %q", got, finalRevision)
	}
	assertFileContent(t, journey.ion.path+"/feature.txt", "combined repository and Ion fix, linted\n")
	assertFileContent(t, journey.ion.path+"/unrelated.txt", "later intent\n")
}

func TestJourneyResolvedConflictRefusesToFlattenNewerMergeHistory(t *testing.T) {
	journey := stageConflictingReconciliationJourney(t)
	requireGitSuccess(t, "create side branch after captured conflict", "-C", journey.ion.path, "checkout", "-b", "side-work")
	journey.ion.commitFile("side.txt", "side work\n", "add side work")
	requireGitSuccess(t, "return to main work", "-C", journey.ion.path, "checkout", "main")
	journey.ion.commitFile("main.txt", "main work\n", "add main work")
	requireGitSuccess(t, "merge newer local work", "-C", journey.ion.path, "merge", "--no-ff", "side-work", "-m", "combine newer work")
	mergeRevision := journey.ion.head()

	resolvedRevision := journey.repositoryAgent.commitFile("feature.txt", "combined repository and Ion fix\n", "resolve competing feature edits")
	requireGitSuccess(t, "publish resolved content", "-C", journey.repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/resolution")
	resolveJourneyConflict(
		t,
		journey.world,
		journey.conflictID,
		journey.conflictVersion,
		journey.amended.Promotion.Intent.ID,
		resolvedRevision,
		"combined Ion's captured intent with the repository repair",
	)

	result := journey.ion.run(journey.grd, "sync")
	if result.err == nil || result.stderr != "grd sync: newer local work contains merge commits; automatic portal replay is not safe\n" {
		t.Fatalf("merge-history sync = (%v, %q, %q), want safe refusal", result.err, result.stdout, result.stderr)
	}
	if got := journey.ion.head(); got != mergeRevision {
		t.Fatalf("merge-history refusal moved HEAD to %q, want %q", got, mergeRevision)
	}
	if !journey.ion.isClean() {
		t.Fatal("merge-history refusal dirtied the workspace")
	}
}

type conflictingReconciliationJourney struct {
	world            *journeyWorld
	ion              *journeyWorkspace
	grd              string
	repositoryAgent  *journeyWorkspace
	amended          journeyAmendmentReceipt
	conflictID       string
	conflictVersion  intent.VersionID
	capturedRevision string
}

func stageConflictingReconciliationJourney(t *testing.T) conflictingReconciliationJourney {
	t.Helper()
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()

	ion.commitFile("feature.txt", "unsafe\n", "implement feature")
	if result := ion.run(grd, "submit"); result.err != nil {
		t.Fatalf("submit original change: %v\n%s", result.err, result.stderr)
	}
	original := world.pendingProposal("ion")
	capturedRevision := ion.commitFile("feature.txt", "Ion's competing fix\n", "continue on the same code")

	repositoryAgent := world.cloneWorkspace("repository-agent")
	amendedRevision := repositoryAgent.commitFile("feature.txt", "repository fix\n", "repair feature")
	requireGitSuccess(t, "publish conflicting amendment", "-C", repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/conflicting-amendment")
	amended := amendJourneyChange(t, world, original, amendedRevision, "repository chose a different repair", "journey-replay-amendment")

	conflictingSync := ion.run(grd, "sync")
	if conflictingSync.err == nil {
		t.Fatal("conflicting sync unexpectedly succeeded")
	}
	if strings.Contains(conflictingSync.stdout, "conflict_") {
		t.Fatalf("conflicting sync exposed machinery identity in normal UX: %q", conflictingSync.stdout)
	}
	res, body := request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/reconciliation-conflicts?limit=1", controlAuthorization, "", "")
	requireStatus(t, res, body, http.StatusOK)
	var page struct {
		Conflicts []struct {
			ID string `json:"id"`
		} `json:"conflicts"`
	}
	decodeJSON(t, res, body, &page)
	if len(page.Conflicts) != 1 {
		t.Fatalf("conflict page = %#v, want one durable conflict", page)
	}
	conflictID := page.Conflicts[0].ID

	res, body = request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/reconciliation-conflicts/"+conflictID, controlAuthorization, "", "")
	requireStatus(t, res, body, http.StatusOK)
	var conflict struct {
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
	}
	decodeJSON(t, res, body, &conflict)

	return conflictingReconciliationJourney{
		world:            world,
		ion:              ion,
		grd:              grd,
		repositoryAgent:  repositoryAgent,
		amended:          amended,
		conflictID:       conflictID,
		conflictVersion:  intent.VersionID(conflict.Version.ID),
		capturedRevision: capturedRevision,
	}
}

type journeyAmendmentReceipt struct {
	Amended   intent.Amended
	Promotion *intent.Promoted
}

func amendJourneyChangePending(t *testing.T, world *journeyWorld, original intent.Proposed, revision, rationale, idempotencyKey string) intent.Amended {
	t.Helper()
	amended, err := world.server.judgement.Amend(context.Background(), world.server.repo.ID, intentservice.AmendmentRequest{
		IdempotencyKey:  idempotencyKey,
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: revision},
		Producer:        "repository-agent",
		Rationale:       rationale,
	})
	if err != nil {
		t.Fatalf("amend journey change: %v", err)
	}
	return amended
}

func amendJourneyChange(t *testing.T, world *journeyWorld, original intent.Proposed, revision, rationale, idempotencyKey string) journeyAmendmentReceipt {
	t.Helper()
	amended := amendJourneyChangePending(t, world, original, revision, rationale, idempotencyKey)
	promoted, err := world.server.judgement.Promote(context.Background(), world.server.repo.ID, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: intent.RevisionID(world.currentIntent().ID),
	})
	if err != nil {
		t.Fatalf("promote amended journey change: %v", err)
	}
	return journeyAmendmentReceipt{Amended: amended, Promotion: &promoted}
}

type journeyPromotionReceipt struct {
	Promotion *intent.Promoted
}

func admitAndAcceptJourneyChange(t *testing.T, world *journeyWorld, proposal intentservice.Proposal) journeyPromotionReceipt {
	t.Helper()
	admitted, err := world.server.judgement.Propose(context.Background(), world.server.repo.ID, proposal)
	if err != nil {
		t.Fatalf("admit journey change: %v", err)
	}
	promoted, err := world.server.judgement.Promote(context.Background(), world.server.repo.ID, intent.PromoteRequest{
		VersionID:      admitted.Version.ID,
		ExpectedIntent: intent.RevisionID(world.currentIntent().ID),
	})
	if err != nil {
		t.Fatalf("accept journey change: %v", err)
	}
	return journeyPromotionReceipt{Promotion: &promoted}
}

type journeyResolutionReceipt struct {
	Resolved  intent.ResolvedReconciliationConflict
	Promotion *intent.Promoted
}

func resolveJourneyConflict(
	t *testing.T,
	world *journeyWorld,
	conflictID string,
	expectedVersion intent.VersionID,
	expectedIntent intent.RevisionID,
	revision string,
	rationale string,
) journeyResolutionReceipt {
	t.Helper()
	resolved := resolveJourneyConflictPending(t, world, conflictID, expectedVersion, expectedIntent, revision, rationale)
	promoted, err := world.server.judgement.Promote(context.Background(), world.server.repo.ID, intent.PromoteRequest{
		VersionID:      resolved.Resolved.Version.ID,
		ExpectedIntent: intent.RevisionID(world.currentIntent().ID),
	})
	if err != nil {
		t.Fatalf("promote journey conflict resolution: %v", err)
	}
	resolved.Promotion = &promoted
	return resolved
}

func resolveJourneyConflictPending(
	t *testing.T,
	world *journeyWorld,
	conflictID string,
	expectedVersion intent.VersionID,
	expectedIntent intent.RevisionID,
	revision string,
	rationale string,
) journeyResolutionReceipt {
	t.Helper()
	resolved, err := world.server.judgement.ResolveReconciliationConflict(
		context.Background(),
		world.server.repo.ID,
		intentservice.ReconciliationResolutionRequest{
			IdempotencyKey:  "journey-resolve-" + conflictID,
			ConflictID:      intent.ConflictID(conflictID),
			ExpectedVersion: expectedVersion,
			ExpectedIntent:  expectedIntent,
			Content:         intent.ContentRef{Engine: "git", Revision: revision},
			Producer:        "repository-agent",
			ResolvedBy:      "judgement-agent",
			Rationale:       rationale,
		},
	)
	if err != nil {
		t.Fatalf("resolve journey conflict: %v", err)
	}
	return journeyResolutionReceipt{Resolved: resolved}
}
