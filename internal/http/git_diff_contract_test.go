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

	const tagMessage = "tag metadata should not be in a patch response"
	requireGitSuccess(t, "tag head", "-C", worktree, "tag", "-a", "v1", "-m", tagMessage)
	tagObject := gitRevParse(t, worktree, "v1^{tag}")
	requireGitSuccess(t, "push tag", "-C", worktree, "push", "origin", "refs/tags/v1")

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

	t.Run("read token gets patch only when show target is an annotated tag object", func(t *testing.T) {
		status, body := getGitDiff(t, fixture, "show/"+tagObject+".diff", readToken.Token)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200\n%s", status, body)
		}
		assertUnifiedDiff(t, body)
		for _, forbidden := range []string{"tag v1", "Tagger:", tagMessage, "update readme"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("show diff leaked %q through annotated tag object:\n%s", forbidden, body)
			}
		}
	})

	requireGitSuccess(t, "create feature branch", "-C", worktree, "checkout", "-b", "feature", base)
	writeGitFile(t, worktree, "feature.txt", "feature branch\n")
	requireGitSuccess(t, "stage feature", "-C", worktree, "add", "feature.txt")
	requireGitSuccess(t, "commit feature", "-C", worktree, "commit", "-m", "feature branch")
	requireGitSuccess(t, "return to main", "-C", worktree, "checkout", "main")
	requireGitSuccess(t, "merge feature", "-C", worktree, "merge", "--no-ff", "feature", "-m", "merge feature")
	requireGitSuccess(t, "push merge", "-C", worktree, "push", "origin", "main")
	mergeCommit := gitRevParse(t, worktree, "HEAD")

	t.Run("read token gets a first parent patch for a merge commit via show", func(t *testing.T) {
		status, body := getGitDiff(t, fixture, "show/"+mergeCommit+".diff", readToken.Token)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200\n%s", status, body)
		}
		if !strings.Contains(body, "diff --git a/feature.txt b/feature.txt") || !strings.Contains(body, "+feature branch") {
			t.Fatalf("merge show diff missing first parent patch:\n%s", body)
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

	requireGitSuccess(t, "create unrelated orphan branch", "-C", worktree, "checkout", "--orphan", "unrelated")
	requireGitSuccess(t, "remove orphan branch files", "-C", worktree, "rm", "-rf", ".")
	writeGitFile(t, worktree, "unrelated.txt", "unrelated history\n")
	requireGitSuccess(t, "stage unrelated", "-C", worktree, "add", "unrelated.txt")
	requireGitSuccess(t, "commit unrelated", "-C", worktree, "commit", "-m", "unrelated history")
	requireGitSuccess(t, "push unrelated", "-C", worktree, "push", "origin", "unrelated")
	unrelated := gitRevParse(t, worktree, "HEAD")

	t.Run("triple dot compare without a merge base is a caller conflict", func(t *testing.T) {
		status, body := getGitDiff(t, fixture, "compare/"+base+"..."+unrelated+".diff", readToken.Token)
		if status != http.StatusConflict {
			t.Fatalf("status = %d, want 409\n%s", status, body)
		}
		if !strings.Contains(body, "diff revisions do not share a merge base") {
			t.Fatalf("body = %q, want no merge base message", body)
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
