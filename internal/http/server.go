package httpapi

import (
	"io"
	"net/http"
	"net/netip"

	"github.com/sky-valley/gitrdone/internal/intentapi"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

type Config struct {
	BaseURL              string
	ControlBearer        string
	StorageRoot          string
	PromotionDecider     intentservice.PromotionDecider
	MaxLFSObjectBytes    int64
	AccessLog            io.Writer
	TrustedProxyPrefixes []netip.Prefix
}

func NewServer(config Config) http.Handler {
	handler, _ := NewServerWithClose(config)
	return handler
}

func NewServerWithClose(config Config) (http.Handler, func() error) {
	repos := newMemoryRepoStore(nil)
	gitStorage := newFilesystemGitStorage(config.StorageRoot)
	repos.gitStorage = gitStorage
	return newServerWithStores(config, repos, newMemoryIdempotencyStore(nil), gitStorage)
}

func NewServerWithStores(config Config, repos repoStore, idempotency idempotencyDoer) http.Handler {
	handler, _ := newServerWithStores(config, repos, idempotency, newFilesystemGitStorage(config.StorageRoot))
	return handler
}

func newServerWithStores(config Config, repos repoStore, idempotency idempotencyDoer, gitStorage repoGitStorage) (http.Handler, func() error) {
	resources := buildServer(config, repos, idempotency, gitStorage)
	return resources.handler, resources.close
}

type serverResources struct {
	handler   http.Handler
	judgement *intentservice.Service
	close     func() error
}

func buildServer(config Config, repos repoStore, idempotency idempotencyDoer, gitStorage repoGitStorage) serverResources {
	control := func(handler http.Handler) http.Handler {
		return controlAuth(config.ControlBearer, handler)
	}
	if idempotency == nil {
		idempotency = newMemoryIdempotencyStore(nil)
	}
	intentRepositories := newIntentRepositoryRegistry(config.StorageRoot, repos, gitStorage)
	judgement := intentservice.NewWithPromotionDecider(intentRepositories, config.PromotionDecider)
	intentHandlers := intentapi.NewHandlers(judgement)

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
		judgement: judgement,
		close:     intentRepositories.Close,
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
