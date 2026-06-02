package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
