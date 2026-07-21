package httpapi_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitRawFileRealGitCommands(t *testing.T) {
	fixture := newGitSmartHTTPFixture(t, "raw")
	readwriteToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "readwrite", "differ-bootstrap-job")
	readToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "read", "differ-reader-job")
	writeToken := createRepoTokenFixture(t, fixture.handler, fixture.repo.ID, "write", "differ-writer-job")

	// Binary content with null bytes and a fake PNG header: the endpoint must
	// be byte-exact, never text-normalizing.
	imageBytes := "\x89PNG\r\n\x1a\n\x00\x01\x02\xff binary \x00 payload"
	worktree := newGitWorktree(t, "README.md", "hello\n")
	writeGitFile(t, worktree, "shots/root.desktop.png", imageBytes)
	requireGitSuccess(t, "stage image", "-C", worktree, "add", "shots/root.desktop.png")
	requireGitSuccess(t, "commit image", "-C", worktree, "commit", "-m", "add capture")
	pushURL := fixture.tokenizedGitURL(readwriteToken.Token)
	requireGitSuccess(t, "add origin", "-C", worktree, "remote", "add", "origin", pushURL)
	requireGitSuccess(t, "push", "-C", worktree, "push", "-u", "origin", "main")
	head := gitRevParse(t, worktree, "HEAD")

	t.Run("read token gets byte-exact file content", func(t *testing.T) {
		status, header, body := getGitRaw(t, fixture, head+"/shots/root.desktop.png", readToken.Token)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200\n%s", status, body)
		}
		if body != imageBytes {
			t.Fatalf("body = %q, want the exact committed bytes", body)
		}
		if got := header.Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("content type = %q, want application/octet-stream", got)
		}
		if header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("nosniff header missing")
		}
	})

	t.Run("root level file resolves", func(t *testing.T) {
		status, _, body := getGitRaw(t, fixture, head+"/README.md", readToken.Token)
		if status != http.StatusOK || body != "hello\n" {
			t.Fatalf("status = %d body = %q, want the README bytes", status, body)
		}
	})

	t.Run("unknown revision is not found", func(t *testing.T) {
		absent := "0000000000000000000000000000000000000000"
		status, _, _ := getGitRaw(t, fixture, absent+"/README.md", readToken.Token)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
	})

	t.Run("absent path is not found", func(t *testing.T) {
		status, _, _ := getGitRaw(t, fixture, head+"/no/such/file.png", readToken.Token)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
	})

	t.Run("directory path is rejected", func(t *testing.T) {
		status, _, _ := getGitRaw(t, fixture, head+"/shots", readToken.Token)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a non-blob path", status)
		}
	})

	t.Run("dot dot path segments never reach the handler", func(t *testing.T) {
		// The mux canonicalizes unclean paths with a redirect before routing,
		// so a traversal-shaped URL resolves to its clean form — it can never
		// escape the tree. (parseGitRawSpec still rejects raw dot-dot as
		// defense-in-depth for non-HTTP callers.)
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		url := strings.TrimRight(fixture.server.URL, "/") + "/git/repos/" + fixture.repo.ID + ".git/raw/" + head + "/shots/../README.md"
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+readToken.Token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 300 || response.StatusCode >= 400 {
			t.Fatalf("status = %d, want a redirect to the canonicalized path", response.StatusCode)
		}
		if location := response.Header.Get("Location"); strings.Contains(location, "..") {
			t.Fatalf("location = %q must be clean", location)
		}
	})

	t.Run("missing path is rejected", func(t *testing.T) {
		status, _, _ := getGitRaw(t, fixture, head, readToken.Token)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a spec without a path", status)
		}
	})

	t.Run("write only token cannot read", func(t *testing.T) {
		status, _, _ := getGitRaw(t, fixture, head+"/README.md", writeToken.Token)
		if status != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", status)
		}
	})

	t.Run("missing token cannot read", func(t *testing.T) {
		status, _, _ := getGitRaw(t, fixture, head+"/README.md", "")
		if status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", status)
		}
	})
}

func getGitRaw(t *testing.T, fixture gitSmartHTTPFixture, spec string, token string) (int, http.Header, string) {
	t.Helper()

	url := strings.TrimRight(fixture.server.URL, "/") + "/git/repos/" + fixture.repo.ID + ".git/raw/" + spec
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := fixture.server.Client().Do(request)
	if err != nil {
		t.Fatalf("raw request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read raw response: %v", err)
	}
	return response.StatusCode, response.Header, string(body)
}
