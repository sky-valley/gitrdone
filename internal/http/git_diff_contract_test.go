package httpapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitDiffRealGitCommands(t *testing.T) {
	fixture := newGitSmartHTTPFixture(t, "diff")
	readwriteToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "readwrite", "differ-bootstrap-job")
	readToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "read", "differ-reader-job")
	writeToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "write", "differ-writer-job")

	// Push a two-commit history: base = "first version", head = "second version".
	worktree := newGitWorktree(t, "README.md", "first version\n")
	pushURL := fixture.tokenizedGitURL(readwriteToken.Token)
	requireGitSuccess(t, "add origin", "-C", worktree, "remote", "add", "origin", pushURL)
	requireGitSuccess(t, "push base", "-C", worktree, "push", "-u", "origin", "main")
	base := gitRevParse(t, worktree, "HEAD")

	writeGitFile(t, worktree, "README.md", "second version\n")
	requireGitSuccess(t, "stage update", "-C", worktree, "add", "README.md")
	requireGitSuccess(t, "commit update", "-C", worktree, "commit", "-m", "update readme")
	requireGitSuccess(t, "push head", "-C", worktree, "push", "origin", "main")
	head := gitRevParse(t, worktree, "HEAD")

	t.Run("read token gets a single commit diff via show", func(t *testing.T) {
		status, body := getGitDiff(t, fixture, "show/"+head+".diff", readToken.Token)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200\n%s", status, body)
		}
		assertUnifiedDiff(t, body)
		if !strings.Contains(body, "second version") {
			t.Fatalf("diff missing the changed line:\n%s", body)
		}
		if strings.Contains(body, "update readme") {
			t.Fatalf("show diff leaked the commit message (want patch only):\n%s", body)
		}
	})

	t.Run("read token gets an endpoint diff via compare", func(t *testing.T) {
		status, body := getGitDiff(t, fixture, "compare/"+base+".."+head+".diff", readToken.Token)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200\n%s", status, body)
		}
		assertUnifiedDiff(t, body)
		if !strings.Contains(body, "-first version") || !strings.Contains(body, "+second version") {
			t.Fatalf("compare diff missing the expected hunk:\n%s", body)
		}
	})

	t.Run("unknown sha is a not found", func(t *testing.T) {
		absent := "0000000000000000000000000000000000000000"
		status, _ := getGitDiff(t, fixture, "show/"+absent+".diff", readToken.Token)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
	})

	t.Run("write only token cannot read a diff", func(t *testing.T) {
		status, _ := getGitDiff(t, fixture, "show/"+head+".diff", writeToken.Token)
		if status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", status)
		}
	})

	t.Run("missing token cannot read a diff", func(t *testing.T) {
		status, _ := getGitDiff(t, fixture, "show/"+head+".diff", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", status)
		}
	})
}

func getGitDiff(t *testing.T, fixture gitSmartHTTPFixture, path string, token string) (int, string) {
	t.Helper()

	url := strings.TrimRight(fixture.server.URL, "/") + "/git/repos/" + fixture.repo.ID + ".git/" + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build diff request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("perform diff request: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read diff body: %v", err)
	}
	return res.StatusCode, string(body)
}

func assertUnifiedDiff(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, "diff --git a/README.md b/README.md") {
		t.Fatalf("body is not a unified diff:\n%s", body)
	}
}

func gitRevParse(t *testing.T, worktree string, rev string) string {
	t.Helper()
	output, err := runGitForTest("-C", worktree, "rev-parse", rev)
	if err != nil {
		t.Fatalf("rev-parse %s failed: %v\n%s", rev, err, output)
	}
	return strings.TrimSpace(output)
}
