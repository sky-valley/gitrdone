package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestJourneyRepositoryAmendsHeldChangeAndSyncReplaysLocalContinuation(t *testing.T) {
	decider := &deferIonDecider{}
	world := newJourneyWorldWithDecider(t, decider)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()

	originalRevision := ion.commitFile("feature.txt", "unsafe\n", "implement feature")
	submit := ion.run(grd, "submit")
	if submit.err != nil {
		t.Fatalf("submit original change: %v\nstdout:\n%sstderr:\n%s", submit.err, submit.stdout, submit.stderr)
	}
	original, ok := decider.original()
	if !ok || original.Version.Content.Revision != originalRevision {
		t.Fatalf("decider original = %#v, want revision %q", original, originalRevision)
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
	decider := &deferIonDecider{}
	world := newJourneyWorldWithDecider(t, decider)
	ion := world.cloneWorkspace("ion")
	grd := world.buildGRD()

	ion.commitFile("feature.txt", "unsafe\n", "implement feature")
	if result := ion.run(grd, "submit"); result.err != nil {
		t.Fatalf("submit original change: %v\n%s", result.err, result.stderr)
	}
	original, ok := decider.original()
	if !ok {
		t.Fatal("decider did not observe Ion's original change")
	}
	continuedRevision := ion.commitFile("feature.txt", "Ion's competing fix\n", "continue on the same code")

	repositoryAgent := world.cloneWorkspace("repository-agent")
	amendedRevision := repositoryAgent.commitFile("feature.txt", "repository fix\n", "repair feature")
	requireGitSuccess(t, "publish conflicting amendment", "-C", repositoryAgent.path, "push", "origin", "HEAD:refs/candidates/conflicting-amendment")
	amended := amendJourneyChange(t, world, original, amendedRevision, "repository chose a different repair", "journey-conflicting-amendment")

	result := ion.run(grd, "sync")
	if result.err == nil || result.stderr != "grd sync: reconciliation conflict is awaiting judgement\n" {
		t.Fatalf("conflicting sync = (%v, %q, %q), want recorded-conflict refusal", result.err, result.stdout, result.stderr)
	}
	const conflictPrefix = "Conflict recorded: "
	conflictStart := strings.Index(result.stdout, conflictPrefix)
	if conflictStart < 0 {
		t.Fatalf("conflicting sync stdout = %q, want conflict identity", result.stdout)
	}
	conflictID := strings.SplitN(result.stdout[conflictStart+len(conflictPrefix):], "\n", 2)[0]
	if !strings.HasPrefix(conflictID, "conflict_") {
		t.Fatalf("conflict id = %q, want generated conflict identity", conflictID)
	}
	for _, want := range []string{
		"Sync needs judgement: implement feature\n",
		"Repository amendment: repository chose a different repair\n",
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

	res, body := request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/reconciliation-conflicts/"+conflictID, controlAuthorization, "", "")
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
		"Reconciliation: " + conflictID + " — awaiting judgement\n" +
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
}

func amendJourneyChange(t *testing.T, world *journeyWorld, original intent.Proposed, revision, rationale, idempotencyKey string) intentservice.AmendmentReceipt {
	t.Helper()
	receipt, err := world.server.judgement.Amend(context.Background(), world.server.repo.ID, intentservice.AmendmentRequest{
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
	return receipt
}

type deferIonDecider struct {
	mu       sync.Mutex
	proposed []intent.Proposed
}

func (decider *deferIonDecider) DecidePromotion(_ context.Context, subject intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	proposed := intent.Proposed{Change: subject.Change, Version: subject.Version}
	decider.proposed = append(decider.proposed, proposed)
	if proposed.Version.Producer == "ion" {
		return intentservice.DeferPromotion, nil
	}
	return intentservice.PromoteNow, nil
}

func (decider *deferIonDecider) original() (intent.Proposed, bool) {
	decider.mu.Lock()
	defer decider.mu.Unlock()
	for _, proposed := range decider.proposed {
		if proposed.Version.Producer == "ion" {
			return proposed, true
		}
	}
	return intent.Proposed{}, false
}
