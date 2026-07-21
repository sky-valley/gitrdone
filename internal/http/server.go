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
	control := func(handler http.Handler) http.Handler {
		return controlAuth(config.ControlBearer, handler)
	}
	if idempotency == nil {
		idempotency = newMemoryIdempotencyStore(nil)
	}
	intentRepositories := newIntentRepositoryRegistry(config.StorageRoot, repos, gitStorage)
	intentHandlers := intentapi.NewHandlers(intentservice.New(intentRepositories, "control-api"))

	mux := NewMux(Handlers{
		AgentDocs:       agentDocsHandler(config.BaseURL),
		Healthz:         noContent(),
		CreateRepo:      control(createRepoHandler(repos, config.BaseURL)),
		GetRepo:         control(getRepoHandler(repos, config.BaseURL)),
		ArchiveRepo:     control(archiveRepoHandler(repos)),
		CreateRepoToken: control(createRepoTokenHandler(repos, idempotency, config.BaseURL)),
		ListRepoTokens:  control(listRepoTokensHandler(repos)),
		RevokeRepoToken: control(revokeRepoTokenHandler(repos)),
		CurrentIntent:   control(intentHandlers.CurrentIntent),
		ProposeIntent:   control(intentHandlers.Propose),
		GetChange:       control(intentHandlers.GetChange),
		ListVersions:    control(intentHandlers.ListVersions),
		GitSmartHTTP:    gitSmartHTTPHandler(repos, execGitHTTPBackend{}),
		GitLFS:          gitLFSHandler(repos, newFilesystemLFSObjectStore(config.StorageRoot), config.MaxLFSObjectBytes),
		GitShowDiff:     gitDiffHandler(repos, execGitDiffBackend{}, gitDiffShow),
		GitCompareDiff:  gitDiffHandler(repos, execGitDiffBackend{}, gitDiffCompare),
	})
	return accessLog(config.AccessLog, config.TrustedProxyPrefixes, mux), func() error {
		return intentRepositories.Close()
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
