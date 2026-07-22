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

	agents := fmt.Sprintf(`# gitrdone Agent Guide

> gitrdone is a repository service where proposed changes become accepted intent through judgement, with authenticated Git smart HTTP and Git LFS compatibility.

Use this guide when acting as an automated agent against gitrdone. The native API deals in accepted intent, durable changes, and immutable change versions. Git is the first content engine and compatibility surface; it does not define the native repository model.

## Discovery

- Agent index: [%[1]s/llms.txt](%[1]s/llms.txt)
- Full agent guide: [%[1]s/AGENTS.md](%[1]s/AGENTS.md)
- Lowercase guide alias: [%[1]s/agents.md](%[1]s/agents.md)
- Well-known guide alias: [%[1]s/.well-known/agents.md](%[1]s/.well-known/agents.md)
- Markdown sitemap: [%[1]s/sitemap.md](%[1]s/sitemap.md)
- XML sitemap: [%[1]s/sitemap.xml](%[1]s/sitemap.xml)

## Authentication

Administrative control API endpoints require an internal control bearer token. Git and native proposal access use repo-scoped tokens minted by the control API. The control bearer remains accepted on native intent endpoints for trusted service callers.

Repo tokens are capability grants, not user identity sessions. The token subject is audit context supplied by the caller. Supported repo token scopes are:

- read: clone, fetch, pull, Git LFS downloads, read diff endpoints, read current intent, inspect changes and versions
- write: push, Git LFS uploads, propose content
- readwrite: all read and write capabilities; required by grd submit because submission reads current intent and publishes content

Normal Git clients should use Basic auth with username x-access-token and a repo token as the password. Do not persist repo tokens in remote URLs. Bearer auth is also accepted by Git and native intent routes for service callers.

## Native repository API

Read the repository's currently accepted content with GET /v1/repos/{repoID}/intent using a read or readwrite repo token, or the control bearer.

For a new repository only, publish the initial Git commit to a noncanonical ref, then establish root intent with PUT /v1/repos/{repoID}/intent using the control bearer token. Retrying the same content is idempotent. Different content is rejected after initialization.

Propose an immutable repository state with POST /v1/repos/{repoID}/proposals using a write or readwrite repo token, or the control bearer. The admitted version's producer comes from the authenticated repo-token subject and cannot be supplied in request JSON. Idempotency-Key is required for proposals and must identify the same logical proposal on every retry. The request body is:

`+"```json"+`
{
  "baseIntent": "intent_...",
  "contentRef": {
    "engine": "git",
    "revision": "<full commit object id>"
  },
  "dependencies": ["version_..."]
}
`+"```"+`

Dependencies are optional exact admitted version IDs. A dependent version may be admitted and inspected while its parents await promotion, but it cannot promote until those dependencies have promoted.

A successful proposal response means the change version was durably admitted. The response always identifies the change and version and reports state as admitted. It may also include a completed promotion when approve-all judgement finished during the request. Promotion is an opportunistic current result, not part of the admission guarantee. Absence of a promotion means judgement is pending; it does not assert a specific hold decision.

Inspect the durable identity with GET /v1/repos/{repoID}/changes/{changeID}. The summary reports latestAmendment and latestPromotion only when those outcomes exist. Read immutable versions with GET /v1/repos/{repoID}/changes/{changeID}/versions. The versions endpoint accepts limit from 1 to 100 and an opaque cursor returned by the previous page. Repository amendment is an internal judgement operation, not a public HTTP command.

## Reliable token creation

For POST /v1/repos/{repoID}/tokens, include Idempotency-Key when retrying or when the request is part of a durable workflow.

Use a key derived from the logical operation, not from a single HTTP attempt:

`+"```http"+`
Idempotency-Key: import:imp_123:source-read-token
Idempotency-Key: workflow:run_456:push-token
Idempotency-Key: release:rel_789:write-token
`+"```"+`

Reuse the same key only for the same repo, scope, subject, and TTL. If the same key is reused for a different token request, gitrdone returns 409 Conflict; do not blindly retry that conflict.

## Token lifecycle

List and revoke responses return token metadata only; token values are returned only at creation. Keep the token id returned by token creation if the workflow may need to revoke or audit that grant later.

Use GET /v1/repos/{repoID}/tokens to inspect token ids, scopes, subjects, expiration times, revocation times, and last-use times for a repo. Use POST /v1/repos/{repoID}/tokens/{tokenID}/revoke to revoke a repo token. Revocation is repo-scoped; do not try to revoke a token through a different repo.

Revoke tokens when a workflow is done or when a token may have leaked. If work must continue after revocation, mint a fresh token for the new logical operation.

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

Administrative control API for trusted services:

- POST /v1/repos
- GET /v1/repos/{repoID}
- POST /v1/repos/{repoID}/tokens
- GET /v1/repos/{repoID}/tokens
- POST /v1/repos/{repoID}/tokens/{tokenID}/revoke
- POST /v1/repos/{repoID}/archive
- PUT /v1/repos/{repoID}/intent

Native repository API:

- GET /v1/repos/{repoID}/intent
- POST /v1/repos/{repoID}/proposals
- GET /v1/repos/{repoID}/changes/{changeID}
- GET /v1/repos/{repoID}/changes/{changeID}/versions

Git smart HTTP adapter for normal Git clients:

- GET /git/repos/{repoID}.git/info/refs?service=git-upload-pack
- POST /git/repos/{repoID}.git/git-upload-pack
- GET /git/repos/{repoID}.git/info/refs?service=git-receive-pack
- POST /git/repos/{repoID}.git/git-receive-pack

Git LFS adapter for normal git-lfs clients:

- POST /git/repos/{repoID}.git/info/lfs/objects/batch
- PUT /git/repos/{repoID}.git/info/lfs/objects/{oid}
- GET /git/repos/{repoID}.git/info/lfs/objects/{oid}
- POST /git/repos/{repoID}.git/info/lfs/locks/verify

Git LFS lock verification returns an empty conflict set. gitrdone does not provide collaborative LFS locking.

Read-only Git adapter diff endpoints for service callers:

- GET /git/repos/{repoID}.git/show/{sha}.diff
- GET /git/repos/{repoID}.git/compare/{base}..{head}.diff
- GET /git/repos/{repoID}.git/compare/{base}...{head}.diff

Diff revisions must be lowercase hex object IDs, full or abbreviated from 7 to 64 characters. Diff responses larger than 8 MiB return 413 Request Entity Too Large.

## Git command use

The current Git adapter rejects external updates to the canonical branch. Publish immutable content to a noncanonical candidate ref, then use the native proposal API. Candidate ref names are temporary adapter storage, not change identity or holding state.

`+"```bash"+`
git clone <gitUrl>
git fetch
git pull
git push origin HEAD:refs/candidates/<temporary-name>
`+"```"+`

The canonical Git remote URL shape is:

`+"```text"+`
%[1]s/git/repos/{repoID}.git
`+"```"+`

repoID is the external control ID, for example repo_00000000-0000-4000-8000-000000000000.

## Do not hit

- Do not use namespace/name Git routes. They are not canonical.
- Do not scrape storage paths. Bare repo paths and LFS object paths are internal implementation details.
- Do not assume anonymous repo access. Git access requires repo tokens.
- Do not treat caller product concepts as gitrdone concepts. gitrdone does not know application workflows, tenants, or logged-in product users.
`, baseURL)

	llms := fmt.Sprintf(`# gitrdone

> gitrdone is a repository service where proposed changes become accepted intent through judgement, with Git smart HTTP and Git LFS compatibility.

gitrdone's native API exposes accepted intent, durable changes, and immutable change versions. Use canonical repo IDs, not namespace/name, as repository identity. Public discovery files are safe to scrape. Control, Git, and Git LFS operations require the appropriate tokens.

For retriable automation, include Idempotency-Key on token creation and derive it from the stable logical operation.

List and revoke repo tokens with the control bearer token; token values are returned only at creation.

Idempotency-Key is required on POST /v1/repos/{repoID}/proposals. A successful proposal response means the change version was durably admitted. Promotion may already be included, but is not part of the admission guarantee.

## Agent Docs

- [Agent guide](%[1]s/AGENTS.md): Full machine-readable guide for agents using this service.
- [Lowercase agent guide alias](%[1]s/agents.md): Same guide at a lowercase path.
- [Well-known agent guide alias](%[1]s/.well-known/agents.md): Same guide under .well-known.
- [Full LLM context](%[1]s/llms-full.txt): Same full guide for tools that look for llms-full.txt.
- [Markdown sitemap](%[1]s/sitemap.md): Agent-readable list of guide and service surfaces.

## Administrative API Surface

- [Health](%[1]s/healthz): Minimal service health check.
- [Create repo](%[1]s/v1/repos): POST with control bearer token.
- [Repo tokens](%[1]s/v1/repos/{repoID}/tokens): POST to create; GET to list metadata with control bearer token.
- [Revoke repo token](%[1]s/v1/repos/{repoID}/tokens/{tokenID}/revoke): POST with control bearer token.
- [Bootstrap intent](%[1]s/v1/repos/{repoID}/intent): PUT the first immutable content reference once with the control bearer token.

## Native Repository API Surface

- [Current intent](%[1]s/v1/repos/{repoID}/intent): GET the accepted repository content with a read/readwrite repo token or the control bearer.
- [Admit proposal](%[1]s/v1/repos/{repoID}/proposals): POST a base intent and immutable content reference with a write/readwrite repo token or the control bearer and Idempotency-Key.
- [Inspect change](%[1]s/v1/repos/{repoID}/changes/{changeID}): GET the durable change identity, latest version, and latest amendment or promotion outcomes when present.
- [List change versions](%[1]s/v1/repos/{repoID}/changes/{changeID}/versions): GET bounded immutable version history with cursor pagination.

## Git Adapter API Surface

- [Git smart HTTP and LFS](%[1]s/git/repos/{repoID}.git): Use normal Git commands with repo-scoped tokens.
- [Git diff endpoints](%[1]s/git/repos/{repoID}.git/show/{sha}.diff): Fetch read-scoped patch text without cloning.

## Optional

- [robots.txt](%[1]s/robots.txt)
- [XML sitemap](%[1]s/sitemap.xml)
`, baseURL)

	root := fmt.Sprintf(`# gitrdone

gitrdone makes judgement a native repository capability. Proposed change versions become accepted intent through explicit promotion, with authenticated Git smart HTTP and Git LFS as the first compatibility surface.

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

	sitemapMD := fmt.Sprintf(`# gitrdone sitemap

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
