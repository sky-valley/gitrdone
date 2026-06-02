package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	httpapi "skyvalley.ac/m/v2/internal/http"
)

func TestGitSmartHTTPPreflightContract(t *testing.T) {
	t.Run("authorized read reaches the real backend boundary", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-read")
		token := createRepoTokenFixture(t, server, repo.ID, "read", "differ-reader-job")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/info/refs?service=git-upload-pack", bearer(token.Token), "", "")

		requireStatus(t, res, body, http.StatusNotImplemented)
		if !strings.Contains(string(body), "git http backend is not implemented") {
			t.Fatalf("body = %q, want backend not implemented message", string(body))
		}
	})

	t.Run("authorized write reaches the real backend boundary", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-write")
		token := createRepoTokenFixture(t, server, repo.ID, "write", "differ-writer-job")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/info/refs?service=git-receive-pack", bearer(token.Token), "", "")

		requireStatus(t, res, body, http.StatusNotImplemented)
		if !strings.Contains(string(body), "git http backend is not implemented") {
			t.Fatalf("body = %q, want backend not implemented message", string(body))
		}
	})

	t.Run("invalid repo id is rejected before auth", func(t *testing.T) {
		server := newGitPreflightServer(t)

		res, body := request(t, server, http.MethodGet, "/git/repos/not-a-repo.git/info/refs?service=git-upload-pack", "", "", "")

		requireStatus(t, res, body, http.StatusBadRequest)
	})

	t.Run("invalid git path is rejected before auth", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-path")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/objects/00/abc", "", "", "")

		requireStatus(t, res, body, http.StatusBadRequest)
	})

	t.Run("invalid discovery service is rejected before auth", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-service")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/info/refs?service=git-archive-pack", "", "", "")

		requireStatus(t, res, body, http.StatusBadRequest)
	})

	t.Run("missing token cannot read", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-missing-token")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/info/refs?service=git-upload-pack", "", "", "")

		requireStatus(t, res, body, http.StatusUnauthorized)
	})

	t.Run("unknown token cannot read", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-unknown-token")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/info/refs?service=git-upload-pack", bearer("gtd_unknown"), "", "")

		requireStatus(t, res, body, http.StatusUnauthorized)
	})

	t.Run("token for another repo cannot read", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-one")
		otherRepo := createRepoFixture(t, server, "git-preflight-two")
		token := createRepoTokenFixture(t, server, repo.ID, "read", "differ-reader-job")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+otherRepo.ID+".git/info/refs?service=git-upload-pack", bearer(token.Token), "", "")

		requireStatus(t, res, body, http.StatusUnauthorized)
	})

	t.Run("read token cannot write", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-read-only")
		token := createRepoTokenFixture(t, server, repo.ID, "read", "differ-reader-job")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/info/refs?service=git-receive-pack", bearer(token.Token), "", "")

		requireStatus(t, res, body, http.StatusForbidden)
	})

	t.Run("write token cannot read", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-write-only")
		token := createRepoTokenFixture(t, server, repo.ID, "write", "differ-writer-job")

		res, body := request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/info/refs?service=git-upload-pack", bearer(token.Token), "", "")

		requireStatus(t, res, body, http.StatusForbidden)
	})

	t.Run("archived repo rejects git access", func(t *testing.T) {
		server := newGitPreflightServer(t)
		repo := createRepoFixture(t, server, "git-preflight-archived")
		token := createRepoTokenFixture(t, server, repo.ID, "read", "differ-reader-job")
		res, body := request(t, server, http.MethodPost, "/v1/repos/"+repo.ID+"/archive", controlAuthorization, "", "")
		requireStatus(t, res, body, http.StatusOK)

		res, body = request(t, server, http.MethodGet, "/git/repos/"+repo.ID+".git/info/refs?service=git-upload-pack", bearer(token.Token), "", "")

		requireStatus(t, res, body, http.StatusGone)
	})

	t.Run("unknown repo id is not found", func(t *testing.T) {
		server := newGitPreflightServer(t)

		res, body := request(t, server, http.MethodGet, "/git/repos/repo_00000000-0000-4000-8000-000000000000.git/info/refs?service=git-upload-pack", bearer("gtd_unknown"), "", "")

		requireStatus(t, res, body, http.StatusNotFound)
	})
}

func newGitPreflightServer(t *testing.T) http.Handler {
	t.Helper()
	return httpapi.NewServer(httpapi.Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
		StorageRoot:   t.TempDir(),
	})
}

func bearer(token string) string {
	return "Bearer " + token
}
