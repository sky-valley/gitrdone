package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlRoutesUseCanonicalRepoID(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		target      string
		wantRepoID  string
		wantTokenID string
	}{
		{
			name:       "get repo",
			method:     http.MethodGet,
			target:     "/v1/repos/repo_123",
			wantRepoID: "repo_123",
		},
		{
			name:       "archive repo",
			method:     http.MethodPost,
			target:     "/v1/repos/repo_123/archive",
			wantRepoID: "repo_123",
		},
		{
			name:       "create repo token",
			method:     http.MethodPost,
			target:     "/v1/repos/repo_123/tokens",
			wantRepoID: "repo_123",
		},
		{
			name:       "list repo tokens",
			method:     http.MethodGet,
			target:     "/v1/repos/repo_123/tokens",
			wantRepoID: "repo_123",
		},
		{
			name:        "revoke repo token",
			method:      http.MethodPost,
			target:      "/v1/repos/repo_123/tokens/token_abc/revoke",
			wantRepoID:  "repo_123",
			wantTokenID: "token_abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRepoID string
			var gotTokenID string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRepoID = r.PathValue("repoID")
				gotTokenID = r.PathValue("tokenID")
				w.WriteHeader(http.StatusNoContent)
			})
			mux := NewMux(Handlers{
				Healthz:         internalServerError(),
				CreateRepo:      internalServerError(),
				GetRepo:         handler,
				ArchiveRepo:     handler,
				CreateRepoToken: handler,
				ListRepoTokens:  handler,
				RevokeRepoToken: handler,
				GitSmartHTTP:    internalServerError(),
			})

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, nil))

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if gotRepoID != tt.wantRepoID {
				t.Fatalf("repoID = %q, want %q", gotRepoID, tt.wantRepoID)
			}
			if gotTokenID != tt.wantTokenID {
				t.Fatalf("tokenID = %q, want %q", gotTokenID, tt.wantTokenID)
			}
		})
	}
}

func TestGitRoutesUseCanonicalRepoID(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		target      string
		wantRepoID  string
		wantGitPath string
	}{
		{
			name:        "upload-pack discovery",
			method:      http.MethodGet,
			target:      "/git/repos/repo_123.git/info/refs?service=git-upload-pack",
			wantRepoID:  "repo_123",
			wantGitPath: "info/refs",
		},
		{
			name:        "upload-pack request",
			method:      http.MethodPost,
			target:      "/git/repos/repo_123.git/git-upload-pack",
			wantRepoID:  "repo_123",
			wantGitPath: "git-upload-pack",
		},
		{
			name:        "receive-pack request",
			method:      http.MethodPost,
			target:      "/git/repos/repo_123.git/git-receive-pack",
			wantRepoID:  "repo_123",
			wantGitPath: "git-receive-pack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRepoID string
			var gotGitPath string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRepoID = r.PathValue("repoID")
				gotGitPath = r.PathValue("gitPath")
				w.WriteHeader(http.StatusNoContent)
			})
			mux := NewMux(Handlers{
				Healthz:         internalServerError(),
				CreateRepo:      internalServerError(),
				GetRepo:         internalServerError(),
				ArchiveRepo:     internalServerError(),
				CreateRepoToken: internalServerError(),
				ListRepoTokens:  internalServerError(),
				RevokeRepoToken: internalServerError(),
				GitSmartHTTP:    handler,
			})

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, nil))

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if gotRepoID != tt.wantRepoID {
				t.Fatalf("repoID = %q, want %q", gotRepoID, tt.wantRepoID)
			}
			if gotGitPath != tt.wantGitPath {
				t.Fatalf("gitPath = %q, want %q", gotGitPath, tt.wantGitPath)
			}
		})
	}
}

func TestGitRoutesRejectNamespaceAliases(t *testing.T) {
	mux := NewMux(Handlers{
		Healthz:         internalServerError(),
		CreateRepo:      internalServerError(),
		GetRepo:         internalServerError(),
		ArchiveRepo:     internalServerError(),
		CreateRepoToken: internalServerError(),
		ListRepoTokens:  internalServerError(),
		RevokeRepoToken: internalServerError(),
		GitSmartHTTP:    internalServerError(),
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/acme/project-123.git/info/refs?service=git-upload-pack", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
