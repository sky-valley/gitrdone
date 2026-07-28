# gitrdone

The current implementation is authenticated Git smart HTTP with a small control API for repo-backed workflows.

That implementation is intentionally narrow: it creates backing Git repos, mints repo-scoped access tokens, and serves those repos over normal Git HTTP routes, including the Git LFS Batch and Basic Transfer APIs. Higher-level product concepts do not belong in this temporary HTTP artifact service.

This README describes the current implementation, which is disposable scaffolding rather than the product boundary. The intended product is a modular repository system with judgement as a native capability. Read the canonical [product vision](docs/vision.md) before architecture work; the [original handoff](docs/vision-source-handoff.md) is retained verbatim as historical context.

## Requirements

- Go 1.26.3 or newer
- `git` available on `PATH`
- `git-lfs` available on `PATH` for the real Git LFS integration test

## Run Locally

```bash
GITRDONE_CONTROL_BEARER=dev-control-token go run ./cmd/gitrdone
```

Defaults:

| Variable | Default | Purpose |
| --- | --- | --- |
| `GITRDONE_ADDR` | `:8080` | HTTP listen address |
| `GITRDONE_BASE_URL` | `http://localhost:8080` | Base URL used in API responses and docs |
| `GITRDONE_STORAGE_ROOT` | `.storage` | Filesystem root for bare Git repos |
| `GITRDONE_MAX_LFS_OBJECT_BYTES` | `5368709120` | Maximum accepted Git LFS object upload size in bytes |
| `GITRDONE_DATABASE_URL` | unset | Postgres URL for durable control metadata |
| `GITRDONE_CONTROL_BEARER` | required | Bearer token for `/v1` control routes |
| `GITRDONE_TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | Comma-separated proxy IPs/CIDRs whose forwarded headers may identify the client |
| `GITRDONE_SHUTDOWN_TIMEOUT` | `2m` | Maximum time to wait for graceful shutdown after `SIGINT` or `SIGTERM` |
| `SENTRY_DSN` | unset | Sentry project DSN; unset disables Sentry reporting |
| `SENTRY_ENVIRONMENT` | unset | Sentry environment name, for example `dev` or `main` |
| `SENTRY_RELEASE` | unset | Sentry release identifier, normally the deployed Git SHA |
| `SENTRY_TRACES_SAMPLE_RATE` | `0` | Optional Sentry transaction sample rate from `0` to `1` |

The service logs the absolute storage root on startup.

The env templates live in `deploy/env/` and use public-safe placeholder values.
Sentry events are sanitized before send: request headers, cookies, bodies, and
query strings are dropped so control and repo tokens are not reported.

## Access Logs

gitrdone writes one JSON access log line per HTTP request to stdout. Logs are
for auditability, not application tracing.

`GET /` and `GET /healthz` are intentionally skipped to avoid logging routine
agent discovery and health probe noise.

Logged fields:

- `timestamp`
- `method`
- `path`
- `status`
- `bytes`
- `durationMs`
- `remoteIp`
- `scheme`
- `host`
- `userAgent`

Access logs do not include query strings, authorization headers, cookies,
request bodies, or response bodies.

`X-Forwarded-For`, `X-Real-IP`, and `X-Forwarded-Proto` are used only when the
immediate peer matches `GITRDONE_TRUSTED_PROXIES`. The default trusts loopback,
which fits a local Caddy reverse proxy in front of `127.0.0.1:8080`.

## Shutdown

On `SIGINT` or `SIGTERM`, gitrdone calls `http.Server.Shutdown` and waits for
active requests to finish up to `GITRDONE_SHUTDOWN_TIMEOUT`. During this first
step there is no separate drain gate; restart behavior relies on Go's HTTP
server graceful shutdown semantics.

## Storage Model

- Bare Git repos are stored under `<storage-root>/repos/{uuid}.git`.
- Git LFS object bytes are stored outside the bare repo under `<storage-root>/lfs/{uuid}/objects/...`.
- Without `GITRDONE_DATABASE_URL`, control metadata and repo tokens are kept in memory.
- With `GITRDONE_DATABASE_URL`, repo metadata, token hashes, token lifecycle timestamps, and idempotency records are stored in Postgres.
- Bare repos still need durable filesystem storage even when Postgres is enabled.

## Administrative control API

Repository creation, token management, archival, and root-intent bootstrap require:

```http
Authorization: Bearer <GITRDONE_CONTROL_BEARER>
```

Create a repo:

```bash
curl -sS -X POST http://localhost:8080/v1/repos \
  -H "Authorization: Bearer dev-control-token" \
  -H "Content-Type: application/json" \
  -d '{"namespace":"acme","name":"example","defaultBranch":"main"}'
