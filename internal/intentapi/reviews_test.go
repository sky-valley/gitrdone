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
	"github.com/sky-valley/gitrdone/internal/requestauth"
)

func TestNativeReviewAPIListsAssignedWorkAndRecordsAuthenticatedApproval(t *testing.T) {
	repository, _ := newRepository(t)
	proposed, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "review-api-proposal",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion@skyvalley.ac",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.RecordConcernAssessment(context.Background(), intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:        "architecture-data-infrastructure",
			Prompt:         "Does this change alter architecture, data models, or infrastructure?",
			Reviewer:       "noam@skyvalley.ac",
			RequiresReview: true,
			Reason:         "the candidate adds a database",
			Evidence:       []string{"internal/store.go"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))

	list := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/reviews?limit=10", nil)
	list.SetPathValue("repoID", "repo_123")
	listRecorder := httptest.NewRecorder()
	handlers.ListReviews.ServeHTTP(listRecorder, requestauth.WithSubject(list, "noam@skyvalley.ac"))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listRecorder.Code, listRecorder.Body.String())
	}
	var listed struct {
		Reviews []struct {
			Version  string `json:"version"`
			Concern  string `json:"concern"`
			Reviewer string `json:"reviewer"`
			Reason   string `json:"reason"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Reviews) != 1 || listed.Reviews[0].Version != string(proposed.Version.ID) || listed.Reviews[0].Reviewer != "noam@skyvalley.ac" {
		t.Fatalf("listed reviews = %#v", listed)
	}

	body := []byte(`{"version":"` + string(proposed.Version.ID) + `","concern":"architecture-data-infrastructure","decision":"approved","rationale":"migration plan reviewed"}`)
	approve := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/review-responses", bytes.NewReader(body))
	approve.SetPathValue("repoID", "repo_123")
	approve.Header.Set("Content-Type", "application/json")
	approve.Header.Set("Idempotency-Key", "review-api-approval")
	approveRecorder := httptest.NewRecorder()
	handlers.RecordReviewResponse.ServeHTTP(approveRecorder, requestauth.WithSubject(approve, "noam@skyvalley.ac"))
	if approveRecorder.Code != http.StatusOK {
		t.Fatalf("approval status = %d: %s", approveRecorder.Code, approveRecorder.Body.String())
	}
	var response struct {
		ID        string `json:"id"`
		Version   string `json:"version"`
		Reviewer  string `json:"reviewer"`
		Decision  string `json:"decision"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal(approveRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID == "" || response.Version != string(proposed.Version.ID) || response.Reviewer != "noam@skyvalley.ac" || response.Decision != "approved" {
		t.Fatalf("approval response = %#v", response)
	}

	after := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/reviews?limit=10", nil)
	after.SetPathValue("repoID", "repo_123")
	afterRecorder := httptest.NewRecorder()
	handlers.ListReviews.ServeHTTP(afterRecorder, requestauth.WithSubject(after, "noam@skyvalley.ac"))
	if afterRecorder.Code != http.StatusOK {
		t.Fatalf("list after approval status = %d", afterRecorder.Code)
	}
	var empty struct {
		Reviews []json.RawMessage `json:"reviews"`
	}
	if err := json.Unmarshal(afterRecorder.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Reviews) != 0 {
		t.Fatalf("reviews after approval = %#v, want none", empty.Reviews)
	}
}

func TestNativeReviewAPIRejectsUnassignedReviewer(t *testing.T) {
	repository, _ := newRepository(t)
	proposed, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "wrong-reviewer-proposal",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion@skyvalley.ac",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.RecordConcernAssessment(context.Background(), intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:        "architecture-data-infrastructure",
			Prompt:         "Does this change alter architecture?",
			Reviewer:       "noam@skyvalley.ac",
			RequiresReview: true,
			Reason:         "architecture changed",
			Evidence:       []string{"architecture.go"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))
	body := []byte(`{"version":"` + string(proposed.Version.ID) + `","concern":"architecture-data-infrastructure","decision":"approved","rationale":"looks fine"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/review-responses", bytes.NewReader(body))
	request.SetPathValue("repoID", "repo_123")
	request.Header.Set("Idempotency-Key", "wrong-reviewer")
	recorder := httptest.NewRecorder()
	handlers.RecordReviewResponse.ServeHTTP(recorder, requestauth.WithSubject(request, "ion@skyvalley.ac"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
}
