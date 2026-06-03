package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	httpapi "skyvalley.ac/m/v2/internal/http"
)

const (
	defaultAddr        = ":8080"
	defaultBaseURL     = "http://localhost:8080"
	defaultStorageRoot = ".storage"

	defaultReadHeaderTimeout = 5 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultMaxHeaderBytes    = 32 * 1024
)

var defaultTrustedProxyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.1/32"),
	netip.MustParsePrefix("::1/128"),
}

type config struct {
	addr                 string
	baseURL              string
	controlBearer        string
	storageRoot          string
	databaseURL          string
	trustedProxyPrefixes []netip.Prefix
	accessLog            io.Writer
}

func main() {
	ctx := context.Background()
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	cfg.accessLog = os.Stdout
	storageRoot, err := filepath.Abs(cfg.storageRoot)
	if err != nil {
		log.Fatal(err)
	}

	server, closeServer, err := newHTTPServer(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := closeServer(); err != nil {
			log.Printf("close server resources: %v", err)
		}
	}()

	log.Printf("gitrdone listening on %s, storage=%s", cfg.addr, storageRoot)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func newHTTPServer(ctx context.Context, cfg config) (*http.Server, func() error, error) {
	handler := httpapi.NewServer(httpapi.Config{
		BaseURL:              cfg.baseURL,
		ControlBearer:        cfg.controlBearer,
		StorageRoot:          cfg.storageRoot,
		AccessLog:            cfg.accessLog,
		TrustedProxyPrefixes: cfg.trustedProxyPrefixes,
	})
	closeServer := func() error {
		return nil
	}
	if cfg.databaseURL != "" {
		postgresHandler, closePostgres, err := httpapi.NewPostgresServer(ctx, httpapi.Config{
			BaseURL:              cfg.baseURL,
			ControlBearer:        cfg.controlBearer,
			StorageRoot:          cfg.storageRoot,
			AccessLog:            cfg.accessLog,
			TrustedProxyPrefixes: cfg.trustedProxyPrefixes,
		}, cfg.databaseURL)
		if err != nil {
			return nil, nil, err
		}
		handler = postgresHandler
		closeServer = closePostgres
	}
	return &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       defaultIdleTimeout,
		MaxHeaderBytes:    defaultMaxHeaderBytes,
	}, closeServer, nil
}

func configFromEnv(getenv func(string) string) (config, error) {
	trustedProxyPrefixes := append([]netip.Prefix(nil), defaultTrustedProxyPrefixes...)
	if raw := strings.TrimSpace(getenv("GITRDONE_TRUSTED_PROXIES")); raw != "" {
		var err error
		trustedProxyPrefixes, err = parseTrustedProxyPrefixes(raw)
		if err != nil {
			return config{}, err
		}
	}
	cfg := config{
		addr:                 strings.TrimSpace(getenv("GITRDONE_ADDR")),
		baseURL:              strings.TrimSpace(getenv("GITRDONE_BASE_URL")),
		controlBearer:        strings.TrimSpace(getenv("GITRDONE_CONTROL_BEARER")),
		databaseURL:          strings.TrimSpace(getenv("GITRDONE_DATABASE_URL")),
		storageRoot:          strings.TrimSpace(getenv("GITRDONE_STORAGE_ROOT")),
		trustedProxyPrefixes: trustedProxyPrefixes,
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if cfg.baseURL == "" {
		cfg.baseURL = defaultBaseURL
	}
	if cfg.storageRoot == "" {
		cfg.storageRoot = defaultStorageRoot
	}
	if cfg.controlBearer == "" {
		return config{}, errors.New("GITRDONE_CONTROL_BEARER is required")
	}
	return cfg, nil
}

func parseTrustedProxyPrefixes(raw string) ([]netip.Prefix, error) {
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		prefix, err := parseTrustedProxyPrefix(part)
		if err != nil {
			return nil, fmt.Errorf("GITRDONE_TRUSTED_PROXIES contains invalid IP or CIDR %q", part)
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, errors.New("GITRDONE_TRUSTED_PROXIES must include at least one IP or CIDR")
	}
	return prefixes, nil
}

func parseTrustedProxyPrefix(value string) (netip.Prefix, error) {
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(value)
	if err == nil {
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	return netip.Prefix{}, errors.New("invalid trusted proxy")
}
