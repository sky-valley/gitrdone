package httpapi

import "net/http"

type Config struct {
	BaseURL       string
	ControlBearer string
}

func NewServer(config Config) http.Handler {
	control := func(handler http.Handler) http.Handler {
		return controlAuth(config.ControlBearer, handler)
	}
	repos := newMemoryRepoStore(nil)

	return NewMux(Handlers{
		Healthz:         noContent(),
		CreateRepo:      control(createRepoHandler(repos, config.BaseURL)),
		GetRepo:         control(getRepoHandler(repos, config.BaseURL)),
		ArchiveRepo:     control(archiveRepoHandler(repos)),
		CreateRepoToken: control(internalServerError()),
		GitSmartHTTP:    internalServerError(),
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