```

Response shape:

```json
{
  "id": "repo_00000000-0000-4000-8000-000000000000",
  "repo": "acme/example",
  "gitUrl": "http://localhost:8080/git/repos/repo_00000000-0000-4000-8000-000000000000.git",
  "defaultBranch": "main"
}
```

Create a repo token:

```bash
curl -sS -X POST http://localhost:8080/v1/repos/${REPO_ID}/tokens \
  -H "Authorization: Bearer dev-control-token" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: import:imp_123:source-read-token" \
  -d '{"scope":"readwrite","ttlSeconds":3600,"subject":"import:imp_123"}'
```

Token scopes:

| Scope | Allows |
| --- | --- |
| `read` | clone, fetch, pull, Git LFS downloads, read diff endpoints, read current intent, inspect changes and versions |
| `write` | push, Git LFS uploads, propose content |
| `readwrite` | all read and write capabilities; required by `grd submit` |

Create-token responses include the raw token. List and revoke endpoints return metadata only.

List token metadata:

```bash
curl -sS http://localhost:8080/v1/repos/${REPO_ID}/tokens \
  -H "Authorization: Bearer dev-control-token"
```

Revoke a token:

```bash
curl -sS -X POST http://localhost:8080/v1/repos/${REPO_ID}/tokens/${TOKEN_ID}/revoke \
  -H "Authorization: Bearer dev-control-token"
```

Other control routes:

```text
GET  /v1/repos/{repoID}
POST /v1/repos/{repoID}/archive
```

Root-intent bootstrap is the administrative exception on the intent path:

```text
PUT /v1/repos/{repoID}/intent
```

## Native repository API

`GET /v1/repos/{repoID}/intent`, change and conflict inspection, and the corresponding `/versions` collection accept a `read` or `readwrite` repo token. `POST /v1/repos/{repoID}/proposals` and `POST /v1/repos/{repoID}/reconciliation-conflicts` accept a `write` or `readwrite` repo token plus `Idempotency-Key`; the admitted producer is derived from the token subject rather than request JSON. A proposal may include `dependencies`, an array of exact admitted version IDs that must promote before the dependent version can promote. The control bearer remains accepted on these routes for trusted service callers. Root-intent bootstrap remains control-only.

```text
GET  /v1/repos/{repoID}/intent
POST /v1/repos/{repoID}/proposals
GET  /v1/repos/{repoID}/changes/{changeID}
GET  /v1/repos/{repoID}/changes/{changeID}/versions
POST /v1/repos/{repoID}/reconciliation-conflicts
GET  /v1/repos/{repoID}/reconciliation-conflicts
GET  /v1/repos/{repoID}/reconciliation-conflicts/{conflictID}
```

Repository amendment is an internal judgement operation, not a public command. Change inspection exposes `latestAmendment` and `latestPromotion` when those outcomes exist; the bounded versions collection preserves the immutable history.

A reconciliation-conflict POST records the authenticated observation that an already-admitted descendant version C could not be replayed from submitted B onto accepted amendment B′ at an exact current intent. New callers include `expectedIntent`; legacy omission is accepted only while B′'s promotion intent is still current. B′ may otherwise be historical but must remain in the expected intent's ancestry. The operation preserves C's existing Change/Version identity, records the authenticated reporter as `reportedBy`, and initially returns a durable conflict in `awaiting_judgement` state. Affected paths are optional, bounded adapter diagnostics, not a replacement for jj-core's future conflicted-content representation.

The conflict collection GET returns durable conflicts oldest-first with bounded `limit` and opaque `cursor` pagination. This is repository history, not a separately persisted worker queue. An internal judgement operation may admit engine-produced C′ as a new Version of C and record an immutable resolution before ordinary promotion; conflict reads then derive `resolved` and include that resolution. If an unresolved attempt or unpromoted resolution falls behind newer accepted intent, reads derive `superseded` while retaining the immutable facts. Internal engine work may rebase stale held C′ into C″ of the same Change and sends C″ through ordinary judgement. Reads expose the ordered effective Version chain, including later held rebases and ordinary amendments, so the Git portal follows accepted repository-produced content instead of creating parallel work from obsolete C. There is intentionally no public conflict-resolution or held-version-rebase mutation endpoint yet.

## grd client

Build the thin client and run it inside a clean Git workspace whose `origin` is a gitrdone HTTP remote:

```bash
go build -o grd ./cmd/grd
grd submit
grd status
grd sync
```

`grd submit` publishes the current committed content and admits it for judgement. Immediate promotion is reported directly. Otherwise the client reports judgement as pending and records a local continuation cursor so subsequent work can be submitted with an explicit dependency on that exact version. `grd status` shows the last known relationship between the active workspace and its submitted parent, includes the rationale for a pending repository amendment, and only claims it is based on accepted intent after checking Git ancestry. If the repository amends and promotes that submitted version, `grd sync` fetches the accepted version, creates a recovery ref, and explains the repository rationale. A workspace still at the submitted version moves directly to the accepted amendment; clean local successor commits are replayed onto it. If replay conflicts, the Git adapter restores the original workspace, first admits C with ordinary Change/Version identity, then records durable reconciliation work against that existing version and waits for judgement. Git path diagnostics are optional evidence, not a custom conflict representation; jj-core remains the intended engine for first-class conflicted versions and automatic descendant rebasing.

## Git adapter access

Canonical remote URL:

```text
http://localhost:8080/git/repos/{repoID}.git
```

Use repo IDs, not namespace/name, in Git URLs.

Git routes accept repo tokens via Basic auth or Bearer auth. For local automation, `http.extraHeader` keeps the token out of the remote URL:

```bash
git -c http.extraHeader="Authorization: Bearer ${REPO_TOKEN}" \
  clone "${GIT_URL}" worktree
