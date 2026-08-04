package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

func TestConfigFromEnvUsesLocalDefaults(t *testing.T) {
	cfg, err := configFromEnv(func(key string) string {
		if key == "GITRDONE_CONTROL_BEARER" {
			return "internal-admin-token"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.addr != ":8080" {
		t.Fatalf("addr = %q, want :8080", cfg.addr)
	}
	if cfg.baseURL != "http://localhost:8080" {
		t.Fatalf("baseURL = %q, want http://localhost:8080", cfg.baseURL)
	}
	if cfg.storageRoot != ".storage" {
		t.Fatalf("storageRoot = %q, want .storage", cfg.storageRoot)
	}
	if cfg.controlBearer != "internal-admin-token" {
		t.Fatalf("controlBearer = %q, want internal-admin-token", cfg.controlBearer)
	}
	if cfg.databaseURL != "" {
		t.Fatalf("databaseURL = %q, want empty", cfg.databaseURL)
	}
	if cfg.judgementWorkers != 0 {
		t.Fatalf("judgementWorkers = %d, want disabled by default", cfg.judgementWorkers)
	}
	if cfg.sentryDSN != "" {
		t.Fatalf("sentryDSN = %q, want empty", cfg.sentryDSN)
	}
	if cfg.sentryEnvironment != "" {
		t.Fatalf("sentryEnvironment = %q, want empty", cfg.sentryEnvironment)
	}
	if cfg.sentryRelease != "" {
		t.Fatalf("sentryRelease = %q, want empty", cfg.sentryRelease)
	}
	if cfg.sentryTracesSampleRate != 0 {
		t.Fatalf("sentryTracesSampleRate = %f, want 0", cfg.sentryTracesSampleRate)
	}
	if cfg.shutdownTimeout != defaultShutdownTimeout {
		t.Fatalf("shutdownTimeout = %s, want %s", cfg.shutdownTimeout, defaultShutdownTimeout)
	}
	if cfg.maxLFSObjectBytes != defaultMaxLFSObjectBytes {
		t.Fatalf("maxLFSObjectBytes = %d, want %d", cfg.maxLFSObjectBytes, defaultMaxLFSObjectBytes)
	}
	requireTrustedProxy(t, cfg.trustedProxyPrefixes, "127.0.0.1")
	requireTrustedProxy(t, cfg.trustedProxyPrefixes, "::1")
}

func TestConfigFromEnvUsesOverrides(t *testing.T) {
	values := map[string]string{
		"GITRDONE_ADDR":                 "127.0.0.1:9090",
		"GITRDONE_BASE_URL":             "https://git.example.com",
		"GITRDONE_CONTROL_BEARER":       "internal-admin-token",
		"GITRDONE_DATABASE_URL":         "postgres://gitrdone:secret@db.example.com:5432/gitrdone?sslmode=require",
		"GITRDONE_STORAGE_ROOT":         "/var/lib/gitrdone",
		"GITRDONE_TRUSTED_PROXIES":      "10.0.0.0/8,192.0.2.10",
		"GITRDONE_SHUTDOWN_TIMEOUT":     "30s",
		"GITRDONE_MAX_LFS_OBJECT_BYTES": "12345",
		"GITRDONE_JUDGEMENT_WORKERS":    "2",
		"GITRDONE_JUDGEMENT_MODEL":      "claude-opus-4-8",
		"ANTHROPIC_API_KEY":             "test-anthropic-key",
		"SENTRY_DSN":                    "https://public@example.ingest.sentry.io/123",
		"SENTRY_ENVIRONMENT":            "main",
		"SENTRY_RELEASE":                "abc1234",
		"SENTRY_TRACES_SAMPLE_RATE":     "0.25",
	}

	cfg, err := configFromEnv(func(key string) string {
		return values[key]
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.addr != "127.0.0.1:9090" {
		t.Fatalf("addr = %q, want 127.0.0.1:9090", cfg.addr)
	}
	if cfg.baseURL != "https://git.example.com" {
		t.Fatalf("baseURL = %q, want https://git.example.com", cfg.baseURL)
	}
	if cfg.storageRoot != "/var/lib/gitrdone" {
		t.Fatalf("storageRoot = %q, want /var/lib/gitrdone", cfg.storageRoot)
	}
	if cfg.controlBearer != "internal-admin-token" {
		t.Fatalf("controlBearer = %q, want internal-admin-token", cfg.controlBearer)
	}
	if cfg.databaseURL != "postgres://gitrdone:secret@db.example.com:5432/gitrdone?sslmode=require" {
		t.Fatalf("databaseURL = %q, want configured Postgres URL", cfg.databaseURL)
	}
	if cfg.shutdownTimeout != 30*time.Second {
		t.Fatalf("shutdownTimeout = %s, want 30s", cfg.shutdownTimeout)
	}
	if cfg.maxLFSObjectBytes != 12345 {
		t.Fatalf("maxLFSObjectBytes = %d, want 12345", cfg.maxLFSObjectBytes)
	}
	if cfg.judgementWorkers != 2 {
		t.Fatalf("judgementWorkers = %d, want 2", cfg.judgementWorkers)
	}
	if cfg.judgementModel != "claude-opus-4-8" {
		t.Fatalf("judgementModel = %q, want claude-opus-4-8", cfg.judgementModel)
	}
	if cfg.anthropicAPIKey != "test-anthropic-key" {
		t.Fatalf("anthropicAPIKey = %q, want configured key", cfg.anthropicAPIKey)
	}
	if cfg.sentryDSN != "https://public@example.ingest.sentry.io/123" {
		t.Fatalf("sentryDSN = %q, want configured DSN", cfg.sentryDSN)
	}
	if cfg.sentryEnvironment != "main" {
		t.Fatalf("sentryEnvironment = %q, want main", cfg.sentryEnvironment)
	}
	if cfg.sentryRelease != "abc1234" {
		t.Fatalf("sentryRelease = %q, want abc1234", cfg.sentryRelease)
	}
	if cfg.sentryTracesSampleRate != 0.25 {
		t.Fatalf("sentryTracesSampleRate = %f, want 0.25", cfg.sentryTracesSampleRate)
	}
	requireTrustedProxy(t, cfg.trustedProxyPrefixes, "10.20.30.40")
	requireTrustedProxy(t, cfg.trustedProxyPrefixes, "192.0.2.10")
	requireUntrustedProxy(t, cfg.trustedProxyPrefixes, "127.0.0.1")
}

func TestConfigFromEnvDefaultsJudgementModel(t *testing.T) {
	cfg, err := configFromEnv(func(key string) string {
		values := map[string]string{
			"GITRDONE_CONTROL_BEARER":    "internal-admin-token",
			"GITRDONE_JUDGEMENT_WORKERS": "1",
			"ANTHROPIC_API_KEY":          "test-anthropic-key",
		}
		return values[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.judgementModel != "claude-sonnet-5" {
		t.Fatalf("judgementModel = %q, want claude-sonnet-5", cfg.judgementModel)
	}
}

func TestConfigFromEnvRequiresAnthropicKeyWhenJudgementRuns(t *testing.T) {
	_, err := configFromEnv(func(key string) string {
		if key == "GITRDONE_CONTROL_BEARER" {
			return "internal-admin-token"
		}
		if key == "GITRDONE_JUDGEMENT_WORKERS" {
			return "1"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("config error = %v, want missing Anthropic key", err)
	}
}

func TestConfigFromEnvRequiresControlBearer(t *testing.T) {
	_, err := configFromEnv(func(key string) string {
		return ""
	})
	if err == nil {
		t.Fatal("err is nil, want missing control bearer error")
	}
}

func TestConfigFromEnvRejectsInvalidTrustedProxy(t *testing.T) {
	_, err := configFromEnv(func(key string) string {
		switch key {
		case "GITRDONE_CONTROL_BEARER":
			return "internal-admin-token"
		case "GITRDONE_TRUSTED_PROXIES":
			return "not a cidr"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("err is nil, want invalid trusted proxy error")
	}
}

func TestConfigFromEnvRejectsInvalidShutdownTimeout(t *testing.T) {
	_, err := configFromEnv(func(key string) string {
		switch key {
		case "GITRDONE_CONTROL_BEARER":
			return "internal-admin-token"
		case "GITRDONE_SHUTDOWN_TIMEOUT":
			return "eventually"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("err is nil, want invalid shutdown timeout error")
	}
}

func TestConfigFromEnvRejectsNonPositiveShutdownTimeout(t *testing.T) {
	_, err := configFromEnv(func(key string) string {
		switch key {
		case "GITRDONE_CONTROL_BEARER":
			return "internal-admin-token"
		case "GITRDONE_SHUTDOWN_TIMEOUT":
			return "0s"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("err is nil, want non-positive shutdown timeout error")
	}
}

func TestConfigFromEnvRejectsInvalidMaxLFSObjectBytes(t *testing.T) {
	tests := []string{"not-a-number", "0", "-1"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			_, err := configFromEnv(func(key string) string {
				switch key {
				case "GITRDONE_CONTROL_BEARER":
					return "internal-admin-token"
				case "GITRDONE_MAX_LFS_OBJECT_BYTES":
					return value
				default:
					return ""
				}
			})
			if err == nil {
				t.Fatal("err is nil, want invalid max LFS object bytes error")
			}
		})
	}
}

func TestConfigFromEnvRejectsInvalidJudgementWorkers(t *testing.T) {
	tests := []string{"not-a-number", "-1"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			_, err := configFromEnv(func(key string) string {
				switch key {
				case "GITRDONE_CONTROL_BEARER":
					return "internal-admin-token"
				case "GITRDONE_JUDGEMENT_WORKERS":
					return value
				default:
					return ""
				}
			})
			if err == nil {
				t.Fatal("err is nil, want invalid judgement worker count error")
			}
		})
	}
}

func TestConfigFromEnvRejectsInvalidSentryTracesSampleRate(t *testing.T) {
	tests := []string{"not-a-number", "-0.1", "1.1"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			_, err := configFromEnv(func(key string) string {
				switch key {
				case "GITRDONE_CONTROL_BEARER":
					return "internal-admin-token"
				case "SENTRY_TRACES_SAMPLE_RATE":
					return value
				default:
					return ""
				}
			})
			if err == nil {
				t.Fatal("err is nil, want invalid Sentry trace sample rate error")
			}
		})
	}
}

func TestSanitizeSentryEventDropsRequestSecrets(t *testing.T) {
	event := &sentry.Event{
		Request: &sentry.Request{
			URL:         "https://git.example.com/v1/repos?token=query-secret",
			Method:      http.MethodPost,
			QueryString: "token=query-secret",
			Cookies:     "session=cookie-secret",
			Data:        "body-secret",
			Headers: map[string]string{
				"Authorization":       "Bearer header-secret",
				"Cookie":              "session=cookie-secret",
				"Proxy-Authorization": "Basic proxy-secret",
				"User-Agent":          "git/2.50",
			},
		},
	}

	got := sanitizeSentryEvent(event, nil)

	if got.Request == nil {
		t.Fatal("Request is nil, want sanitized request context")
	}
	if got.Request.URL != "https://git.example.com/v1/repos" {
		t.Fatalf("URL = %q, want query stripped", got.Request.URL)
	}
	if got.Request.Method != http.MethodPost {
		t.Fatalf("Method = %q, want POST", got.Request.Method)
	}
	if got.Request.QueryString != "" {
		t.Fatalf("QueryString = %q, want empty", got.Request.QueryString)
	}
	if got.Request.Cookies != "" {
		t.Fatalf("Cookies = %q, want empty", got.Request.Cookies)
	}
	if got.Request.Data != "" {
		t.Fatalf("Data = %q, want empty", got.Request.Data)
	}
	if got.Request.Headers != nil {
		t.Fatalf("Headers = %#v, want nil", got.Request.Headers)
	}
}

func TestHTTPServerUsesDefensiveTimeouts(t *testing.T) {
	server, closeServer, err := newHTTPServer(context.Background(), config{
		addr:                 "127.0.0.1:9090",
		baseURL:              "https://git.example.com",
		controlBearer:        "internal-admin-token",
		storageRoot:          t.TempDir(),
		trustedProxyPrefixes: defaultTrustedProxyPrefixes,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeServer(); err != nil {
			t.Fatal(err)
		}
	})

	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 5s", server.ReadHeaderTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %s, want 1m0s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 32*1024 {
		t.Fatalf("MaxHeaderBytes = %d, want 32768", server.MaxHeaderBytes)
	}
}

func TestHTTPServerEmitsAccessLogsWithTrustedForwardedHeaders(t *testing.T) {
	var logs bytes.Buffer
	server, closeServer, err := newHTTPServer(context.Background(), config{
		addr:                 "127.0.0.1:9090",
		baseURL:              "https://git.example.com",
		controlBearer:        "internal-admin-token",
		storageRoot:          t.TempDir(),
		trustedProxyPrefixes: defaultTrustedProxyPrefixes,
		accessLog:            &logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeServer(); err != nil {
			t.Fatal(err)
		}
	})

	req := httptest.NewRequest(http.MethodGet, "http://git.example.com/v1/repos/repo_00000000-0000-4000-8000-000000000000?token=query-secret", nil)
	req.RemoteAddr = "127.0.0.1:49152"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Authorization", "Bearer header-secret")

	rec := httptest.NewRecorder()
	server.Handler.ServeHTTP(rec, req)

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatalf("access log is not JSON: %v: %q", err, logs.String())
	}
	if record["remoteIp"] != "203.0.113.10" {
		t.Fatalf("remoteIp = %#v, want forwarded client IP", record["remoteIp"])
	}
	if record["scheme"] != "https" {
		t.Fatalf("scheme = %#v, want forwarded https", record["scheme"])
	}
	for _, secret := range []string{"query-secret", "header-secret"} {
		if bytes.Contains(logs.Bytes(), []byte(secret)) {
			t.Fatalf("access log leaked %q in %q", secret, logs.String())
		}
	}
}

func TestRunServerShutsDownWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listening := make(chan struct{})
	allowServeReturn := make(chan struct{})
	shutdownCalled := make(chan struct{})
	closeCalled := make(chan struct{})

	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, serverLifecycle{
			listenAndServe: func() error {
				close(listening)
				<-allowServeReturn
				return http.ErrServerClosed
			},
			shutdown: func(context.Context) error {
				close(shutdownCalled)
				close(allowServeReturn)
				return nil
			},
			closeResources: func() error {
				close(closeCalled)
				return nil
			},
			shutdownTimeout: time.Minute,
		})
	}()

	<-listening
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("runServer error = %v, want nil", err)
	}
	requireClosed(t, shutdownCalled, "shutdown")
	requireClosed(t, closeCalled, "close resources")
}

func TestRunServerWaitsForShutdownBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listening := make(chan struct{})
	shutdownStarted := make(chan struct{})
	finishShutdown := make(chan struct{})
	allowServeReturn := make(chan struct{})

	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, serverLifecycle{
			listenAndServe: func() error {
				close(listening)
				<-allowServeReturn
				return http.ErrServerClosed
			},
			shutdown: func(context.Context) error {
				close(shutdownStarted)
				<-finishShutdown
				close(allowServeReturn)
				return nil
			},
			closeResources:  func() error { return nil },
			shutdownTimeout: time.Minute,
		})
	}()

	<-listening
	cancel()
	<-shutdownStarted
	select {
	case err := <-done:
		t.Fatalf("runServer returned before shutdown finished: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	close(finishShutdown)
	if err := <-done; err != nil {
		t.Fatalf("runServer error = %v, want nil", err)
	}
}

func TestRunServerReturnsShutdownTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listening := make(chan struct{})
	allowServeReturn := make(chan struct{})

	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, serverLifecycle{
			listenAndServe: func() error {
				close(listening)
				<-allowServeReturn
				return http.ErrServerClosed
			},
			shutdown: func(ctx context.Context) error {
				<-ctx.Done()
				close(allowServeReturn)
				return ctx.Err()
			},
			closeResources:  func() error { return nil },
			shutdownTimeout: time.Nanosecond,
		})
	}()

	<-listening
	cancel()

	err := <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runServer error = %v, want context deadline exceeded", err)
	}
}

func TestRunServerClosesResourcesWhenListenFails(t *testing.T) {
	listenErr := errors.New("listen failed")
	closeCalled := false

	err := runServer(context.Background(), serverLifecycle{
		listenAndServe: func() error {
			return listenErr
		},
		shutdown: func(context.Context) error {
			t.Fatal("shutdown called, want none")
			return nil
		},
		closeResources: func() error {
			closeCalled = true
			return nil
		},
		shutdownTimeout: time.Minute,
	})

	if !errors.Is(err, listenErr) {
		t.Fatalf("runServer error = %v, want listen error", err)
	}
	if !closeCalled {
		t.Fatal("closeResources was not called")
	}
}

func requireTrustedProxy(t *testing.T, prefixes []netip.Prefix, ip string) {
	t.Helper()
	addr := netip.MustParseAddr(ip)
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return
		}
	}
	t.Fatalf("%s is not trusted by %v", ip, prefixes)
}

func requireUntrustedProxy(t *testing.T, prefixes []netip.Prefix, ip string) {
	t.Helper()
	addr := netip.MustParseAddr(ip)
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			t.Fatalf("%s is trusted by %v, want untrusted", ip, prefixes)
		}
	}
}

func requireClosed(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatalf("%s was not called", name)
	}
}
