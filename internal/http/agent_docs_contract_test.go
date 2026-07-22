package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	httpapi "github.com/sky-valley/gitrdone/internal/http"
)

func TestAgentDiscoveryDocumentsContract(t *testing.T) {
	server := httpapi.NewServer(httpapi.Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
		StorageRoot:   t.TempDir(),
	})

	t.Run("root serves an agent-facing guide", func(t *testing.T) {
		res, body := request(t, server, http.MethodGet, "/", "", "", "")

		requireStatus(t, res, body, http.StatusOK)
		requireContentType(t, res, "text/markdown")
		requireNoSniff(t, res)
		requireBodyIncludes(t, body, "# gitrdone")
		requireBodyIncludes(t, body, "/llms.txt")
		requireBodyIncludes(t, body, "/AGENTS.md")
		requireBodyIncludes(t, body, "/git/repos/{repoID}.git")
	})

	t.Run("llms txt follows root markdown discovery convention", func(t *testing.T) {
		res, body := request(t, server, http.MethodGet, "/llms.txt", "", "", "")

		requireStatus(t, res, body, http.StatusOK)
		requireContentType(t, res, "text/plain")
		requireNoSniff(t, res)
		requireBodyIncludes(t, body, "# gitrdone")
		requireBodyIncludes(t, body, "> gitrdone is a repository service where proposed changes become accepted intent through judgement")
		requireBodyIncludes(t, body, "[Agent guide](https://git.example.com/AGENTS.md)")
		requireBodyIncludes(t, body, "[Git smart HTTP and LFS](https://git.example.com/git/repos/{repoID}.git)")
		requireBodyIncludes(t, body, "For retriable automation, include Idempotency-Key on token creation")
		requireBodyIncludes(t, body, "List and revoke repo tokens with the control bearer token; token values are returned only at creation.")
		requireBodyIncludes(t, body, "POST /v1/repos/{repoID}/proposals")
		requireBodyIncludes(t, body, "A successful proposal response means the change version was durably admitted")
		requireBodyIncludes(t, body, "## Administrative API Surface")
		requireBodyIncludes(t, body, "## Native Repository API Surface")
		requireBodyIncludes(t, body, "## Git Adapter API Surface")
		requireBodyIncludes(t, body, "Admit proposal")
		requireBodyIncludes(t, body, "List change versions")
	})

	t.Run("well-known llms txt aliases root llms txt", func(t *testing.T) {
		_, canonical := request(t, server, http.MethodGet, "/llms.txt", "", "", "")
		res, alias := request(t, server, http.MethodGet, "/.well-known/llms.txt", "", "", "")

		requireStatus(t, res, alias, http.StatusOK)
		if string(alias) != string(canonical) {
			t.Fatalf("well-known llms.txt alias differs from canonical")
		}
	})

	t.Run("agents markdown is available at common agent paths", func(t *testing.T) {
		paths := []string{"/AGENTS.md", "/agents.md", "/.well-known/agents.md", "/llms-full.txt"}
		for _, path := range paths {
			t.Run(path, func(t *testing.T) {
				res, body := request(t, server, http.MethodGet, path, "", "", "")

				requireStatus(t, res, body, http.StatusOK)
				requireContentType(t, res, "text/markdown")
				requireNoSniff(t, res)
				requireBodyIncludes(t, body, "## Agent-safe surfaces")
				requireBodyIncludes(t, body, "POST /v1/repos/{repoID}/tokens")
				requireBodyIncludes(t, body, "GET /v1/repos/{repoID}/tokens")
				requireBodyIncludes(t, body, "POST /v1/repos/{repoID}/tokens/{tokenID}/revoke")
				requireBodyIncludes(t, body, "GET /v1/repos/{repoID}/intent")
				requireBodyIncludes(t, body, "PUT /v1/repos/{repoID}/intent")
				requireBodyIncludes(t, body, "POST /v1/repos/{repoID}/proposals")
				requireBodyIncludes(t, body, "GET /v1/repos/{repoID}/changes/{changeID}")
				requireBodyIncludes(t, body, "GET /v1/repos/{repoID}/changes/{changeID}/versions")
				requireBodyIncludes(t, body, "Administrative control API for trusted services")
				requireBodyIncludes(t, body, "Native repository API")
				requireBodyIncludes(t, body, "Git smart HTTP adapter")
				requireBodyIncludes(t, body, "Repository amendment is an internal judgement operation")
				requireBodyIncludes(t, body, "Idempotency-Key is required for proposals")
				requireBodyIncludes(t, body, "Promotion is an opportunistic current result, not part of the admission guarantee")
				requireBodyIncludes(t, body, "GET /git/repos/{repoID}.git/info/refs?service=git-upload-pack")
				requireBodyIncludes(t, body, "POST /git/repos/{repoID}.git/info/lfs/objects/batch")
				requireBodyIncludes(t, body, "GET /git/repos/{repoID}.git/info/lfs/objects/{oid}")
				requireBodyIncludes(t, body, "Git LFS lock verification returns an empty conflict set")
				requireBodyIncludes(t, body, "GET /git/repos/{repoID}.git/show/{sha}.diff")
				requireBodyIncludes(t, body, "GET /git/repos/{repoID}.git/compare/{base}..{head}.diff")
				requireBodyIncludes(t, body, "rejects external updates to the canonical branch")
				requireBodyIncludes(t, body, "Candidate ref names are temporary adapter storage")
				requireBodyIncludes(t, body, "## Reliable token creation")
				requireBodyIncludes(t, body, "Idempotency-Key: import:imp_123:source-read-token")
				requireBodyIncludes(t, body, "Reuse the same key only for the same repo, scope, subject, and TTL.")
				requireBodyIncludes(t, body, "If the same key is reused for a different token request, gitrdone returns 409 Conflict")
				requireBodyIncludes(t, body, "## Token lifecycle")
				requireBodyIncludes(t, body, "List and revoke responses return token metadata only; token values are returned only at creation.")
				requireBodyIncludes(t, body, "Revoke tokens when a workflow is done or when a token may have leaked.")
				requireBodyIncludes(t, body, "Do not use namespace/name Git routes")
			})
		}
	})

	t.Run("robots txt points agents at the guide surface", func(t *testing.T) {
		res, body := request(t, server, http.MethodGet, "/robots.txt", "", "", "")

		requireStatus(t, res, body, http.StatusOK)
		requireContentType(t, res, "text/plain")
		requireNoSniff(t, res)
		requireBodyIncludes(t, body, "User-agent: *")
		requireBodyIncludes(t, body, "Allow: /llms.txt")
		requireBodyIncludes(t, body, "Sitemap: https://git.example.com/sitemap.xml")
	})

	t.Run("sitemap markdown lists agent-facing endpoints", func(t *testing.T) {
		res, body := request(t, server, http.MethodGet, "/sitemap.md", "", "", "")

		requireStatus(t, res, body, http.StatusOK)
		requireContentType(t, res, "text/markdown")
		requireNoSniff(t, res)
		requireBodyIncludes(t, body, "[llms.txt](https://git.example.com/llms.txt)")
		requireBodyIncludes(t, body, "[Agent guide](https://git.example.com/AGENTS.md)")
		requireBodyIncludes(t, body, "[Health](https://git.example.com/healthz)")
	})

	t.Run("sitemap xml lists agent-facing endpoints", func(t *testing.T) {
		res, body := request(t, server, http.MethodGet, "/sitemap.xml", "", "", "")

		requireStatus(t, res, body, http.StatusOK)
		requireContentType(t, res, "application/xml")
		requireNoSniff(t, res)
		requireBodyIncludes(t, body, "<loc>https://git.example.com/llms.txt</loc>")
		requireBodyIncludes(t, body, "<loc>https://git.example.com/AGENTS.md</loc>")
		requireBodyIncludes(t, body, "<loc>https://git.example.com/healthz</loc>")
	})
}

func requireContentType(t *testing.T, res *http.Response, wantPrefix string) {
	t.Helper()
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("Content-Type = %q, want prefix %q", got, wantPrefix)
	}
}

func requireNoSniff(t *testing.T, res *http.Response) {
	t.Helper()
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func requireBodyIncludes(t *testing.T, body []byte, want string) {
	t.Helper()
	if !strings.Contains(string(body), want) {
		t.Fatalf("body missing %q:\n%s", want, string(body))
	}
}
