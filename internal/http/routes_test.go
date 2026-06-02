package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlRoutesUseCanonicalRepoID(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
	}{
		{
			name:   "get repo",
			method: http.MethodGet,
			target: "/v1/repos/repo_123",
		},
		{
			name:   "archive repo",
			method: http.MethodPost,
			target: "/v1/repos/repo_123/archive",
		},
		{
			name:   "create repo token",
			method: http.MethodPost,
			target: "/v1/repos/repo_123/tokens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRepoID string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRepoID = r.PathValue("repoID")
				w.WriteHeader(http.StatusNoContent)
			})
			mux := NewMux(Handlers{
				Healthz:         internalServerError(),
				CreateRepo:      internalServerError(),
				GetRepo:         handler,
				ArchiveRepo:     handler,
				CreateRepoToken: handler,
				GitSmartHTTP:    internalServerError(),
			})

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, nil))

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if gotRepoID != "repo_123" {
				t.Fatalf("repoID = %q, want repo_123", gotRepoID)
			}
		})
	}
}
