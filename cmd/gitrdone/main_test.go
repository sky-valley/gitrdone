package main

import (
	"context"
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
}

func TestConfigFromEnvUsesOverrides(t *testing.T) {
	values := map[string]string{
		"GITRDONE_ADDR":           "127.0.0.1:9090",
		"GITRDONE_BASE_URL":       "https://git.example.com",
		"GITRDONE_CONTROL_BEARER": "internal-admin-token",
		"GITRDONE_DATABASE_URL":   "postgres://gitrdone:secret@db.example.com:5432/gitrdone?sslmode=require",
		"GITRDONE_STORAGE_ROOT":   "/var/lib/gitrdone",
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
}

func TestConfigFromEnvRequiresControlBearer(t *testing.T) {
	_, err := configFromEnv(func(key string) string {
		return ""
	})
	if err == nil {
		t.Fatal("err is nil, want missing control bearer error")
	}
}

func TestHTTPServerUsesDefensiveTimeouts(t *testing.T) {
	server, closeServer, err := newHTTPServer(context.Background(), config{
		addr:          "127.0.0.1:9090",
		baseURL:       "https://git.example.com",
		controlBearer: "internal-admin-token",
		storageRoot:   t.TempDir(),
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
