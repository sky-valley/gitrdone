package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	httpapi "skyvalley.ac/m/v2/internal/http"
)

const (
	defaultAddr    = ":8080"
	defaultBaseURL = "http://localhost:8080"
)

type config struct {
	addr          string
	baseURL       string
	controlBearer string
}

func main() {
	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	server := &http.Server{
		Addr: cfg.addr,
		Handler: httpapi.NewServer(httpapi.Config{
			BaseURL:       cfg.baseURL,
			ControlBearer: cfg.controlBearer,
		}),
	}

	log.Printf("giterdone listening on %s", cfg.addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func configFromEnv(getenv func(string) string) (config, error) {
	cfg := config{
		addr:          strings.TrimSpace(getenv("GITERDONE_ADDR")),
		baseURL:       strings.TrimSpace(getenv("GITERDONE_BASE_URL")),
		controlBearer: strings.TrimSpace(getenv("GITERDONE_CONTROL_BEARER")),
	}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if cfg.baseURL == "" {
		cfg.baseURL = defaultBaseURL
	}
	if cfg.controlBearer == "" {
		return config{}, errors.New("GITERDONE_CONTROL_BEARER is required")
	}
	return cfg, nil
}
