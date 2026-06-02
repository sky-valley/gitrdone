package httpapi

import "net/http"

type Config struct {
	BaseURL       string
	ControlBearer string
	StorageRoot   string
}

func NewServer(config Config) http.Handler {
	control := func(handler http.Handler) http.Handler {
		return controlAuth(config.ControlBearer, handler)
	}
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = newFilesystemGitStorage(config.StorageRoot)

	return NewMux(Handlers{
		AgentDocs:       agentDocsHandler(config.BaseURL),
		Healthz:         noContent(),
		CreateRepo:      control(createRepoHandler(repos, config.BaseURL)),
		GetRepo:         control(getRepoHandler(repos, config.BaseURL)),
		ArchiveRepo:     control(archiveRepoHandler(repos)),
		CreateRepoToken: control(createRepoTokenHandler(repos, config.BaseURL)),
		GitSmartHTTP:    gitSmartHTTPHandler(repos, execGitHTTPBackend{}),
	})
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
