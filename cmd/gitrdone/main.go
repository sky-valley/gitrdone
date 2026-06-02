package main

import (
	"context"
	"errors"
	"log"
	"net/http"
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

type config struct {
	addr          string
	baseURL       string
	controlBearer string
	storageRoot   string
	databaseURL   string
}

func main() {
	ctx := context.Background()
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
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
		BaseURL:       cfg.baseURL,
		ControlBearer: cfg.controlBearer,
		StorageRoot:   cfg.storageRoot,
	})
	closeServer := func() error {
		return nil
	}
	if cfg.databaseURL != "" {
		postgresHandler, closePostgres, err := httpapi.NewPostgresServer(ctx, httpapi.Config{
			BaseURL:       cfg.baseURL,
			ControlBearer: cfg.controlBearer,
			StorageRoot:   cfg.storageRoot,
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
	cfg := config{
		addr:          strings.TrimSpace(getenv("GITRDONE_ADDR")),
		baseURL:       strings.TrimSpace(getenv("GITRDONE_BASE_URL")),
		controlBearer: strings.TrimSpace(getenv("GITRDONE_CONTROL_BEARER")),
		databaseURL:   strings.TrimSpace(getenv("GITRDONE_DATABASE_URL")),
		storageRoot:   strings.TrimSpace(getenv("GITRDONE_STORAGE_ROOT")),
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
