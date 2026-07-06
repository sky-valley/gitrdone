package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseGitDiffSpec(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	other := "89abcdef0123456789abcdef0123456789abcdef"

	tests := []struct {
		name        string
		kind        gitDiffKind
		raw         string
		wantOK      bool
		wantRevspec string
		wantRevs    []string
	}{
		{name: "show full sha", kind: gitDiffShow, raw: sha + ".diff", wantOK: true, wantRevspec: sha, wantRevs: []string{sha}},
		{name: "show abbreviated sha", kind: gitDiffShow, raw: "abc1234.diff", wantOK: true, wantRevspec: "abc1234", wantRevs: []string{"abc1234"}},
		{name: "show missing suffix", kind: gitDiffShow, raw: sha, wantOK: false},
		{name: "show wrong suffix", kind: gitDiffShow, raw: sha + ".patch", wantOK: false},
		{name: "show empty rev", kind: gitDiffShow, raw: ".diff", wantOK: false},
		{name: "show too short", kind: gitDiffShow, raw: "abc12.diff", wantOK: false},
		{name: "show uppercase", kind: gitDiffShow, raw: "ABC1234.diff", wantOK: false},
		{name: "show non hex", kind: gitDiffShow, raw: "abc12zz.diff", wantOK: false},
		{name: "show leading dash", kind: gitDiffShow, raw: "-bc1234.diff", wantOK: false},
		{
			name: "compare two dot", kind: gitDiffCompare, raw: sha + ".." + other + ".diff",
			wantOK: true, wantRevspec: sha + ".." + other, wantRevs: []string{sha, other},
		},
		{
			name: "compare three dot", kind: gitDiffCompare, raw: sha + "..." + other + ".diff",
			wantOK: true, wantRevspec: sha + "..." + other, wantRevs: []string{sha, other},
		},
		{name: "compare missing separator", kind: gitDiffCompare, raw: sha + ".diff", wantOK: false},
		{name: "compare short head", kind: gitDiffCompare, raw: sha + "..abc.diff", wantOK: false},
		{name: "compare four dots", kind: gitDiffCompare, raw: sha + "...." + other + ".diff", wantOK: false},
		{name: "compare empty base", kind: gitDiffCompare, raw: ".." + other + ".diff", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, ok := parseGitDiffSpec(tt.kind, tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if target.kind != tt.kind {
				t.Fatalf("kind = %v, want %v", target.kind, tt.kind)
			}
			if target.revspec != tt.wantRevspec {
				t.Fatalf("revspec = %q, want %q", target.revspec, tt.wantRevspec)
			}
			if len(target.revs) != len(tt.wantRevs) {
				t.Fatalf("revs = %v, want %v", target.revs, tt.wantRevs)
			}
			for i, rev := range tt.wantRevs {
				if target.revs[i] != rev {
					t.Fatalf("revs[%d] = %q, want %q", i, target.revs[i], rev)
				}
			}
		})
	}
}

