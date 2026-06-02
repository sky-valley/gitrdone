package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	httpapi "skyvalley.ac/m/v2/internal/http"
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
		requireBodyIncludes(t, body, "# giterdone")
		requireBodyIncludes(t, body, "/llms.txt")
		requireBodyIncludes(t, body, "/AGENTS.md")
		requireBodyIncludes(t, body, "/git/repos/{repoID}.git")
	})

	t.Run("llms txt follows root markdown discovery convention", func(t *testing.T) {
		res, body := request(t, server, http.MethodGet, "/llms.txt", "", "", "")

		requireStatus(t, res, body, http.StatusOK)
		requireContentType(t, res, "text/plain")
		requireNoSniff(t, res)
		requireBodyIncludes(t, body, "# giterdone")
		requireBodyIncludes(t, body, "> giterdone is an authenticated Git smart HTTP service")
		requireBodyIncludes(t, body, "[Agent guide](https://git.example.com/AGENTS.md)")
		requireBodyIncludes(t, body, "[Git smart HTTP](https://git.example.com/git/repos/{repoID}.git)")
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
				requireBodyIncludes(t, body, "GET /git/repos/{repoID}.git/info/refs?service=git-upload-pack")
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
