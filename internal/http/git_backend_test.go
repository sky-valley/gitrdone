package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitSmartHTTPHandlerBackendBoundary(t *testing.T) {
	t.Run("authorized request is delegated to the backend with its access grant", func(t *testing.T) {
		access := &recordingGitAccessAuthorizer{
			grant: gitAccessGrant{
				RepoID:   "00000000-0000-4000-8000-000000000001",
				RepoPath: "/tmp/repos/00000000-0000-4000-8000-000000000001.git",
			},
		}
		backend := &recordingGitHTTPBackend{status: http.StatusNoContent}
		handler := gitSmartHTTPHandler(access, backend)

		req := httptest.NewRequest(http.MethodGet, "/git/repos/repo_00000000-0000-4000-8000-000000000001.git/info/refs?service=git-upload-pack", nil)
		req.SetPathValue("repoID", "repo_00000000-0000-4000-8000-000000000001")
		req.SetPathValue("gitPath", "info/refs")
		req.Header.Set("Authorization", "Bearer gtd_read_token")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
		}
		if access.input.RepoID != "00000000-0000-4000-8000-000000000001" {
			t.Fatalf("repoID = %q, want raw UUID", access.input.RepoID)
		}
		if access.input.Token != "gtd_read_token" {
			t.Fatalf("token = %q, want gtd_read_token", access.input.Token)
		}
		if access.input.Operation != gitOperationRead {
			t.Fatalf("operation = %q, want %q", access.input.Operation, gitOperationRead)
		}
		if !backend.called {
			t.Fatal("backend was not called")
		}
		if backend.grant != access.grant {
			t.Fatalf("backend grant = %#v, want %#v", backend.grant, access.grant)
		}
		if backend.request != req {
			t.Fatal("backend did not receive the original request")
		}
	})

	t.Run("authorization failure is not delegated to the backend", func(t *testing.T) {
		access := &recordingGitAccessAuthorizer{err: errRepoTokenForbidden}
		backend := &recordingGitHTTPBackend{status: http.StatusNoContent}
		handler := gitSmartHTTPHandler(access, backend)

		req := httptest.NewRequest(http.MethodGet, "/git/repos/repo_00000000-0000-4000-8000-000000000001.git/info/refs?service=git-upload-pack", nil)
		req.SetPathValue("repoID", "repo_00000000-0000-4000-8000-000000000001")
		req.SetPathValue("gitPath", "info/refs")
		req.Header.Set("Authorization", "Bearer gtd_read_token")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if backend.called {
			t.Fatal("backend was called despite authorization failure")
		}
	})

	t.Run("backend error becomes an internal server error", func(t *testing.T) {
		access := &recordingGitAccessAuthorizer{
			grant: gitAccessGrant{
				RepoID:   "00000000-0000-4000-8000-000000000001",
				RepoPath: "/tmp/repos/00000000-0000-4000-8000-000000000001.git",
			},
		}
		backend := &recordingGitHTTPBackend{err: errRecordingGitBackend}
		handler := gitSmartHTTPHandler(access, backend)

		req := httptest.NewRequest(http.MethodGet, "/git/repos/repo_00000000-0000-4000-8000-000000000001.git/info/refs?service=git-upload-pack", nil)
		req.SetPathValue("repoID", "repo_00000000-0000-4000-8000-000000000001")
		req.SetPathValue("gitPath", "info/refs")
		req.Header.Set("Authorization", "Bearer gtd_read_token")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if !backend.called {
			t.Fatal("backend was not called")
		}
	})
}

