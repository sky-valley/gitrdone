package httpapi

import (
	"io"
	"net/http"
	"net/netip"
)

type Config struct {
	BaseURL              string
	ControlBearer        string
	StorageRoot          string
	AccessLog            io.Writer
	TrustedProxyPrefixes []netip.Prefix
}

func NewServer(config Config) http.Handler {
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = newFilesystemGitStorage(config.StorageRoot)
	return NewServerWithStores(config, repos, newMemoryIdempotencyStore(nil))
}

func NewServerWithStores(config Config, repos repoStore, idempotency idempotencyDoer) http.Handler {
	control := func(handler http.Handler) http.Handler {
		return controlAuth(config.ControlBearer, handler)
	}
	if idempotency == nil {
		idempotency = newMemoryIdempotencyStore(nil)
	}

	mux := NewMux(Handlers{
		AgentDocs:       agentDocsHandler(config.BaseURL),
		Healthz:         noContent(),
		CreateRepo:      control(createRepoHandler(repos, config.BaseURL)),
		GetRepo:         control(getRepoHandler(repos, config.BaseURL)),
		ArchiveRepo:     control(archiveRepoHandler(repos)),
		CreateRepoToken: control(createRepoTokenHandler(repos, idempotency, config.BaseURL)),
		ListRepoTokens:  control(listRepoTokensHandler(repos)),
		RevokeRepoToken: control(revokeRepoTokenHandler(repos)),
		GitSmartHTTP:    gitSmartHTTPHandler(repos, execGitHTTPBackend{}),
	})
	return accessLog(config.AccessLog, config.TrustedProxyPrefixes, mux)
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
