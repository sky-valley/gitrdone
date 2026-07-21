package httpapi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	httpapi "github.com/sky-valley/gitrdone/internal/http"
)

func TestGitLFSRealGitCommands(t *testing.T) {
	requireGitLFS(t)

	fixture := newGitSmartHTTPFixture(t, "lfs")
	readwriteToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "readwrite", "lfs-writer-job")
	readToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "read", "lfs-reader-job")

	payload := "large artifact bytes stored through git lfs\n"
	worktree := newGitLFSWorktree(t, payload)
	requireGitSuccess(t, "add lfs remote", "-C", worktree, "remote", "add", "origin", fixture.tokenizedGitURL(readwriteToken.Token))
	requireGitSuccess(t, "publish lfs repo", "-C", worktree, "push", "origin", "HEAD:refs/candidates/bootstrap")
	bootstrapIntentFixture(t, fixture, gitRevParse(t, worktree, "HEAD"))

	cloneDir := filepath.Join(t.TempDir(), "clone")
	requireGitSuccessWithEnv(t, "clone lfs repo without smudge", []string{"GIT_LFS_SKIP_SMUDGE=1"}, "clone", "--branch", "main", fixture.tokenizedGitURL(readToken.Token), cloneDir)
	requireGitSuccess(t, "install lfs filters in clone", "-C", cloneDir, "lfs", "install", "--local")
	requireGitSuccess(t, "pull lfs objects", "-C", cloneDir, "lfs", "pull")
	assertFileContent(t, filepath.Join(cloneDir, "artifact.bin"), payload)
}

func TestGitLFSHTTPContract(t *testing.T) {
	t.Run("batch upload requires write scope", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "lfs-upload-scope")
		readToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "read", "lfs-reader-job")
		payload := "upload scope payload"
		body := fmt.Sprintf(`{"operation":"upload","objects":[{"oid":%q,"size":%d}]}`, lfsOID(payload), len(payload))

		res, responseBody := request(t, fixture.handler, http.MethodPost, lfsBatchPath(fixture.repo.ID), bearer(readToken.Token), "application/vnd.git-lfs+json", body)

		requireStatus(t, res, responseBody, http.StatusForbidden)
		requireLFSContentType(t, res)
	})

	t.Run("batch download requires read scope", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "lfs-download-scope")
		writeToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "write", "lfs-writer-job")
		payload := "download scope payload"
		body := fmt.Sprintf(`{"operation":"download","objects":[{"oid":%q,"size":%d}]}`, lfsOID(payload), len(payload))

		res, responseBody := request(t, fixture.handler, http.MethodPost, lfsBatchPath(fixture.repo.ID), bearer(writeToken.Token), "application/vnd.git-lfs+json", body)

		requireStatus(t, res, responseBody, http.StatusForbidden)
		requireLFSContentType(t, res)
	})

	t.Run("upload verifies oid and size", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "lfs-verify")
		writeToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "write", "lfs-writer-job")

		res, responseBody := request(t, fixture.handler, http.MethodPut, lfsObjectPath(fixture.repo.ID, lfsOID("expected")), bearer(writeToken.Token), "application/octet-stream", "different")

		requireStatus(t, res, responseBody, http.StatusUnprocessableEntity)
		requireLFSContentType(t, res)
	})

	t.Run("direct upload and download use repo token scopes", func(t *testing.T) {
		fixture := newGitSmartHTTPFixture(t, "lfs-direct")
		writeToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "write", "lfs-writer-job")
		readToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "read", "lfs-reader-job")
		payload := "direct lfs payload\n"
		objectPath := lfsObjectPath(fixture.repo.ID, lfsOID(payload))

		res, responseBody := request(t, fixture.handler, http.MethodPut, objectPath, bearer(writeToken.Token), "application/octet-stream", payload)
		requireStatus(t, res, responseBody, http.StatusOK)

		res, responseBody = request(t, fixture.handler, http.MethodGet, objectPath, bearer(readToken.Token), "", "")
		requireStatus(t, res, responseBody, http.StatusOK)
		if got := string(responseBody); got != payload {
			t.Fatalf("download body = %q, want %q", got, payload)
		}
	})

	t.Run("upload rejects objects larger than configured limit", func(t *testing.T) {
		handler := httpapi.NewServer(httpapi.Config{
			BaseURL:           "https://git.example.com",
			ControlBearer:     "internal-admin-token",
			StorageRoot:       t.TempDir(),
			MaxLFSObjectBytes: 3,
		})
		repo := createRepoFixture(t, handler, "lfs-too-large")
		writeToken := createRepoTokenFixture(t, handler, repo.ID, "write", "lfs-writer-job")
		payload := "four"

		res, responseBody := request(t, handler, http.MethodPut, lfsObjectPath(repo.ID, lfsOID(payload)), bearer(writeToken.Token), "application/octet-stream", payload)

		requireStatus(t, res, responseBody, http.StatusRequestEntityTooLarge)
		requireLFSContentType(t, res)
	})

	t.Run("upload rejects bodies larger than declared content length during copy", func(t *testing.T) {
		handler := httpapi.NewServer(httpapi.Config{
			BaseURL:           "https://git.example.com",
			ControlBearer:     "internal-admin-token",
			StorageRoot:       t.TempDir(),
			MaxLFSObjectBytes: 3,
		})
		repo := createRepoFixture(t, handler, "lfs-copy-limit")
		writeToken := createRepoTokenFixture(t, handler, repo.ID, "write", "lfs-writer-job")
		req := httptest.NewRequest(http.MethodPut, lfsObjectPath(repo.ID, lfsOID("four")), strings.NewReader("four"))
		req.ContentLength = 3
		req.Header.Set("Authorization", bearer(writeToken.Token))
		req.Header.Set("Content-Type", "application/octet-stream")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		res := rec.Result()
		t.Cleanup(func() {
			res.Body.Close()
		})
		responseBody, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}

		requireStatus(t, res, responseBody, http.StatusRequestEntityTooLarge)
		requireLFSContentType(t, res)
	})
}

