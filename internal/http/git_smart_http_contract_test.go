package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	httpapi "skyvalley.ac/m/v2/internal/http"
)

func TestGitSmartHTTPRealGitCommands(t *testing.T) {
	t.Run("readwrite token can push and read token can clone and fetch", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "readwrite")
		readwriteToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "readwrite", "differ-bootstrap-job")
		readToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "read", "differ-reader-job")

		worktree := newGitWorktree(t, "README.md", "first version\n")
		pushURL := fixture.tokenizedGitURL(readwriteToken.Token)
		requireGitSuccess(t, "push initial commit", "-C", worktree, "remote", "add", "origin", pushURL)
		requireGitSuccess(t, "push initial commit", "-C", worktree, "push", "-u", "origin", "main")

		cloneDir := filepath.Join(t.TempDir(), "clone")
		requireGitSuccess(t, "clone with read token", "clone", "--branch", "main", fixture.tokenizedGitURL(readToken.Token), cloneDir)
		assertFileContent(t, filepath.Join(cloneDir, "README.md"), "first version\n")

		writeGitFile(t, worktree, "README.md", "second version\n")
		requireGitSuccess(t, "commit update", "-C", worktree, "add", "README.md")
		requireGitSuccess(t, "commit update", "-C", worktree, "commit", "-m", "update readme")
		requireGitSuccess(t, "push update", "-C", worktree, "push", "origin", "main")

		requireGitSuccess(t, "fetch with read token", "-C", cloneDir, "pull", "--ff-only", "origin", "main")
		assertFileContent(t, filepath.Join(cloneDir, "README.md"), "second version\n")
	})

	t.Run("write token can push without read scope", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "write")
		writeToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "write", "differ-writer-job")

		worktree := newGitWorktree(t, "write.txt", "write scope\n")
		requireGitSuccess(t, "push with write token", "-C", worktree, "remote", "add", "origin", fixture.tokenizedGitURL(writeToken.Token))
		requireGitSuccess(t, "push with write token", "-C", worktree, "push", "-u", "origin", "main")
	})

	t.Run("read token cannot push", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "read-denied")
		readToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "read", "differ-reader-job")

		worktree := newGitWorktree(t, "denied.txt", "read-only\n")
		requireGitSuccess(t, "add remote with read token", "-C", worktree, "remote", "add", "origin", fixture.tokenizedGitURL(readToken.Token))
		requireGitFailure(t, "push with read token", "-C", worktree, "push", "-u", "origin", "main")
	})

	t.Run("write token cannot clone", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "write-denied")
		writeToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "write", "differ-writer-job")

		requireGitFailure(t, "clone with write token", "clone", fixture.tokenizedGitURL(writeToken.Token), filepath.Join(t.TempDir(), "clone"))
	})

	t.Run("missing token cannot clone", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "missing-token")

		requireGitFailure(t, "clone without token", "clone", fixture.gitURL(), filepath.Join(t.TempDir(), "clone"))
	})

	t.Run("token for one repo cannot read another repo", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "wrong-repo")
		otherRepo := createRepoFixture(t, fixture.handler, "wrong-repo-other")
		readToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "read", "differ-reader-job")

		requireGitFailure(t, "clone with token from another repo", "clone", fixture.tokenizedGitURLForRepo(otherRepo.ID, readToken.Token), filepath.Join(t.TempDir(), "clone"))
	})
}

type gitSmartHTTPFixture struct {
	handler http.Handler
	server  *httptest.Server
	repo    createdRepoFixture
}

type repoTokenFixture struct {
	Token string `json:"token"`
}

func newGitSmartHTTPFixture(t *testing.T, suffix string) gitSmartHTTPFixture {
	t.Helper()

	handler := httpapi.NewServer(httpapi.Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
		StorageRoot:   t.TempDir(),
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return gitSmartHTTPFixture{
		handler: handler,
		server:  server,
		repo:    createRepoFixture(t, handler, "git-"+suffix),
	}
}

func createRepoTokenFixture(t *testing.T, server http.Handler, repoID string, scope string, subject string) repoTokenFixture {
	t.Helper()

	body := fmt.Sprintf(
		`{"scope":%q,"ttlSeconds":3600,"subject":%q}`,
		scope,
		subject,
	)
	res, responseBody := request(t, server, http.MethodPost, "/v1/repos/"+repoID+"/tokens", controlAuthorization, "application/json", body)
	requireStatus(t, res, responseBody, http.StatusCreated)

	var token repoTokenFixture
	decodeJSON(t, res, responseBody, &token)
	if token.Token == "" {
		t.Fatal("repo token is empty")
	}
	return token
}

func (fixture gitSmartHTTPFixture) gitURL() string {
	return fixture.gitURLForRepo(fixture.repo.ID)
}

func (fixture gitSmartHTTPFixture) gitURLForRepo(repoID string) string {
	return strings.TrimRight(fixture.server.URL, "/") + "/git/repos/" + repoID + ".git"
}

func (fixture gitSmartHTTPFixture) tokenizedGitURL(token string) string {
	return fixture.tokenizedGitURLForRepo(fixture.repo.ID, token)
}

func (fixture gitSmartHTTPFixture) tokenizedGitURLForRepo(repoID string, token string) string {
	parsed, err := url.Parse(fixture.gitURLForRepo(repoID))
	if err != nil {
		panic(err)
	}
	parsed.User = url.UserPassword("x-access-token", token)
	return parsed.String()
}

func newGitWorktree(t *testing.T, filename string, content string) string {
	t.Helper()

	worktree := t.TempDir()
	requireGitSuccess(t, "init worktree", "-C", worktree, "init")
	requireGitSuccess(t, "create main branch", "-C", worktree, "checkout", "-b", "main")
	requireGitSuccess(t, "configure test email", "-C", worktree, "config", "user.email", "gitrdone-tests@example.com")
	requireGitSuccess(t, "configure test name", "-C", worktree, "config", "user.name", "Giterdone Tests")
	writeGitFile(t, worktree, filename, content)
	requireGitSuccess(t, "commit initial file", "-C", worktree, "add", filename)
	requireGitSuccess(t, "commit initial file", "-C", worktree, "commit", "-m", "initial commit")
	return worktree
}

func writeGitFile(t *testing.T, worktree string, filename string, content string) {
	t.Helper()

	path := filepath.Join(worktree, filename)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent directory for %s: %v", filename, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}

func requireGitSuccess(t *testing.T, description string, args ...string) {
	t.Helper()

	output, err := runGitForTest(args...)
	if err != nil {
		t.Fatalf("%s failed: git %s\n%s", description, strings.Join(args, " "), output)
	}
}

func requireGitFailure(t *testing.T, description string, args ...string) {
	t.Helper()

	output, err := runGitForTest(args...)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded: git %s\n%s", description, strings.Join(args, " "), output)
	}
}

func runGitForTest(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	return string(output), err
}
