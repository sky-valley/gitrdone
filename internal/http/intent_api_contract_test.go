package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestNativeIntentAPIUsesRealGitProjection(t *testing.T) {
	fixture := newGitSmartHTTPFixture(t, "intent-api")
	token := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "readwrite", "ion")
	worktree := newGitWorktree(t, "README.md", "initial\n")
	remote := fixture.tokenizedGitURL(token.Token)
	requireGitSuccess(t, "add intent remote", "-C", worktree, "remote", "add", "origin", remote)
	requireGitSuccess(t, "publish initial content", "-C", worktree, "push", "origin", "HEAD:refs/candidates/bootstrap")
	initialCommit := gitRevParse(t, worktree, "HEAD")
	bootstrapBody := fmt.Sprintf(`{"contentRef":{"engine":"git","revision":%q}}`, initialCommit)
	res, body := request(t, fixture.handler, http.MethodPut, "/v1/repos/"+fixture.repo.ID+"/intent", controlAuthorization, "application/json", bootstrapBody)
	requireStatus(t, res, body, http.StatusOK)

	res, body = request(t, fixture.handler, http.MethodGet, "/v1/repos/"+fixture.repo.ID+"/intent", controlAuthorization, "", "")
	requireStatus(t, res, body, http.StatusOK)
	var initial struct {
		ID         string `json:"id"`
		ContentRef struct {
			Engine   string `json:"engine"`
			Revision string `json:"revision"`
		} `json:"contentRef"`
	}
	decodeJSON(t, res, body, &initial)
	if initial.ID == "" || initial.ContentRef.Engine != "git" || initial.ContentRef.Revision != initialCommit {
		t.Fatalf("initial intent = %#v, want git:%s", initial, initialCommit)
	}
	res, body = request(t, fixture.handler, http.MethodPut, "/v1/repos/"+fixture.repo.ID+"/intent", controlAuthorization, "application/json", bootstrapBody)
	requireStatus(t, res, body, http.StatusOK)
	var bootstrapRetry struct {
		ID string `json:"id"`
	}
	decodeJSON(t, res, body, &bootstrapRetry)
	if bootstrapRetry.ID != initial.ID {
		t.Fatalf("bootstrap retry intent = %q, want %q", bootstrapRetry.ID, initial.ID)
	}

	writeGitFile(t, worktree, "README.md", "proposed\n")
	requireGitSuccess(t, "stage proposed content", "-C", worktree, "add", "README.md")
	requireGitSuccess(t, "commit proposed content", "-C", worktree, "commit", "-m", "proposed intent")
	proposedCommit := gitRevParse(t, worktree, "HEAD")
	requireGitFailure(t, "push proposed content directly to main", "-C", worktree, "push", "origin", "HEAD:main")
	requireGitSuccess(t, "publish proposed object", "-C", worktree, "push", "origin", "HEAD:refs/candidates/api-test")
	secondBootstrapBody := fmt.Sprintf(`{"contentRef":{"engine":"git","revision":%q}}`, proposedCommit)
	res, body = request(t, fixture.handler, http.MethodPut, "/v1/repos/"+fixture.repo.ID+"/intent", controlAuthorization, "application/json", secondBootstrapBody)
	requireStatus(t, res, body, http.StatusConflict)
	if main := gitRemoteRef(t, remote, "refs/heads/main"); main != initialCommit {
		t.Fatalf("main after rejected bypasses = %q, want initial %q", main, initialCommit)
	}

	proposalBody := fmt.Sprintf(`{"baseIntent":%q,"contentRef":{"engine":"git","revision":%q}}`, initial.ID, proposedCommit)
	res, body = requestWithHeaders(t, fixture.handler, http.MethodPost, "/v1/repos/"+fixture.repo.ID+"/proposals", map[string]string{
		"Authorization":   controlAuthorization,
		"Content-Type":    "application/json",
		"Idempotency-Key": "intent-api-request-1",
	}, proposalBody)
	requireStatus(t, res, body, http.StatusOK)
	var receipt struct {
		Change struct {
			ID string `json:"id"`
		} `json:"change"`
		Version struct {
			ID       string `json:"id"`
			Producer string `json:"producer"`
		} `json:"version"`
		State     string `json:"state"`
		Promotion *struct {
			ID string `json:"id"`
		} `json:"promotion"`
	}
	decodeJSON(t, res, body, &receipt)
	if receipt.Change.ID == "" || receipt.Version.ID == "" || receipt.Version.Producer != "control-api" {
		t.Fatalf("proposal receipt identities = %#v", receipt)
	}
	if receipt.State != "admitted" || receipt.Promotion == nil || receipt.Promotion.ID == "" {
		t.Fatalf("proposal receipt outcome = %#v, want admitted with completed promotion", receipt)
	}

	remoteMain, err := runGitForTest("ls-remote", remote, "refs/heads/main")
	if err != nil {
		t.Fatalf("read promoted main: %v\n%s", err, remoteMain)
	}
	if fields := strings.Fields(remoteMain); len(fields) != 2 || fields[0] != proposedCommit {
		t.Fatalf("remote main = %q, want %s", remoteMain, proposedCommit)
	}

	res, body = request(t, fixture.handler, http.MethodGet, "/v1/repos/"+fixture.repo.ID+"/changes/"+receipt.Change.ID, controlAuthorization, "", "")
	requireStatus(t, res, body, http.StatusOK)
	var change struct {
		ID            string `json:"id"`
		LatestVersion struct {
			ID string `json:"id"`
		} `json:"latestVersion"`
		Promotion *struct {
			ID string `json:"id"`
		} `json:"promotion"`
	}
	decodeJSON(t, res, body, &change)
	if change.ID != receipt.Change.ID {
		t.Fatalf("change id = %q, want %q", change.ID, receipt.Change.ID)
	}
	if change.LatestVersion.ID != receipt.Version.ID || change.Promotion == nil || change.Promotion.ID != receipt.Promotion.ID {
		t.Fatalf("change summary = %#v, want latest version and promotion from receipt", change)
	}

	res, body = request(t, fixture.handler, http.MethodGet, "/v1/repos/"+fixture.repo.ID+"/changes/"+receipt.Change.ID+"/versions?limit=1", controlAuthorization, "", "")
	requireStatus(t, res, body, http.StatusOK)
	var versions struct {
		Versions []struct {
			ID string `json:"id"`
		} `json:"versions"`
		NextCursor json.RawMessage `json:"nextCursor"`
	}
	decodeJSON(t, res, body, &versions)
	if len(versions.Versions) != 1 || versions.Versions[0].ID != receipt.Version.ID {
		t.Fatalf("versions = %#v, want %q", versions.Versions, receipt.Version.ID)
	}
}
