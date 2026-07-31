package httpapi

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/netip"
	"time"

	"github.com/sky-valley/gitrdone/internal/intentapi"
	"github.com/sky-valley/gitrdone/internal/intentservice"
	judgementrunner "github.com/sky-valley/gitrdone/internal/judgement"
)

type Config struct {
	BaseURL              string
	ControlBearer        string
	StorageRoot          string
	MaxLFSObjectBytes    int64
	AccessLog            io.Writer
	TrustedProxyPrefixes []netip.Prefix
}

type PendingRuntimeConfig struct {
	Workers int
}

func NewServer(config Config) http.Handler {
	handler, _ := NewServerWithClose(config)
	return handler
}

func NewServerWithClose(config Config) (http.Handler, func() error) {
	return NewServerWithPendingRunner(config, PendingRuntimeConfig{})
}

// NewServerWithPendingRunner returns both the handler and ownership of the
// background pending-work runtime. Callers must invoke the returned closer.
func NewServerWithPendingRunner(config Config, runtime PendingRuntimeConfig) (http.Handler, func() error) {
	repos := newMemoryRepoStore(nil)
	gitStorage := newFilesystemGitStorage(config.StorageRoot)
	repos.gitStorage = gitStorage
	return newServerWithStores(config, runtime, repos, newMemoryIdempotencyStore(nil), gitStorage)
}

func NewServerWithStores(config Config, repos repoStore, idempotency idempotencyDoer) http.Handler {
	handler, _ := newServerWithStores(config, PendingRuntimeConfig{}, repos, idempotency, newFilesystemGitStorage(config.StorageRoot))
	return handler
}

func newServerWithStores(config Config, runtime PendingRuntimeConfig, repos repoStore, idempotency idempotencyDoer, gitStorage repoGitStorage) (http.Handler, func() error) {
	resources := buildServer(config, runtime, repos, idempotency, gitStorage)
	return resources.handler, resources.close
}

type serverResources struct {
	handler   http.Handler
	judgement *intentservice.Service
	close     func() error
}

func buildServer(config Config, runtime PendingRuntimeConfig, repos repoStore, idempotency idempotencyDoer, gitStorage repoGitStorage) serverResources {
	control := func(handler http.Handler) http.Handler {
		return controlAuth(config.ControlBearer, handler)
	}
	if idempotency == nil {
		idempotency = newMemoryIdempotencyStore(nil)
	}
	intentRepositories := newIntentRepositoryRegistry(config.StorageRoot, repos, gitStorage)
	intentService := intentservice.New(intentRepositories)
	intentHandlers := intentapi.NewHandlers(intentService)
	closeRunner := func() error { return nil }
	if runtime.Workers > 0 {
		runner := judgementrunner.NewPendingRunner(
			newFilesystemPendingSource(intentRepositories),
			judgementrunner.NewMemoryLeases(nil),
			judgementrunner.NewApproveAllProcessor(intentService),
			judgementrunner.RunnerOptions{
				Workers:      runtime.Workers,
				BatchSize:    runtime.Workers,
				PollInterval: 100 * time.Millisecond,
				LeaseTTL:     5 * time.Minute,
				Report: func(err error) {
					log.Printf("gitrdone pending runner: %v", err)
				},
			},
		)
		runnerCtx, stopRunner := context.WithCancel(context.Background())
		runnerDone := make(chan struct{})
		go func() {
			defer close(runnerDone)
			runner.Run(runnerCtx)
		}()
		closeRunner = func() error {
			stopRunner()
			<-runnerDone
			return nil
		}
	}

	mux := NewMux(Handlers{
		AgentDocs:                    agentDocsHandler(config.BaseURL),
		Healthz:                      noContent(),
		CreateRepo:                   control(createRepoHandler(repos, config.BaseURL)),
		GetRepo:                      control(getRepoHandler(repos, config.BaseURL)),
		ArchiveRepo:                  control(archiveRepoHandler(repos)),
		CreateRepoToken:              control(createRepoTokenHandler(repos, idempotency, config.BaseURL)),
		ListRepoTokens:               control(listRepoTokensHandler(repos)),
		RevokeRepoToken:              control(revokeRepoTokenHandler(repos)),
		CurrentIntent:                repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityInspect, intentHandlers.CurrentIntent),
		BootstrapIntent:              control(intentHandlers.Bootstrap),
		AdmitProposal:                repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityPropose, intentHandlers.AdmitProposal),
		RecordReconciliationConflict: repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityPropose, intentHandlers.RecordReconciliationConflict),
		ListReconciliationConflicts:  repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityInspect, intentHandlers.ListReconciliationConflicts),
		GetReconciliationConflict:    repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityInspect, intentHandlers.GetReconciliationConflict),
		GetChange:                    repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityInspect, intentHandlers.GetChange),
		ListVersions:                 repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityInspect, intentHandlers.ListVersions),
		GitSmartHTTP:                 gitSmartHTTPHandler(repos, execGitHTTPBackend{}),
		GitLFS:                       gitLFSHandler(repos, newFilesystemLFSObjectStore(config.StorageRoot), config.MaxLFSObjectBytes),
		GitShowDiff:                  gitDiffHandler(repos, execGitDiffBackend{}, gitDiffShow),
		GitCompareDiff:               gitDiffHandler(repos, execGitDiffBackend{}, gitDiffCompare),
	})
	return serverResources{
		handler:   accessLog(config.AccessLog, config.TrustedProxyPrefixes, mux),
		judgement: intentService,
		close: func() error {
			return errors.Join(closeRunner(), intentRepositories.Close())
		},
	}
}

func internalServerError() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	})
}

func noContent() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}
