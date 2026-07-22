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
	CurrentIntent   http.Handler
	BootstrapIntent http.Handler
	AdmitProposal   http.Handler
	GetChange       http.Handler
	ListVersions    http.Handler
	GitSmartHTTP    http.Handler
	GitLFS          http.Handler
	GitShowDiff     http.Handler
	GitCompareDiff  http.Handler
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

	currentIntent := handlerOrNotFound(h.CurrentIntent)
	admitProposal := handlerOrNotFound(h.AdmitProposal)
	getChange := handlerOrNotFound(h.GetChange)
	listVersions := handlerOrNotFound(h.ListVersions)
	mux.Handle("GET /v1/repos/{repoID}/intent", currentIntent)
	mux.Handle("PUT /v1/repos/{repoID}/intent", handlerOrNotFound(h.BootstrapIntent))
	mux.Handle("POST /v1/repos/{repoID}/proposals", admitProposal)
	mux.Handle("GET /v1/repos/{repoID}/changes/{changeID}", getChange)
	mux.Handle("GET /v1/repos/{repoID}/changes/{changeID}/versions", listVersions)

	gitSmartHTTP := gitRepoRoute(h.GitSmartHTTP)
	mux.Handle("GET /git/repos/{repoGitID}/{gitPath...}", gitSmartHTTP)
	mux.Handle("POST /git/repos/{repoGitID}/{gitPath...}", gitSmartHTTP)

	gitLFS := gitRepoRoute(h.GitLFS)
	mux.Handle("POST /git/repos/{repoGitID}/info/lfs/objects/batch", gitLFS)
	mux.Handle("PUT /git/repos/{repoGitID}/info/lfs/objects/{oid}", gitLFS)
	mux.Handle("GET /git/repos/{repoGitID}/info/lfs/objects/{oid}", gitLFS)
	mux.Handle("POST /git/repos/{repoGitID}/info/lfs/locks/verify", gitLFS)

	// More specific than the {gitPath...} catch-all above, so Go's ServeMux
	// routes these here rather than into the git smart-HTTP transport.
	mux.Handle("GET /git/repos/{repoGitID}/show/{spec}", gitRepoRoute(h.GitShowDiff))
	mux.Handle("GET /git/repos/{repoGitID}/compare/{spec}", gitRepoRoute(h.GitCompareDiff))

	return mux
}

func handlerOrNotFound(handler http.Handler) http.Handler {
	if handler == nil {
		return http.NotFoundHandler()
	}
	return handler
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
