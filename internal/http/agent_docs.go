package httpapi

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

func agentDocsHandler(baseURL string) http.Handler {
	docs := newAgentDocs(baseURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeText(w, http.StatusOK, "text/markdown; charset=utf-8", docs.root)
		case "/llms.txt", "/.well-known/llms.txt":
			writeText(w, http.StatusOK, "text/plain; charset=utf-8", docs.llms)
		case "/AGENTS.md", "/agents.md", "/.well-known/agents.md", "/llms-full.txt":
			writeText(w, http.StatusOK, "text/markdown; charset=utf-8", docs.agents)
		case "/robots.txt":
			writeText(w, http.StatusOK, "text/plain; charset=utf-8", docs.robots)
		case "/sitemap.md":
			writeText(w, http.StatusOK, "text/markdown; charset=utf-8", docs.sitemapMD)
		case "/sitemap.xml":
			writeText(w, http.StatusOK, "application/xml; charset=utf-8", docs.sitemapXML)
		default:
			http.NotFound(w, r)
		}
	})
}

type agentDocs struct {
	root       string
	llms       string
	agents     string
	robots     string
	sitemapMD  string
	sitemapXML string
}

func newAgentDocs(baseURL string) agentDocs {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}

	agents := fmt.Sprintf(`# giterdone Agent Guide

> giterdone is an authenticated Git smart HTTP service with a small control API for creating backing Git repos and repo-scoped access tokens.

Use this guide when acting as an automated agent against giterdone. giterdone is intentionally lower-level than Differ: it serves Git repos, tokens, and Git smart HTTP. Product concepts such as stems, divergences, adaptations, and app ownership belong in Differ.

## Discovery

- Agent index: [%[1]s/llms.txt](%[1]s/llms.txt)
- Full agent guide: [%[1]s/AGENTS.md](%[1]s/AGENTS.md)
- Lowercase guide alias: [%[1]s/agents.md](%[1]s/agents.md)
- Well-known guide alias: [%[1]s/.well-known/agents.md](%[1]s/.well-known/agents.md)
- Markdown sitemap: [%[1]s/sitemap.md](%[1]s/sitemap.md)
- XML sitemap: [%[1]s/sitemap.xml](%[1]s/sitemap.xml)

## Authentication

Control API endpoints require an internal control bearer token. Git endpoints require repo-scoped tokens minted by the control API.

Repo tokens are capability grants, not user identity sessions. The token subject is audit context supplied by the caller. Supported repo token scopes are:

- read: clone, fetch, pull
- write: push
- readwrite: clone, fetch, pull, push

Normal Git clients should use Basic auth with username x-access-token and a repo token as the password. Do not persist repo tokens in remote URLs. Bearer auth is also accepted by Git routes for service callers.

## Reliable token creation

For POST /v1/repos/{repoID}/tokens, include Idempotency-Key when retrying or when the request is part of a durable workflow.

Use a key derived from the logical operation, not from a single HTTP attempt:

`+"```http"+`
Idempotency-Key: differ:import:imp_123:source-read-token
Idempotency-Key: differ:divergence:div_456:push-token
Idempotency-Key: differ:adaptation-run:run_789:stem-write-token
`+"```"+`

Reuse the same key only for the same repo, scope, subject, and TTL. If the same key is reused for a different token request, giterdone returns 409 Conflict; do not blindly retry that conflict.

## Agent-safe surfaces

Public discovery:

- GET /
- GET /llms.txt
- GET /.well-known/llms.txt
- GET /AGENTS.md
- GET /agents.md
- GET /.well-known/agents.md
- GET /llms-full.txt
- GET /robots.txt
- GET /sitemap.md
- GET /sitemap.xml
- GET /healthz

Control API for trusted Differ services:

- POST /v1/repos
- GET /v1/repos/{repoID}
- POST /v1/repos/{repoID}/tokens
- POST /v1/repos/{repoID}/archive

Git smart HTTP for normal Git clients:

- GET /git/repos/{repoID}.git/info/refs?service=git-upload-pack
- POST /git/repos/{repoID}.git/git-upload-pack
- GET /git/repos/{repoID}.git/info/refs?service=git-receive-pack
- POST /git/repos/{repoID}.git/git-receive-pack

## Git command use

Prefer ordinary Git commands over handcrafted protocol requests:

`+"```bash"+`
git clone <gitUrl>
git fetch
git pull
git push origin main
`+"```"+`

The canonical Git remote URL shape is:

`+"```text"+`
%[1]s/git/repos/{repoID}.git
`+"```"+`

repoID is the external control ID, for example repo_00000000-0000-4000-8000-000000000000.

## Do not hit

- Do not use namespace/name Git routes. They are not canonical.
- Do not scrape storage paths. Bare repo paths are internal implementation details.
- Do not assume anonymous repo access. Git access requires repo tokens.
- Do not treat Differ concepts as giterdone concepts. giterdone does not know stems, divergences, adaptations, apps, or logged-in product users.
`, baseURL)

	llms := fmt.Sprintf(`# giterdone

> giterdone is an authenticated Git smart HTTP service with a small control API for creating backing Git repos and repo-scoped access tokens.

giterdone is intended to be used by Differ services and trusted agents as a Git backend. Use canonical repo IDs, not namespace/name, as the Git identity. Public discovery files are safe to scrape. Control and Git operations require the appropriate tokens.

For retriable automation, include Idempotency-Key on token creation and derive it from the stable logical operation.

## Agent Docs

- [Agent guide](%[1]s/AGENTS.md): Full machine-readable guide for agents using this service.
- [Lowercase agent guide alias](%[1]s/agents.md): Same guide at a lowercase path.
- [Well-known agent guide alias](%[1]s/.well-known/agents.md): Same guide under .well-known.
- [Full LLM context](%[1]s/llms-full.txt): Same full guide for tools that look for llms-full.txt.
- [Markdown sitemap](%[1]s/sitemap.md): Agent-readable list of guide and service surfaces.

## API Surface

- [Health](%[1]s/healthz): Minimal service health check.
- [Create repo](%[1]s/v1/repos): POST with control bearer token.
- [Repo tokens](%[1]s/v1/repos/{repoID}/tokens): POST with control bearer token.
- [Git smart HTTP](%[1]s/git/repos/{repoID}.git): Use normal Git commands with repo-scoped tokens.

## Optional

- [robots.txt](%[1]s/robots.txt)
- [XML sitemap](%[1]s/sitemap.xml)
`, baseURL)

	root := fmt.Sprintf(`# giterdone

giterdone serves authenticated Git smart HTTP for repo-ID-addressed backing repos.

Agent entrypoints:

- [/llms.txt](%[1]s/llms.txt)
- [/AGENTS.md](%[1]s/AGENTS.md)
- [/agents.md](%[1]s/agents.md)
- [/.well-known/agents.md](%[1]s/.well-known/agents.md)
- [/sitemap.md](%[1]s/sitemap.md)

Canonical Git remote shape:

`+"```text"+`
%[1]s/git/repos/{repoID}.git
`+"```"+`

Use control API tokens for /v1 routes and repo-scoped tokens for Git routes.
`, baseURL)

	robots := fmt.Sprintf(`User-agent: *
Allow: /
Allow: /llms.txt
Allow: /AGENTS.md
Allow: /agents.md
Allow: /.well-known/agents.md
Allow: /sitemap.md

Sitemap: %[1]s/sitemap.xml
`, baseURL)

	sitemapMD := fmt.Sprintf(`# giterdone sitemap

- [Root](%[1]s/)
- [llms.txt](%[1]s/llms.txt)
- [Agent guide](%[1]s/AGENTS.md)
- [Lowercase agent guide](%[1]s/agents.md)
- [Well-known agent guide](%[1]s/.well-known/agents.md)
- [Full LLM context](%[1]s/llms-full.txt)
- [Robots](%[1]s/robots.txt)
- [Health](%[1]s/healthz)
`, baseURL)

	sitemapXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%[1]s/</loc></url>
  <url><loc>%[1]s/llms.txt</loc></url>
  <url><loc>%[1]s/AGENTS.md</loc></url>
  <url><loc>%[1]s/agents.md</loc></url>
  <url><loc>%[1]s/.well-known/agents.md</loc></url>
  <url><loc>%[1]s/llms-full.txt</loc></url>
  <url><loc>%[1]s/robots.txt</loc></url>
  <url><loc>%[1]s/sitemap.md</loc></url>
  <url><loc>%[1]s/healthz</loc></url>
</urlset>
`, html.EscapeString(baseURL))

	return agentDocs{
		root:       root,
		llms:       llms,
		agents:     agents,
		robots:     robots,
		sitemapMD:  sitemapMD,
		sitemapXML: sitemapXML,
	}
}

func writeText(w http.ResponseWriter, status int, contentType string, value string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(value))
}
