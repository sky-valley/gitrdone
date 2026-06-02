package main

import "testing"

func TestConfigFromEnvUsesLocalDefaults(t *testing.T) {
	cfg, err := configFromEnv(func(key string) string {
		if key == "GITERDONE_CONTROL_BEARER" {
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
	if cfg.controlBearer != "internal-admin-token" {
		t.Fatalf("controlBearer = %q, want internal-admin-token", cfg.controlBearer)
	}
}

func TestConfigFromEnvUsesOverrides(t *testing.T) {
	values := map[string]string{
		"GITERDONE_ADDR":           "127.0.0.1:9090",
		"GITERDONE_BASE_URL":       "https://git.example.com",
		"GITERDONE_CONTROL_BEARER": "internal-admin-token",
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
	if cfg.controlBearer != "internal-admin-token" {
		t.Fatalf("controlBearer = %q, want internal-admin-token", cfg.controlBearer)
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