func TestNotImplementedGitHTTPBackend(t *testing.T) {
	backend := notImplementedGitHTTPBackend{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/git/repos/repo_00000000-0000-4000-8000-000000000001.git/info/refs?service=git-upload-pack", nil)

	backend.ServeHTTPGit(rec, req, gitAccessGrant{})

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
	if !strings.Contains(rec.Body.String(), "git http backend is not implemented") {
		t.Fatalf("body = %q, want backend not implemented message", rec.Body.String())
	}
}

func TestGitHTTPBackendEnvDoesNotInheritProcessSecrets(t *testing.T) {
	t.Setenv("GITRDONE_CONTROL_BEARER", "secret-control-token")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/malicious-gitconfig")

	req := httptest.NewRequest(http.MethodGet, "/git/repos/repo_00000000-0000-4000-8000-000000000001.git/info/refs?service=git-upload-pack", nil)
	req.SetPathValue("gitPath", "info/refs")
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("Git-Protocol", "version=2")
	grant := gitAccessGrant{
		RepoPath: "/tmp/repos/00000000-0000-4000-8000-000000000001.git",
		Subject:  "external:dev@example.com",
	}

	env := gitHTTPBackendEnv(req, grant)

	if got, ok := envValue(env, "GITRDONE_CONTROL_BEARER"); ok {
		t.Fatalf("backend env inherited control bearer: %q", got)
	}
	if got := envValueOrEmpty(env, "GIT_CONFIG_GLOBAL"); got != os.DevNull {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q, want %q", got, os.DevNull)
	}
	if strings.Contains(strings.Join(env, "\n"), "secret-control-token") {
		t.Fatalf("backend env leaked control bearer: %#v", env)
	}
	if strings.Contains(strings.Join(env, "\n"), "/tmp/malicious-gitconfig") {
		t.Fatalf("backend env inherited caller git config: %#v", env)
	}
	if got := envValueOrEmpty(env, "REMOTE_USER"); got != "external:dev@example.com" {
		t.Fatalf("REMOTE_USER = %q, want subject", got)
	}
	if got := envValueOrEmpty(env, "GIT_PROTOCOL"); got != "version=2" {
		t.Fatalf("GIT_PROTOCOL = %q, want version=2", got)
	}
}

func TestRunGitDoesNotInheritProcessSecrets(t *testing.T) {
	binDir := t.TempDir()
	fakeGit := filepath.Join(binDir, "git")
	script := `#!/bin/sh
printf 'control=%s\n' "${GITRDONE_CONTROL_BEARER-}"
printf 'global=%s\n' "${GIT_CONFIG_GLOBAL-}"
exit 1
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir)
	t.Setenv("GITRDONE_CONTROL_BEARER", "secret-control-token")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/malicious-gitconfig")

	err := runGit(context.Background(), "env-check")
	if err == nil {
		t.Fatal("runGit succeeded, want fake git failure")
	}
	if strings.Contains(err.Error(), "secret-control-token") {
		t.Fatalf("runGit error leaked inherited control bearer: %v", err)
	}
	if strings.Contains(err.Error(), "/tmp/malicious-gitconfig") {
		t.Fatalf("runGit error inherited caller git config: %v", err)
	}
}

func envValueOrEmpty(env []string, key string) string {
	value, _ := envValue(env, key)
	return value
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		value, ok := strings.CutPrefix(entry, prefix)
		if ok {
			return value, true
		}
	}
	return "", false
}

type recordingGitAccessAuthorizer struct {
	input authorizeGitAccessInput
	grant gitAccessGrant
	err   error
}

func (authorizer *recordingGitAccessAuthorizer) AuthorizeGitAccess(ctx context.Context, input authorizeGitAccessInput) (gitAccessGrant, error) {
	authorizer.input = input
	if authorizer.err != nil {
		return gitAccessGrant{}, authorizer.err
	}
	return authorizer.grant, nil
}

type recordingGitHTTPBackend struct {
	status  int
	called  bool
	request *http.Request
	grant   gitAccessGrant
	err     error
}

func (backend *recordingGitHTTPBackend) ServeHTTPGit(w http.ResponseWriter, r *http.Request, grant gitAccessGrant) error {
	backend.called = true
	backend.request = r
	backend.grant = grant
	if backend.err != nil {
		return backend.err
	}
	w.WriteHeader(backend.status)
	return nil
}

var errRecordingGitBackend = errors.New("recording git backend error")