```

Direct pushes to the canonical branch are rejected because they would bypass repository judgement. Make the repo token available through a Git credential helper, commit the intended content, then run `grd submit`. The current client publishes content to a temporary candidate ref and requests native admission; only promotion may move canonical `main`.

Normal Git clients can also use Basic auth with username `x-access-token` and the repo token as the password. Do not persist repo tokens in remote URLs.

Git LFS repositories use the same canonical Git remote and repo tokens. gitrdone supports the LFS Batch API, Basic Transfer upload/download, and lock verification. Lock verification returns an empty conflict set; gitrdone does not provide collaborative LFS locking.

Read-scoped service callers can fetch patch text without cloning:

```bash
curl -sS "${GIT_URL}/show/${SHA}.diff" \
  -H "Authorization: Bearer ${REPO_TOKEN}"

curl -sS "${GIT_URL}/compare/${BASE}..${HEAD}.diff" \
  -H "Authorization: Bearer ${REPO_TOKEN}"
```

`show/{sha}.diff` returns a single commit patch. `compare/{base}..{head}.diff`
returns an endpoint diff, and `compare/{base}...{head}.diff` uses Git's
merge-base comparison. Revision values must be lowercase hex object IDs, full
or abbreviated from 7 to 64 characters. Diff responses are capped at 8 MiB;
larger diffs return `413 Request Entity Too Large`.

## Public Agent Docs

The service exposes agent-readable discovery documents:

```text
GET /
GET /llms.txt
GET /.well-known/llms.txt
GET /AGENTS.md
GET /agents.md
GET /.well-known/agents.md
GET /llms-full.txt
GET /robots.txt
GET /sitemap.md
GET /sitemap.xml
```

Health check:

```text
GET /healthz
```

`/healthz` returns `204 No Content` when the service is up.

Git LFS endpoints for normal `git-lfs` clients:

```text
POST /git/repos/{repoID}.git/info/lfs/objects/batch
PUT  /git/repos/{repoID}.git/info/lfs/objects/{oid}
GET  /git/repos/{repoID}.git/info/lfs/objects/{oid}
POST /git/repos/{repoID}.git/info/lfs/locks/verify
```

## Test

```bash
go test ./...
go vet ./...
go test -race ./...
```

`TestGitLFSRealGitCommands` requires `git-lfs`. Set
`GITRDONE_SKIP_GIT_LFS_CONTRACT_TEST=1` only when intentionally running the
suite in an environment that cannot install the real client.

Postgres contract test:

```bash
scripts/test-postgres.sh
```
