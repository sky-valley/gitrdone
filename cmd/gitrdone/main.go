package main

import (
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
}

func main() {
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	storageRoot, err := filepath.Abs(cfg.storageRoot)
	if err != nil {
		log.Fatal(err)
	}

	server := newHTTPServer(cfg)

	log.Printf("gitrdone listening on %s, storage=%s", cfg.addr, storageRoot)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func newHTTPServer(cfg config) *http.Server {
	return &http.Server{
		Addr: cfg.addr,
		Handler: httpapi.NewServer(httpapi.Config{
			BaseURL:       cfg.baseURL,
			ControlBearer: cfg.controlBearer,
			StorageRoot:   cfg.storageRoot,
		}),
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       defaultIdleTimeout,
		MaxHeaderBytes:    defaultMaxHeaderBytes,
	}
}

func configFromEnv(getenv func(string) string) (config, error) {
	cfg := config{
		addr:          strings.TrimSpace(getenv("GITRDONE_ADDR")),
		baseURL:       strings.TrimSpace(getenv("GITRDONE_BASE_URL")),
		controlBearer: strings.TrimSpace(getenv("GITRDONE_CONTROL_BEARER")),
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
