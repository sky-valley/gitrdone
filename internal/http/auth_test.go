package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlAuth(t *testing.T) {
	tests := []struct {
		name             string
		expected         string
		authorization    string
		wantStatus       int
		wantNextCalls    int
		wantResponseBody string
	}{
		{
			name:             "missing configured token denies closed",
			expected:         "",
			authorization:    "Bearer internal-admin-token",
			wantStatus:       http.StatusUnauthorized,
			wantResponseBody: "",
		},
		{
			name:             "missing authorization denies",
			expected:         "internal-admin-token",
			authorization:    "",
			wantStatus:       http.StatusUnauthorized,
			wantResponseBody: "",
		},
		{
			name:             "wrong authorization scheme denies",
			expected:         "internal-admin-token",
			authorization:    "Basic aW50ZXJuYWwtYWRtaW4tdG9rZW4=",
			wantStatus:       http.StatusUnauthorized,
			wantResponseBody: "",
		},
		{
			name:             "empty bearer token denies",
			expected:         "internal-admin-token",
			authorization:    "Bearer ",
			wantStatus:       http.StatusUnauthorized,
			wantResponseBody: "",
		},
		{
			name:             "wrong bearer token denies",
			expected:         "internal-admin-token",
			authorization:    "Bearer wrong-token",
			wantStatus:       http.StatusUnauthorized,
			wantResponseBody: "",
		},
		{
			name:             "exact bearer token allows",
			expected:         "internal-admin-token",
			authorization:    "Bearer internal-admin-token",
			wantStatus:       http.StatusNoContent,
			wantNextCalls:    1,
			wantResponseBody: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				w.WriteHeader(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/repos", nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()

			controlAuth(tt.expected, next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if nextCalls != tt.wantNextCalls {
				t.Fatalf("next calls = %d, want %d", nextCalls, tt.wantNextCalls)
			}
			if rec.Body.String() != tt.wantResponseBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.wantResponseBody)
			}
		})
	}
}
