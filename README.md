# gitrdone

Authenticated Git smart HTTP with a small control API for repo-backed workflows.

gitrdone is intentionally narrow: it creates backing Git repos, mints repo-scoped access tokens, and serves those repos over normal Git HTTP routes. Higher-level product concepts belong in the caller.

## Requirements

- Go 1.26.3 or newer
- `git` available on `PATH`

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
| `GITRDONE_DATABASE_URL` | unset | Postgres URL for durable control metadata |
| `GITRDONE_CONTROL_BEARER` | required | Bearer token for `/v1` control routes |
| `GITRDONE_TRUSTED_PROXIES` | `127.0.0.1/32,::1/128` | Comma-separated proxy IPs/CIDRs whose forwarded headers may identify the client |

The service logs the absolute storage root on startup.

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

## Storage Model

- Bare Git repos are stored under `<storage-root>/repos/{uuid}.git`.
- Without `GITRDONE_DATABASE_URL`, control metadata and repo tokens are kept in memory.
- With `GITRDONE_DATABASE_URL`, repo metadata, token hashes, token lifecycle timestamps, and idempotency records are stored in Postgres.
- Bare repos still need durable filesystem storage even when Postgres is enabled.

## Control API

All `/v1` routes require:

```http
Authorization: Bearer <GITRDONE_CONTROL_BEARER>
```

Create a repo:

```bash
curl -sS -X POST http://localhost:8080/v1/repos \
  -H "Authorization: Bearer dev-control-token" \
  -H "Content-Type: application/json" \
  -d '{"namespace":"differ","name":"example","defaultBranch":"main"}'
```

Response shape:

```json
{
  "id": "repo_00000000-0000-4000-8000-000000000000",
  "repo": "differ/example",
  "gitUrl": "http://localhost:8080/git/repos/repo_00000000-0000-4000-8000-000000000000.git",
  "defaultBranch": "main"
}
```

Create a repo token:

```bash
curl -sS -X POST http://localhost:8080/v1/repos/${REPO_ID}/tokens \
  -H "Authorization: Bearer dev-control-token" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: differ:import:imp_123:source-read-token" \
  -d '{"scope":"readwrite","ttlSeconds":3600,"subject":"differ-import:imp_123"}'
```

Token scopes:

| Scope | Allows |
| --- | --- |
| `read` | clone, fetch, pull |
| `write` | push |
| `readwrite` | clone, fetch, pull, push |

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

## Git Access

Canonical remote URL:

```text
http://localhost:8080/git/repos/{repoID}.git
```

Use repo IDs, not namespace/name, in Git URLs.

Git routes accept repo tokens via Basic auth or Bearer auth. For local automation, `http.extraHeader` keeps the token out of the remote URL:

```bash
git -c http.extraHeader="Authorization: Bearer ${REPO_TOKEN}" \
  clone "${GIT_URL}" worktree

git -C worktree -c http.extraHeader="Authorization: Bearer ${REPO_TOKEN}" \
  push origin main
```

Normal Git clients can also use Basic auth with username `x-access-token` and the repo token as the password. Do not persist repo tokens in remote URLs.

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

## Test

```bash
go test ./...
go vet ./...
go test -race ./...
```

Postgres contract test:

```bash
scripts/test-postgres.sh
```
