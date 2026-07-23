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
		"descendantVersion":"` + string(descendant.Version.ID) + `"
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

	retry := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/reconciliation-conflicts", bytes.NewReader(body))
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
	ReportedBy    string   `json:"reportedBy"`
	AffectedPaths []string `json:"affectedPaths"`
}

func reconciliationConflictResponsesEqual(left, right reconciliationConflictResponse) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
