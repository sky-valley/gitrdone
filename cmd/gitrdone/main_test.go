package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
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
	requireTrustedProxy(t, cfg.trustedProxyPrefixes, "127.0.0.1")
	requireTrustedProxy(t, cfg.trustedProxyPrefixes, "::1")
}

func TestConfigFromEnvUsesOverrides(t *testing.T) {
	values := map[string]string{
		"GITRDONE_ADDR":            "127.0.0.1:9090",
		"GITRDONE_BASE_URL":        "https://git.example.com",
		"GITRDONE_CONTROL_BEARER":  "internal-admin-token",
		"GITRDONE_DATABASE_URL":    "postgres://gitrdone:secret@db.example.com:5432/gitrdone?sslmode=require",
		"GITRDONE_STORAGE_ROOT":    "/var/lib/gitrdone",
		"GITRDONE_TRUSTED_PROXIES": "10.0.0.0/8,192.0.2.10",
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
	requireTrustedProxy(t, cfg.trustedProxyPrefixes, "10.20.30.40")
	requireTrustedProxy(t, cfg.trustedProxyPrefixes, "192.0.2.10")
	requireUntrustedProxy(t, cfg.trustedProxyPrefixes, "127.0.0.1")
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
