package httpapi

import (
	"net/http"
	"strings"
)

type Handlers struct {
	AgentDocs       http.Handler
	Healthz         http.Handler
	CreateRepo      http.Handler
	GetRepo         http.Handler
	ArchiveRepo     http.Handler
	CreateRepoToken http.Handler
	ListRepoTokens  http.Handler
	RevokeRepoToken http.Handler
	GitSmartHTTP    http.Handler
}

func NewMux(h Handlers) *http.ServeMux {
	mux := http.NewServeMux()

	agentDocs := h.AgentDocs
	if agentDocs == nil {
		agentDocs = http.NotFoundHandler()
	}
	mux.Handle("GET /", agentDocs)
	mux.Handle("GET /llms.txt", agentDocs)
	mux.Handle("GET /.well-known/llms.txt", agentDocs)
	mux.Handle("GET /AGENTS.md", agentDocs)
	mux.Handle("GET /agents.md", agentDocs)
	mux.Handle("GET /.well-known/agents.md", agentDocs)
	mux.Handle("GET /llms-full.txt", agentDocs)
	mux.Handle("GET /robots.txt", agentDocs)
	mux.Handle("GET /sitemap.md", agentDocs)
	mux.Handle("GET /sitemap.xml", agentDocs)

	mux.Handle("GET /healthz", h.Healthz)

	mux.Handle("POST /v1/repos", h.CreateRepo)
	mux.Handle("GET /v1/repos/{repoID}", h.GetRepo)
	mux.Handle("POST /v1/repos/{repoID}/archive", h.ArchiveRepo)
	mux.Handle("POST /v1/repos/{repoID}/tokens", h.CreateRepoToken)
	mux.Handle("GET /v1/repos/{repoID}/tokens", h.ListRepoTokens)
	mux.Handle("POST /v1/repos/{repoID}/tokens/{tokenID}/revoke", h.RevokeRepoToken)

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
