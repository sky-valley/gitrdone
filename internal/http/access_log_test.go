package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestAccessLogWritesBoundedNonLeakyRecord(t *testing.T) {
	var logs bytes.Buffer
	handler := accessLog(&logs, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"response-secret"}`))
	}))
	req := httptest.NewRequest(http.MethodPost, "https://git.example.com/v1/repos?token=query-secret", strings.NewReader("request-secret"))
	req.RemoteAddr = "198.51.100.9:49152"
	req.Header.Set("Authorization", "Bearer header-secret")
	req.Header.Set("Cookie", "session=cookie-secret")
	req.Header.Set("User-Agent", "git/2.45")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	record := accessLogRecord(t, logs.String())
	requireLogField(t, record, "method", http.MethodPost)
	requireLogField(t, record, "path", "/v1/repos")
	requireLogField(t, record, "status", float64(http.StatusCreated))
	requireLogField(t, record, "bytes", float64(len(`{"token":"response-secret"}`)))
	requireLogField(t, record, "remoteIp", "198.51.100.9")
	requireLogField(t, record, "scheme", "https")
	requireLogField(t, record, "host", "git.example.com")
	requireLogField(t, record, "userAgent", "git/2.45")
	if _, ok := record["durationMs"]; !ok {
		t.Fatal("durationMs is missing")
	}
	if _, ok := record["timestamp"]; !ok {
		t.Fatal("timestamp is missing")
	}

	logLine := logs.String()
	for _, secret := range []string{"query-secret", "request-secret", "header-secret", "cookie-secret", "response-secret"} {
		if strings.Contains(logLine, secret) {
			t.Fatalf("access log leaked %q in %q", secret, logLine)
		}
	}
}

func TestAccessLogTrustsForwardedHeadersOnlyFromTrustedProxies(t *testing.T) {
	tests := []struct {
		name         string
		remoteAddr   string
		trusted      []netip.Prefix
		wantRemoteIP string
		wantScheme   string
	}{
		{
			name:         "trusted loopback proxy",
			remoteAddr:   "127.0.0.1:52144",
			trusted:      []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
			wantRemoteIP: "203.0.113.10",
			wantScheme:   "https",
		},
		{
			name:         "untrusted remote peer",
			remoteAddr:   "198.51.100.20:52144",
			trusted:      []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
			wantRemoteIP: "198.51.100.20",
			wantScheme:   "http",
		},
		{
			name:         "malformed forwarded chain",
			remoteAddr:   "127.0.0.1:52144",
			trusted:      []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")},
			wantRemoteIP: "127.0.0.1",
			wantScheme:   "https",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			handler := accessLog(&logs, tt.trusted, noContent())
			req := httptest.NewRequest(http.MethodGet, "http://git.example.com/v1/repos/repo_123", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.name == "malformed forwarded chain" {
				req.Header.Set("X-Forwarded-For", "not an ip, 203.0.113.10")
			} else {
				req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.4")
			}
			req.Header.Set("X-Forwarded-Proto", "https")

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			record := accessLogRecord(t, logs.String())
			requireLogField(t, record, "remoteIp", tt.wantRemoteIP)
			requireLogField(t, record, "scheme", tt.wantScheme)
		})
	}
}

func TestAccessLogRecordsPanicsWithoutRecoveringThem(t *testing.T) {
	var logs bytes.Buffer
	handler := accessLog(&logs, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest(http.MethodGet, "https://git.example.com/v1/repos", nil)
	req.RemoteAddr = "198.51.100.9:49152"

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("panic was recovered, want it to propagate")
		}
		record := accessLogRecord(t, logs.String())
		requireLogField(t, record, "status", float64(http.StatusInternalServerError))
	}()

	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func TestAccessLogSkipsNoisyProbeRoutes(t *testing.T) {
	tests := []string{
		"/",
		"/healthz",
	}

	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			var logs bytes.Buffer
			handler := accessLog(&logs, nil, noContent())
			req := httptest.NewRequest(http.MethodGet, "https://git.example.com"+target, nil)
			req.RemoteAddr = "198.51.100.9:49152"

			handler.ServeHTTP(httptest.NewRecorder(), req)

			if logs.Len() != 0 {
				t.Fatalf("logs = %q, want none", logs.String())
			}
		})
	}
}

func TestAccessLogKeepsAPIRoutes(t *testing.T) {
	var logs bytes.Buffer
	handler := accessLog(&logs, nil, noContent())
	req := httptest.NewRequest(http.MethodGet, "https://git.example.com/v1/repos/repo_123", nil)
	req.RemoteAddr = "198.51.100.9:49152"

	handler.ServeHTTP(httptest.NewRecorder(), req)

	if logs.Len() == 0 {
		t.Fatal("logs are empty, want access log for API route")
	}
}

func accessLogRecord(t *testing.T, line string) map[string]any {
	t.Helper()
	line = strings.TrimSpace(line)
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("log line count = %d, want 1: %q", strings.Count(line, "\n")+1, line)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("log line is not JSON: %v: %q", err, line)
	}
	return record
}

func requireLogField(t *testing.T, record map[string]any, field string, want any) {
	t.Helper()
	if got := record[field]; got != want {
		t.Fatalf("%s = %#v, want %#v", field, got, want)
	}
}
