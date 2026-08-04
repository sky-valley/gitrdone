package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"time"

	"github.com/sky-valley/gitrdone/internal/gitcontent"
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
	Workers          int
	ProcessorFactory judgementrunner.PendingProcessorFactory
}

func NewServer(config Config) http.Handler {
	handler, _ := NewServerWithClose(config)
	return handler
}

func NewServerWithClose(config Config) (http.Handler, func() error) {
	handler, closeServer, err := NewServerWithPendingRunner(config, PendingRuntimeConfig{})
	if err != nil {
		return internalServerError(), func() error { return err }
	}
	return handler, closeServer
}

// NewServerWithPendingRunner returns both the handler and ownership of the
// background pending-work runtime. Callers must invoke the returned closer.
func NewServerWithPendingRunner(config Config, runtime PendingRuntimeConfig) (http.Handler, func() error, error) {
	repos := newMemoryRepoStore(nil)
	gitStorage := newFilesystemGitStorage(config.StorageRoot)
	repos.gitStorage = gitStorage
	return newServerWithStores(config, runtime, repos, newMemoryIdempotencyStore(nil), gitStorage)
}

func NewServerWithStores(config Config, repos repoStore, idempotency idempotencyDoer) http.Handler {
	handler, _, err := newServerWithStores(config, PendingRuntimeConfig{}, repos, idempotency, newFilesystemGitStorage(config.StorageRoot))
	if err != nil {
		return internalServerError()
	}
	return handler
}

func newServerWithStores(config Config, runtime PendingRuntimeConfig, repos repoStore, idempotency idempotencyDoer, gitStorage repoGitStorage) (http.Handler, func() error, error) {
	resources, err := buildServer(config, runtime, repos, idempotency, gitStorage)
	if err != nil {
		return nil, nil, err
	}
	return resources.handler, resources.close, nil
}

type serverResources struct {
	handler   http.Handler
	judgement *intentservice.Service
	close     func() error
}

func buildServer(config Config, runtime PendingRuntimeConfig, repos repoStore, idempotency idempotencyDoer, gitStorage repoGitStorage) (serverResources, error) {
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
		if runtime.ProcessorFactory == nil {
			_ = intentRepositories.Close()
			return serverResources{}, errors.New("judgement workers require an explicit processor factory")
		}
		contentSource, err := gitcontent.NewSource(gitContentLocator{storage: gitStorage})
		if err != nil {
			_ = intentRepositories.Close()
			return serverResources{}, fmt.Errorf("create repository content source: %w", err)
		}
		processor, err := runtime.ProcessorFactory.Build(intentService, contentSource)
		if err != nil {
			_ = intentRepositories.Close()
			return serverResources{}, fmt.Errorf("create pending judgement processor: %w", err)
		}
		runner := judgementrunner.NewPendingRunner(
			newFilesystemPendingSource(intentRepositories),
			judgementrunner.NewMemoryLeases(nil),
			processor,
			judgementrunner.RunnerOptions{
				Workers:      runtime.Workers,
				BatchSize:    runtime.Workers,
				PollInterval: time.Second,
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
		ListReviews:                  repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityReview, intentHandlers.ListReviews),
		RecordReviewResponse:         repositoryAccessAuth(config.ControlBearer, repos, repoCapabilityReview, intentHandlers.RecordReviewResponse),
		GitSmartHTTP:                 gitSmartHTTPHandler(repos, execGitHTTPBackend{}),
		GitLFS:                       gitLFSHandler(repos, newFilesystemLFSObjectStore(config.StorageRoot), config.MaxLFSObjectBytes),
		GitShowDiff:                  gitDiffHandler(repos, execGitDiffBackend{}, gitDiffShow),
		GitCompareDiff:               gitDiffHandler(repos, execGitDiffBackend{}, gitDiffCompare),
		GitRawFile:                   gitRawHandler(repos, execGitRawBackend{}),
	})
	return serverResources{
		handler:   accessLog(config.AccessLog, config.TrustedProxyPrefixes, mux),
		judgement: intentService,
		close: func() error {
			return errors.Join(closeRunner(), intentRepositories.Close())
		},
	}, nil
}

type gitContentLocator struct {
	storage repoGitStorage
}

func (locator gitContentLocator) BareRepoPath(ctx context.Context, repoID string) (string, error) {
	rawID, ok := parseRepoControlID(repoID)
	if !ok {
		return "", errors.New("repository id is invalid")
	}
	return locator.storage.BareRepoPath(ctx, rawID)
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