func TestGitDiffHandlerBoundary(t *testing.T) {
	const rawRepoID = "00000000-0000-4000-8000-000000000001"
	const pathRepoID = "repo_" + rawRepoID
	const sha = "0123456789abcdef0123456789abcdef01234567"

	newRequest := func(spec string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/git/repos/"+pathRepoID+".git/show/"+spec, nil)
		req.SetPathValue("repoID", pathRepoID)
		req.SetPathValue("spec", spec)
		req.Header.Set("Authorization", "Bearer gtd_read_token")
		return req
	}

	t.Run("authorized request is delegated with a read grant and parsed target", func(t *testing.T) {
		access := &recordingGitAccessAuthorizer{
			grant: gitAccessGrant{RepoID: rawRepoID, RepoPath: "/tmp/repos/" + rawRepoID + ".git"},
		}
		backend := &recordingGitDiffBackend{status: http.StatusOK}
		handler := gitDiffHandler(access, backend, gitDiffShow)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(sha+".diff"))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if access.input.RepoID != rawRepoID {
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
		if backend.target.revspec != sha {
			t.Fatalf("target revspec = %q, want %q", backend.target.revspec, sha)
		}
	})

	t.Run("invalid repo id is rejected before authorization", func(t *testing.T) {
		access := &recordingGitAccessAuthorizer{}
		backend := &recordingGitDiffBackend{status: http.StatusOK}
		handler := gitDiffHandler(access, backend, gitDiffShow)

		req := newRequest(sha + ".diff")
		req.SetPathValue("repoID", "not-a-repo-id")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if backend.called {
			t.Fatal("backend was called for an invalid repo id")
		}
	})

	t.Run("invalid revision is rejected before authorization", func(t *testing.T) {
		access := &recordingGitAccessAuthorizer{}
		backend := &recordingGitDiffBackend{status: http.StatusOK}
		handler := gitDiffHandler(access, backend, gitDiffShow)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest("not-hex.diff"))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if backend.called {
			t.Fatal("backend was called for an invalid revision")
		}
	})

	t.Run("authorization failure is not delegated to the backend", func(t *testing.T) {
		access := &recordingGitAccessAuthorizer{err: errRepoTokenForbidden}
		backend := &recordingGitDiffBackend{status: http.StatusOK}
		handler := gitDiffHandler(access, backend, gitDiffShow)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(sha+".diff"))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if backend.called {
			t.Fatal("backend was called despite authorization failure")
		}
	})

	t.Run("backend error becomes an internal server error", func(t *testing.T) {
		access := &recordingGitAccessAuthorizer{
			grant: gitAccessGrant{RepoID: rawRepoID, RepoPath: "/tmp/repos/" + rawRepoID + ".git"},
		}
		backend := &recordingGitDiffBackend{err: errRecordingGitBackend}
		handler := gitDiffHandler(access, backend, gitDiffShow)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(sha+".diff"))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if !backend.called {
			t.Fatal("backend was not called")
		}
	})
}

func TestGitDiffRoutesUseCanonicalRepoID(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantSpec string
		route    string
	}{
		{name: "show", target: "/git/repos/repo_123.git/show/0123abc.diff", wantSpec: "0123abc.diff", route: "show"},
		{name: "compare", target: "/git/repos/repo_123.git/compare/0123abc..89abdef.diff", wantSpec: "0123abc..89abdef.diff", route: "compare"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRepoID, gotSpec, gotRoute string
			record := func(route string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotRepoID = r.PathValue("repoID")
					gotSpec = r.PathValue("spec")
					gotRoute = route
					w.WriteHeader(http.StatusNoContent)
				})
			}
			mux := NewMux(Handlers{
				Healthz:         internalServerError(),
				CreateRepo:      internalServerError(),
				GetRepo:         internalServerError(),
				ArchiveRepo:     internalServerError(),
				CreateRepoToken: internalServerError(),
				ListRepoTokens:  internalServerError(),
				RevokeRepoToken: internalServerError(),
				// A different handler on the greedy git route proves the more
				// specific show/compare routes win precedence over {gitPath...}.
				GitSmartHTTP:   record("smart"),
				GitShowDiff:    record("show"),
				GitCompareDiff: record("compare"),
			})

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if gotRoute != tt.route {
				t.Fatalf("route = %q, want %q (greedy git route stole the request)", gotRoute, tt.route)
			}
			if gotRepoID != "repo_123" {
				t.Fatalf("repoID = %q, want repo_123", gotRepoID)
			}
			if gotSpec != tt.wantSpec {
				t.Fatalf("spec = %q, want %q", gotSpec, tt.wantSpec)
			}
		})
	}
}

type recordingGitDiffBackend struct {
	status  int
	called  bool
	request *http.Request
	grant   gitAccessGrant
	target  gitDiffTarget
	err     error
}

func (backend *recordingGitDiffBackend) ServeGitDiff(w http.ResponseWriter, r *http.Request, grant gitAccessGrant, target gitDiffTarget) error {
	backend.called = true
	backend.request = r
	backend.grant = grant
	backend.target = target
	if backend.err != nil {
		return backend.err
	}
	w.WriteHeader(backend.status)
	return nil
}
