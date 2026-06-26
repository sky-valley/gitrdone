package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"

	httpapi "skyvalley.ac/m/v2/internal/http"
)

const (
	defaultAddr        = ":8080"
	defaultBaseURL     = "http://localhost:8080"
	defaultStorageRoot = ".storage"

	defaultReadHeaderTimeout = 5 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultMaxHeaderBytes    = 32 * 1024
	defaultShutdownTimeout   = 2 * time.Minute
	defaultSentryFlushTimeout = 2 * time.Second
)

var defaultTrustedProxyPrefixes = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.1/32"),
	netip.MustParsePrefix("::1/128"),
}

type config struct {
	addr                   string
	baseURL                string
	controlBearer          string
	storageRoot            string
	databaseURL            string
	trustedProxyPrefixes   []netip.Prefix
	shutdownTimeout        time.Duration
	accessLog              io.Writer
	sentryDSN              string
	sentryEnvironment      string
	sentryRelease          string
	sentryTracesSampleRate float64
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := configFromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	flushSentry, err := initSentry(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer flushSentry()
	cfg.accessLog = os.Stdout
	storageRoot, err := filepath.Abs(cfg.storageRoot)
	if err != nil {
		log.Fatal(err)
	}

	server, closeServer, err := newHTTPServer(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("gitrdone listening on %s, storage=%s", cfg.addr, storageRoot)
	if err := runServer(ctx, serverLifecycle{
		listenAndServe:  server.ListenAndServe,
		shutdown:        server.Shutdown,
		closeResources:  closeServer,
		shutdownTimeout: cfg.shutdownTimeout,
	}); err != nil {
		log.Fatal(err)
	}
}

type serverLifecycle struct {
	listenAndServe  func() error
	shutdown        func(context.Context) error
	closeResources  func() error
	shutdownTimeout time.Duration
}

func runServer(ctx context.Context, lifecycle serverLifecycle) error {
	serverErr := make(chan error, 1)
	go func() {
		err := lifecycle.listenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
	}()

	select {
	case err := <-serverErr:
		return errors.Join(err, closeResources(lifecycle.closeResources))
	case <-ctx.Done():
	}

	timeout := lifecycle.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	shutdownErr := lifecycle.shutdown(shutdownCtx)
	listenErr := <-serverErr
	return errors.Join(shutdownErr, listenErr, closeResources(lifecycle.closeResources))
}

func closeResources(closeFunc func() error) error {
	if closeFunc == nil {
		return nil
	}
	return closeFunc()
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
	if cfg.sentryDSN != "" {
		sentryHandler := sentryhttp.New(sentryhttp.Options{
			Repanic: true,
		})
		handler = sentryHandler.Handle(handler)
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
		sentryDSN:            strings.TrimSpace(getenv("SENTRY_DSN")),
		sentryEnvironment:    strings.TrimSpace(getenv("SENTRY_ENVIRONMENT")),
		sentryRelease:        strings.TrimSpace(getenv("SENTRY_RELEASE")),
		trustedProxyPrefixes: trustedProxyPrefixes,
		shutdownTimeout:      defaultShutdownTimeout,
	}
	if raw := strings.TrimSpace(getenv("GITRDONE_SHUTDOWN_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return config{}, fmt.Errorf("GITRDONE_SHUTDOWN_TIMEOUT must be a valid duration: %w", err)
		}
		if timeout <= 0 {
			return config{}, errors.New("GITRDONE_SHUTDOWN_TIMEOUT must be greater than zero")
		}
		cfg.shutdownTimeout = timeout
	}
	if raw := strings.TrimSpace(getenv("SENTRY_TRACES_SAMPLE_RATE")); raw != "" {
		rate, err := strconv.ParseFloat(raw, 64)
		if err != nil || rate < 0 || rate > 1 {
			return config{}, errors.New("SENTRY_TRACES_SAMPLE_RATE must be a number between 0 and 1")
		}
		cfg.sentryTracesSampleRate = rate
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

func initSentry(cfg config) (func(), error) {
	if cfg.sentryDSN == "" {
		return func() {}, nil
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.sentryDSN,
		Environment:      cfg.sentryEnvironment,
		Release:          cfg.sentryRelease,
		EnableTracing:    cfg.sentryTracesSampleRate > 0,
		TracesSampleRate: cfg.sentryTracesSampleRate,
		SendDefaultPII:   false,
		BeforeSend:       sanitizeSentryEvent,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Sentry: %w", err)
	}
	return func() {
		sentry.Flush(defaultSentryFlushTimeout)
	}, nil
}

func sanitizeSentryEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil || event.Request == nil {
		return event
	}
	event.Request.Headers = nil
	event.Request.Cookies = ""
	event.Request.Data = ""
	event.Request.QueryString = ""
	if event.Request.URL == "" {
		return event
	}
	parsed, err := url.Parse(event.Request.URL)
	if err != nil {
		urlWithoutQuery, _, ok := strings.Cut(event.Request.URL, "?")
		if ok {
			event.Request.URL = urlWithoutQuery
		}
		return event
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	event.Request.URL = parsed.String()
	return event
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
