package intentapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentapi"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestNativeIntentAPIRecordsAndReadsReconciliationConflict(t *testing.T) {
	repository, _ := newRepository(t)
	initial := repository.CurrentIntent()
	original, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose B: %v", err)
	}
	amended, err := repository.Amend(context.Background(), intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend B: %v", err)
	}
	if _, err := repository.Promote(context.Background(), intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: initial.ID,
	}); err != nil {
		t.Fatalf("promote B prime: %v", err)
	}
	descendant, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose C: %v", err)
	}
	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))
	body := []byte(`{
		"fromVersion":"` + string(original.Version.ID) + `",
		"toVersion":"` + string(amended.Version.ID) + `",
		"descendantVersion":"` + string(descendant.Version.ID) + `",
		"expectedIntent":"` + string(repository.CurrentIntent().ID) + `"
	}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/reconciliation-conflicts", bytes.NewReader(body))
	request.SetPathValue("repoID", "repo_123")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "conflict-b-c")
	recorder := httptest.NewRecorder()

	handlers.RecordReconciliationConflict.ServeHTTP(recorder, intentapi.WithAuthenticatedProducer(request, "ion"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("record status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var recorded reconciliationConflictResponse
	decodeResponse(t, recorder, &recorded)
	if recorded.ID == "" || recorded.State != "awaiting_judgement" {
		t.Fatalf("recorded conflict = %#v, want durable awaiting-judgement identity", recorded)
	}
	if recorded.Change.ID != string(descendant.Change.ID) ||
		recorded.Version.ID != string(descendant.Version.ID) ||
		recorded.Version.Change != recorded.Change.ID {
		t.Fatalf("descendant identity = %#v / %#v, want existing C %#v", recorded.Change, recorded.Version, descendant)
	}
	if recorded.Version.BaseIntent != string(initial.ID) ||
		recorded.Version.Producer != "ion" ||
		recorded.Version.ContentRef.Engine != "git" ||
		recorded.Version.ContentRef.Revision != "cccccccc" {
		t.Fatalf("descendant version = %#v, want captured C", recorded.Version)
	}
	if len(recorded.Version.Dependencies) != 0 {
		t.Fatalf("descendant dependencies = %q, want provenance carried by conflict lineage", recorded.Version.Dependencies)
	}
	if recorded.FromVersion != string(original.Version.ID) || recorded.ToVersion != string(amended.Version.ID) {
		t.Fatalf("lineage = %q -> %q, want B -> B prime", recorded.FromVersion, recorded.ToVersion)
	}
	if recorded.ReportedBy != "ion" {
		t.Fatalf("reported by = %q, want authenticated ion", recorded.ReportedBy)
	}
	if len(recorded.AffectedPaths) != 0 {
		t.Fatalf("affected paths = %q, want optional diagnostics omitted", recorded.AffectedPaths)
	}

	get := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/reconciliation-conflicts/"+recorded.ID, nil)
	get.SetPathValue("repoID", "repo_123")
	get.SetPathValue("conflictID", recorded.ID)
	getRecorder := httptest.NewRecorder()
	handlers.GetReconciliationConflict.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200: %s", getRecorder.Code, getRecorder.Body.String())
	}
	var loaded reconciliationConflictResponse
	decodeResponse(t, getRecorder, &loaded)
	if !reconciliationConflictResponsesEqual(loaded, recorded) {
		t.Fatalf("loaded conflict = %#v, want %#v", loaded, recorded)
	}

	legacyBody := []byte(`{
		"fromVersion":"` + string(original.Version.ID) + `",
		"toVersion":"` + string(amended.Version.ID) + `",
		"descendantVersion":"` + string(descendant.Version.ID) + `"
	}`)
	retry := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/reconciliation-conflicts", bytes.NewReader(legacyBody))
	retry.SetPathValue("repoID", "repo_123")
	retry.Header.Set("Content-Type", "application/json")
	retry.Header.Set("Idempotency-Key", "conflict-b-c")
	retryRecorder := httptest.NewRecorder()
	handlers.RecordReconciliationConflict.ServeHTTP(retryRecorder, intentapi.WithAuthenticatedProducer(retry, "ion"))
	if retryRecorder.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200: %s", retryRecorder.Code, retryRecorder.Body.String())
	}
	var retried reconciliationConflictResponse
	decodeResponse(t, retryRecorder, &retried)
	if !reconciliationConflictResponsesEqual(retried, recorded) {
		t.Fatalf("retried conflict = %#v, want %#v", retried, recorded)
	}

	secondDescendant, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "proposal-d",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose D: %v", err)
	}
	secondRecorded, err := repository.RecordReconciliationConflict(context.Background(), intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-b-d",
		FromVersion:       original.Version.ID,
		ToVersion:         amended.Version.ID,
		DescendantVersion: secondDescendant.Version.ID,
		ExpectedIntent:    repository.CurrentIntent().ID,
		ReportedBy:        "ion",
	})
	if err != nil {
		t.Fatalf("record second conflict: %v", err)
	}

	listFirst := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/reconciliation-conflicts?limit=1", nil)
	listFirst.SetPathValue("repoID", "repo_123")
	listFirstRecorder := httptest.NewRecorder()
	handlers.ListReconciliationConflicts.ServeHTTP(listFirstRecorder, listFirst)
	if listFirstRecorder.Code != http.StatusOK {
		t.Fatalf("list first page status = %d, want 200: %s", listFirstRecorder.Code, listFirstRecorder.Body.String())
	}
	var firstPage reconciliationConflictPageResponse
	decodeResponse(t, listFirstRecorder, &firstPage)
	if len(firstPage.Conflicts) != 1 ||
		!reconciliationConflictResponsesEqual(firstPage.Conflicts[0], recorded) ||
		firstPage.NextCursor != recorded.ID {
		t.Fatalf("first conflict page = %#v, want first conflict and cursor %q", firstPage, recorded.ID)
	}

	listSecond := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/reconciliation-conflicts?limit=1&cursor="+firstPage.NextCursor, nil)
	listSecond.SetPathValue("repoID", "repo_123")
	listSecondRecorder := httptest.NewRecorder()
	handlers.ListReconciliationConflicts.ServeHTTP(listSecondRecorder, listSecond)
	if listSecondRecorder.Code != http.StatusOK {
		t.Fatalf("list second page status = %d, want 200: %s", listSecondRecorder.Code, listSecondRecorder.Body.String())
	}
	var secondPage reconciliationConflictPageResponse
	decodeResponse(t, listSecondRecorder, &secondPage)
	if len(secondPage.Conflicts) != 1 ||
		secondPage.Conflicts[0].ID != string(secondRecorded.ID) ||
		secondPage.Conflicts[0].Version.ID != string(secondRecorded.Version.ID) ||
		secondPage.NextCursor != "" {
		t.Fatalf("second conflict page = %#v, want second conflict and no cursor", secondPage)
	}

	invalidCursor := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/reconciliation-conflicts?cursor=conflict_unknown", nil)
	invalidCursor.SetPathValue("repoID", "repo_123")
	invalidCursorRecorder := httptest.NewRecorder()
	handlers.ListReconciliationConflicts.ServeHTTP(invalidCursorRecorder, invalidCursor)
	if invalidCursorRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d, want 400: %s", invalidCursorRecorder.Code, invalidCursorRecorder.Body.String())
	}
	invalidLimit := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/reconciliation-conflicts?limit=101", nil)
	invalidLimit.SetPathValue("repoID", "repo_123")
	invalidLimitRecorder := httptest.NewRecorder()
	handlers.ListReconciliationConflicts.ServeHTTP(invalidLimitRecorder, invalidLimit)
	if invalidLimitRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d, want 400: %s", invalidLimitRecorder.Code, invalidLimitRecorder.Body.String())
	}

	resolved, err := repository.ResolveReconciliationConflict(context.Background(), intent.ResolveReconciliationConflictRequest{
		IdempotencyKey:  "resolve-b-c",
		ConflictID:      intent.ConflictID(recorded.ID),
		ExpectedVersion: descendant.Version.ID,
		ExpectedIntent:  repository.CurrentIntent().ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:        "repository-agent",
		ResolvedBy:      "judgement-agent",
		Rationale:       "replayed C onto accepted B prime",
	})
	if err != nil {
		t.Fatalf("resolve recorded conflict: %v", err)
	}
	resolvedGet := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/reconciliation-conflicts/"+recorded.ID, nil)
	resolvedGet.SetPathValue("repoID", "repo_123")
	resolvedGet.SetPathValue("conflictID", recorded.ID)
	resolvedRecorder := httptest.NewRecorder()
	handlers.GetReconciliationConflict.ServeHTTP(resolvedRecorder, resolvedGet)
	if resolvedRecorder.Code != http.StatusOK {
		t.Fatalf("resolved get status = %d, want 200: %s", resolvedRecorder.Code, resolvedRecorder.Body.String())
	}
	var resolvedResponse reconciliationConflictResponse
	decodeResponse(t, resolvedRecorder, &resolvedResponse)
	if resolvedResponse.State != "resolved" || resolvedResponse.Resolution == nil {
		t.Fatalf("resolved conflict response = %#v, want derived resolved state and resolution", resolvedResponse)
	}
	if resolvedResponse.Resolution.ID != string(resolved.Resolution.ID) ||
		resolvedResponse.Resolution.FromVersion != string(descendant.Version.ID) ||
		resolvedResponse.Resolution.ToVersion != string(resolved.Version.ID) ||
		resolvedResponse.Resolution.BaseIntent != string(repository.CurrentIntent().ID) ||
		resolvedResponse.Resolution.ResolvedBy != "judgement-agent" ||
		resolvedResponse.Resolution.Rationale != "replayed C onto accepted B prime" {
		t.Fatalf("resolution response = %#v, want %#v", resolvedResponse.Resolution, resolved.Resolution)
	}
	resolvedConflictRetry := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/reconciliation-conflicts", bytes.NewReader(body))
	resolvedConflictRetry.SetPathValue("repoID", "repo_123")
	resolvedConflictRetry.Header.Set("Content-Type", "application/json")
	resolvedConflictRetry.Header.Set("Idempotency-Key", "conflict-b-c")
	resolvedConflictRetryRecorder := httptest.NewRecorder()
	handlers.RecordReconciliationConflict.ServeHTTP(resolvedConflictRetryRecorder, intentapi.WithAuthenticatedProducer(resolvedConflictRetry, "ion"))
	if resolvedConflictRetryRecorder.Code != http.StatusOK {
		t.Fatalf("resolved conflict retry status = %d, want 200: %s", resolvedConflictRetryRecorder.Code, resolvedConflictRetryRecorder.Body.String())
	}
	var resolvedConflictRetried reconciliationConflictResponse
	decodeResponse(t, resolvedConflictRetryRecorder, &resolvedConflictRetried)
	if resolvedConflictRetried.State != "resolved" || resolvedConflictRetried.Resolution == nil ||
		resolvedConflictRetried.Resolution.ID != string(resolved.Resolution.ID) {
		t.Fatalf("resolved conflict retry = %#v, want same derived resolution", resolvedConflictRetried)
	}
}

type reconciliationConflictResponse struct {
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
	Resolution    *struct {
		ID          string `json:"id"`
		FromVersion string `json:"fromVersion"`
		ToVersion   string `json:"toVersion"`
		BaseIntent  string `json:"baseIntent"`
		ResolvedBy  string `json:"resolvedBy"`
		Rationale   string `json:"rationale"`
	} `json:"resolution"`
}

type reconciliationConflictPageResponse struct {
	Conflicts  []reconciliationConflictResponse `json:"conflicts"`
	NextCursor string                           `json:"nextCursor"`
}

func reconciliationConflictResponsesEqual(left, right reconciliationConflictResponse) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
