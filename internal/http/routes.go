package httpapi

import "net/http"

type Handlers struct {
	Healthz         http.Handler
	CreateRepo      http.Handler
	GetRepo         http.Handler
	ArchiveRepo     http.Handler
	CreateRepoToken http.Handler
	GitSmartHTTP    http.Handler
}

func NewMux(h Handlers) *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", h.Healthz)

	mux.Handle("POST /v1/repos", h.CreateRepo)
	mux.Handle("GET /v1/repos/{repoID}", h.GetRepo)
	mux.Handle("POST /v1/repos/{repoID}/archive", h.ArchiveRepo)
	mux.Handle("POST /v1/repos/{repoID}/tokens", h.CreateRepoToken)

	mux.Handle("GET /{namespace}/{gitPath...}", h.GitSmartHTTP)
	mux.Handle("POST /{namespace}/{gitPath...}", h.GitSmartHTTP)

	return mux
}