func requireGitLFS(t *testing.T) {
	t.Helper()

	if strings.TrimSpace(os.Getenv("GITRDONE_SKIP_GIT_LFS_CONTRACT_TEST")) != "" {
		t.Skip("GITRDONE_SKIP_GIT_LFS_CONTRACT_TEST is set")
	}
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Fatalf("git-lfs is not installed; install git-lfs or set GITRDONE_SKIP_GIT_LFS_CONTRACT_TEST=1 to skip this contract test")
	}
	output, err := runGitForTest("lfs", "version")
	if err != nil {
		t.Fatalf("git-lfs is not available through git: %s", strings.TrimSpace(output))
	}
}

func newGitLFSWorktree(t *testing.T, payload string) string {
	t.Helper()

	worktree := t.TempDir()
	requireGitSuccess(t, "init lfs worktree", "-C", worktree, "init")
	requireGitSuccess(t, "create main branch", "-C", worktree, "checkout", "-b", "main")
	requireGitSuccess(t, "configure test email", "-C", worktree, "config", "user.email", "gitrdone-tests@example.com")
	requireGitSuccess(t, "configure test name", "-C", worktree, "config", "user.name", "gitrdone Tests")
	requireGitSuccess(t, "install lfs filters", "-C", worktree, "lfs", "install", "--local")
	requireGitSuccess(t, "track lfs files", "-C", worktree, "lfs", "track", "*.bin")
	writeGitFile(t, worktree, "artifact.bin", payload)
	requireGitSuccess(t, "add lfs files", "-C", worktree, "add", ".gitattributes", "artifact.bin")
	requireGitSuccess(t, "commit lfs file", "-C", worktree, "commit", "-m", "add lfs artifact")
	return worktree
}

func lfsBatchPath(repoID string) string {
	return "/git/repos/" + repoID + ".git/info/lfs/objects/batch"
}

func lfsObjectPath(repoID string, oid string) string {
	return "/git/repos/" + repoID + ".git/info/lfs/objects/" + oid
}

func lfsOID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func requireLFSContentType(t *testing.T, res *http.Response) {
	t.Helper()
	if got := res.Header.Get("Content-Type"); got != "application/vnd.git-lfs+json" {
		t.Fatalf("Content-Type = %q, want application/vnd.git-lfs+json", got)
	}
}

func requireGitSuccessWithEnv(t *testing.T, description string, env []string, args ...string) {
	t.Helper()

	output, err := runGitForTestWithEnv(env, args...)
	if err != nil {
		t.Fatalf("%s failed: git %s\n%s", description, strings.Join(args, " "), output)
	}
}

func runGitForTestWithEnv(env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
