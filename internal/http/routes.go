package httpapi

import (
	"net/http"
	"strings"
)

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

	gitSmartHTTP := gitRepoRoute(h.GitSmartHTTP)
	mux.Handle("GET /git/repos/{repoGitID}/{gitPath...}", gitSmartHTTP)
	mux.Handle("POST /git/repos/{repoGitID}/{gitPath...}", gitSmartHTTP)

	return mux
}

func gitRepoRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoID, ok := strings.CutSuffix(r.PathValue("repoGitID"), ".git")
		if !ok || repoID == "" {
			http.NotFound(w, r)
			return
		}
		r.SetPathValue("repoID", repoID)
		next.ServeHTTP(w, r)
	})
}
