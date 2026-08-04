package grdclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewTransportListsPagesAndRecordsResponse(t *testing.T) {
	var postBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "reviewer" || password != "secret" {
			t.Errorf("basic auth = %q, %q, %t", username, password, ok)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/repo_cove/reviews" && r.URL.Query().Get("cursor") == "":
			_, _ = w.Write([]byte(`{"reviews":[{"version":"version_a","concern":"architecture-data-infrastructure","reviewer":"noam@skyvalley.ac","reason":"database added","evidence":["store.go"]}],"nextCursor":"next"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/repos/repo_cove/reviews" && r.URL.Query().Get("cursor") == "next":
			_, _ = w.Write([]byte(`{"reviews":[{"version":"version_b","concern":"design-user-experience","reviewer":"noam@skyvalley.ac","reason":"navigation changed","evidence":["nav.go"]}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/repos/repo_cove/review-responses":
			if got := r.Header.Get("Idempotency-Key"); got != "review-attempt-1" {
				t.Errorf("Idempotency-Key = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&postBody); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"id":"review_response_1","version":"version_a","concern":"architecture-data-infrastructure","reviewer":"noam@skyvalley.ac","decision":"approved","rationale":"migration reviewed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{HTTPClient: server.Client()}
	remote := remote{baseURL: server.URL, repoID: "repo_cove", username: "reviewer", password: "secret"}
	reviews, err := client.listReviews(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 || reviews[0].Version != "version_a" || reviews[1].Version != "version_b" {
		t.Fatalf("reviews = %#v", reviews)
	}
	response, err := client.respondReview(context.Background(), remote, reviews[0], "approved", "migration reviewed", "review-attempt-1")
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "review_response_1" || postBody["version"] != "version_a" || postBody["decision"] != "approved" {
		t.Fatalf("response = %#v, body = %#v", response, postBody)
	}
}

func TestReviewHandleIsShortStableAndConcernSpecific(t *testing.T) {
	first := reviewObligation{Version: "version_a", Concern: "architecture-data-infrastructure"}
	if got := reviewHandle(first); len(got) != 10 {
		t.Fatalf("handle = %q, want 10 characters", got)
	}
	if reviewHandle(first) != reviewHandle(first) {
		t.Fatal("review handle is unstable")
	}
	second := first
	second.Concern = "design-user-experience"
	if reviewHandle(first) == reviewHandle(second) {
		t.Fatal("different concerns share a handle")
	}
	if strings.Contains(reviewHandle(first), "version") {
		t.Fatalf("handle exposes machinery: %q", reviewHandle(first))
	}
}

func TestReviewResponseAttemptRetriesOnlyTheUnconfirmedOperation(t *testing.T) {
	ctx := context.Background()
	workdir := filepath.Join(t.TempDir(), "workspace")
	if output, err := exec.Command("git", "init", workdir).CombinedOutput(); err != nil {
		t.Fatalf("init Git repository: %v: %s", err, output)
	}
	review := reviewObligation{Version: "version_a", Concern: "architecture-data-infrastructure"}

	first, err := prepareReviewResponseAttempt(ctx, workdir, "repo_cove", review, "approved", "looks good")
	if err != nil {
		t.Fatal(err)
	}
	retried, err := prepareReviewResponseAttempt(ctx, workdir, "repo_cove", review, "approved", "looks good")
	if err != nil {
		t.Fatal(err)
	}
	if retried.IdempotencyKey != first.IdempotencyKey {
		t.Fatalf("unconfirmed retry key = %q, want %q", retried.IdempotencyKey, first.IdempotencyKey)
	}
	if err := forgetReviewResponseAttempt(ctx, workdir, "repo_cove", reviewHandle(review)); err != nil {
		t.Fatal(err)
	}

	changesRequested, err := prepareReviewResponseAttempt(ctx, workdir, "repo_cove", review, "changes_requested", "found an issue")
	if err != nil {
		t.Fatal(err)
	}
	if err := forgetReviewResponseAttempt(ctx, workdir, "repo_cove", reviewHandle(review)); err != nil {
		t.Fatal(err)
	}
	secondApproval, err := prepareReviewResponseAttempt(ctx, workdir, "repo_cove", review, "approved", "looks good")
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotencyKey == changesRequested.IdempotencyKey || first.IdempotencyKey == secondApproval.IdempotencyKey {
		t.Fatalf("confirmed actions reused keys: first=%q changes=%q second=%q", first.IdempotencyKey, changesRequested.IdempotencyKey, secondApproval.IdempotencyKey)
	}
}

func TestRespondReviewUsesANewOperationAfterConfirmedInterveningResponse(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"reviews":[{"version":"version_a","concern":"architecture-data-infrastructure","reviewer":"noam@skyvalley.ac","reason":"database added","evidence":["store.go"]}]}`))
		case http.MethodPost:
			keys = append(keys, r.Header.Get("Idempotency-Key"))
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			response := map[string]string{
				"id": "response", "version": body["version"], "concern": body["concern"],
				"reviewer": "noam@skyvalley.ac", "decision": body["decision"], "rationale": body["rationale"],
			}
			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	workdir := filepath.Join(t.TempDir(), "workspace")
	if output, err := exec.Command("git", "init", workdir).CombinedOutput(); err != nil {
		t.Fatalf("init Git repository: %v: %s", err, output)
	}
	remoteURL := strings.Replace(server.URL, "://", "://reviewer:secret@", 1) + "/git/repos/repo_cove.git"
	if err := gitRun(context.Background(), workdir, "remote", "add", "origin", remoteURL); err != nil {
		t.Fatal(err)
	}
	client := Client{HTTPClient: server.Client(), Stdout: &bytes.Buffer{}}
	handle := reviewHandle(reviewObligation{Version: "version_a", Concern: "architecture-data-infrastructure"})
	for _, response := range []struct {
		decision  string
		rationale string
	}{
		{decision: "approved", rationale: "looks good"},
		{decision: "changes_requested", rationale: "found an issue"},
		{decision: "approved", rationale: "looks good"},
	} {
		if err := client.RespondReview(context.Background(), workdir, handle, response.decision, response.rationale); err != nil {
			t.Fatal(err)
		}
	}
	if len(keys) != 3 || keys[0] == keys[1] || keys[0] == keys[2] || keys[1] == keys[2] {
		t.Fatalf("review response operation keys = %#v, want three distinct confirmed actions", keys)
	}
}
