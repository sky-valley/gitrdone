package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "skyvalley.ac/m/v2/internal/http"
)

const controlAuthorization = "Bearer internal-admin-token"

func TestControlAPIContract(t *testing.T) {
	server := httpapi.NewServer(httpapi.Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
	})

	t.Run("healthz reports service readiness", func(t *testing.T) {
		res, body := request(t, server, http.MethodGet, "/healthz", "", "", "")

		requireStatus(t, res, body, http.StatusNoContent)
		if len(body) != 0 {
			t.Fatalf("body = %q, want empty", string(body))
		}
	})

	t.Run("internal traffic creates a repo", func(t *testing.T) {
		res, body := createRepo(t, server, "differ", "project-create", "main")

		requireStatus(t, res, body, http.StatusCreated)
		var got struct {
			ID            string `json:"id"`
			Repo          string `json:"repo"`
			GitURL        string `json:"gitUrl"`
			DefaultBranch string `json:"defaultBranch"`
		}
		decodeJSON(t, res, body, &got)
		if got.ID == "" {
			t.Fatal("id is empty")
		}
		if got.Repo != "differ/project-create" {
			t.Fatalf("repo = %q, want differ/project-create", got.Repo)
		}
		if got.GitURL != "https://git.example.com/differ/project-create.git" {
			t.Fatalf("gitUrl = %q", got.GitURL)
		}
		if got.DefaultBranch != "main" {
			t.Fatalf("defaultBranch = %q, want main", got.DefaultBranch)
		}
	})

	t.Run("repo lookup returns the public repo identity", func(t *testing.T) {
		created := createRepoFixture(t, server, "lookup")
		res, body := request(t, server, http.MethodGet, "/v1/repos/"+created.ID, controlAuthorization, "", "")

		requireStatus(t, res, body, http.StatusOK)
		var got struct {
			ID       string `json:"id"`
			Repo     string `json:"repo"`
			GitURL   string `json:"gitUrl"`
			Archived bool   `json:"archived"`
		}
		decodeJSON(t, res, body, &got)
		if got.ID != created.ID {
			t.Fatalf("id = %q, want %q", got.ID, created.ID)
		}
		if got.Repo != created.Repo {
			t.Fatalf("repo = %q, want %q", got.Repo, created.Repo)
		}
		if got.GitURL != created.GitURL {
			t.Fatalf("gitUrl = %q, want %q", got.GitURL, created.GitURL)
		}
		if got.Archived {
			t.Fatal("archived = true, want false")
		}
	})

	t.Run("archive repo is metadata-first", func(t *testing.T) {
		created := createRepoFixture(t, server, "archive")
		res, body := request(t, server, http.MethodPost, "/v1/repos/"+created.ID+"/archive", controlAuthorization, "", "")

		requireStatus(t, res, body, http.StatusOK)
		var got struct {
			ID         string `json:"id"`
			Repo       string `json:"repo"`
			Archived   bool   `json:"archived"`
			ArchivedAt string `json:"archivedAt"`
		}
		decodeJSON(t, res, body, &got)
		if got.ID != created.ID {
			t.Fatalf("id = %q, want %q", got.ID, created.ID)
		}
		if got.Repo != created.Repo {
			t.Fatalf("repo = %q, want %q", got.Repo, created.Repo)
		}
		if !got.Archived {
			t.Fatal("archived = false, want true")
		}
		if _, err := time.Parse(time.RFC3339, got.ArchivedAt); err != nil {
			t.Fatalf("archivedAt is not RFC3339: %q", got.ArchivedAt)
		}
	})
}

func TestTokenProvisioningContract(t *testing.T) {
	server := httpapi.NewServer(httpapi.Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
	})

	t.Run("short lived internal token can read and write", func(t *testing.T) {
		created := createRepoFixture(t, server, "internal-token")
		res, body := request(
			t,
			server,
			http.MethodPost,
			"/v1/repos/"+created.ID+"/tokens",
			controlAuthorization,
			"application/json",
			`{"scope":"readwrite","ttlSeconds":3600,"subject":"differ-bootstrap-job-abc","kind":"internal"}`,
		)

		requireStatus(t, res, body, http.StatusCreated)
		var got struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
			GitURL    string `json:"gitUrl"`
			Scope     string `json:"scope"`
			Kind      string `json:"kind"`
		}
		decodeJSON(t, res, body, &got)
		if !strings.HasPrefix(got.Token, "gtd_") {
			t.Fatalf("token = %q, want gtd_ prefix", got.Token)
		}
		if got.Scope != "readwrite" {
			t.Fatalf("scope = %q, want readwrite", got.Scope)
		}
		if got.Kind != "internal" {
			t.Fatalf("kind = %q, want internal", got.Kind)
		}
		assertFutureExpiryWithin(t, got.ExpiresAt, time.Hour)
		if !strings.Contains(got.GitURL, got.Token+"@git.example.com/"+created.Repo+".git") {
			t.Fatalf("gitUrl does not embed token for normal git clients: %q", got.GitURL)
		}
	})

	t.Run("developer token supports longer direct push access", func(t *testing.T) {
		created := createRepoFixture(t, server, "developer-token")
		res, body := request(
			t,
			server,
			http.MethodPost,
			"/v1/repos/"+created.ID+"/tokens",
			controlAuthorization,
			"application/json",
			`{"scope":"readwrite","ttlSeconds":604800,"subject":"dev@example.com","kind":"developer"}`,
		)

		requireStatus(t, res, body, http.StatusCreated)
		var got struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
			Scope     string `json:"scope"`
			Kind      string `json:"kind"`
			Subject   string `json:"subject"`
		}
		decodeJSON(t, res, body, &got)
		if !strings.HasPrefix(got.Token, "gtd_") {
			t.Fatalf("token = %q, want gtd_ prefix", got.Token)
		}
		if got.Scope != "readwrite" {
			t.Fatalf("scope = %q, want readwrite", got.Scope)
		}
		if got.Kind != "developer" {
			t.Fatalf("kind = %q, want developer", got.Kind)
		}
		if got.Subject != "dev@example.com" {
			t.Fatalf("subject = %q, want dev@example.com", got.Subject)
		}
		assertFutureExpiryWithin(t, got.ExpiresAt, 7*24*time.Hour)
	})
}

func TestControlAuthContract(t *testing.T) {
	server := httpapi.NewServer(httpapi.Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
	})

	tests := []struct {
		name        string
		method      string
		target      string
		auth        string
		contentType string
		body        string
	}{
		{
			name:        "create repo rejects missing auth",
			method:      http.MethodPost,
			target:      "/v1/repos",
			contentType: "application/json",
			body:        `{"namespace":"differ","name":"project-123","defaultBranch":"main"}`,
		},
		{
			name:   "get repo rejects missing auth",
			method: http.MethodGet,
			target: "/v1/repos/repo_123",
		},
		{
			name:   "archive repo rejects missing auth",
			method: http.MethodPost,
			target: "/v1/repos/repo_123/archive",
		},
		{
			name:        "create token rejects missing auth",
			method:      http.MethodPost,
			target:      "/v1/repos/repo_123/tokens",
			contentType: "application/json",
			body:        `{"scope":"readwrite","ttlSeconds":3600,"subject":"differ-bootstrap-job-abc","kind":"internal"}`,
		},
		{
			name:        "control route rejects wrong bearer token",
			method:      http.MethodPost,
			target:      "/v1/repos",
			auth:        "Bearer wrong-token",
			contentType: "application/json",
			body:        `{"namespace":"differ","name":"project-123","defaultBranch":"main"}`,
		},
		{
			name:        "control route rejects non-bearer authorization",
			method:      http.MethodPost,
			target:      "/v1/repos",
			auth:        "Basic aW50ZXJuYWwtYWRtaW4tdG9rZW4=",
			contentType: "application/json",
			body:        `{"namespace":"differ","name":"project-123","defaultBranch":"main"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, body := request(t, server, tt.method, tt.target, tt.auth, tt.contentType, tt.body)

			requireStatus(t, res, body, http.StatusUnauthorized)
			if len(body) != 0 {
				t.Fatalf("body = %q, want empty", string(body))
			}
		})
	}
}

func TestGitSmartHTTPContract(t *testing.T) {
	server := httpapi.NewServer(httpapi.Config{
		BaseURL: "https://git.example.com",
	})

	t.Run("upload-pack discovery uses smart http content type", func(t *testing.T) {
		res, body := request(t, server, http.MethodGet, "/differ/project-123.git/info/refs?service=git-upload-pack", "Bearer gtd_read_token", "", "")

		requireStatus(t, res, body, http.StatusOK)
		if got := res.Header.Get("Content-Type"); got != "application/x-git-upload-pack-advertisement" {
			t.Fatalf("Content-Type = %q", got)
		}
		if !bytes.HasPrefix(body, []byte("001e# service=git-upload-pack\n")) {
			t.Fatalf("body does not start with upload-pack service advertisement: %q", string(body))
		}
	})

	t.Run("receive-pack accepts pushes through normal git smart http", func(t *testing.T) {
		res, body := request(
			t,
			server,
			http.MethodPost,
			"/differ/project-123.git/git-receive-pack",
			"Bearer gtd_write_token",
			"application/x-git-receive-pack-request",
			"0000",
		)

		requireStatus(t, res, body, http.StatusOK)
		if got := res.Header.Get("Content-Type"); got != "application/x-git-receive-pack-result" {
			t.Fatalf("Content-Type = %q", got)
		}
		if len(body) == 0 {
			t.Fatal("receive-pack response body is empty")
		}
	})
}

func request(t *testing.T, server http.Handler, method, target, auth, contentType, body string) (*http.Response, []byte) {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	res := rec.Result()
	t.Cleanup(func() {
		res.Body.Close()
	})

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, resBody
}

type createdRepoFixture struct {
	ID     string `json:"id"`
	Repo   string `json:"repo"`
	GitURL string `json:"gitUrl"`
}

func createRepoFixture(t *testing.T, server http.Handler, suffix string) createdRepoFixture {
	t.Helper()

	namespace := "fixture"
	name := fmt.Sprintf("%s-%d", suffix, time.Now().UnixNano())
	res, body := createRepo(t, server, namespace, name, "main")
	requireStatus(t, res, body, http.StatusCreated)

	var created createdRepoFixture
	decodeJSON(t, res, body, &created)
	if created.ID == "" {
		t.Fatal("created repo id is empty")
	}
	if created.Repo != namespace+"/"+name {
		t.Fatalf("created repo = %q, want %s/%s", created.Repo, namespace, name)
	}
	if created.GitURL != "https://git.example.com/"+namespace+"/"+name+".git" {
		t.Fatalf("created gitUrl = %q", created.GitURL)
	}
	return created
}

func createRepo(t *testing.T, server http.Handler, namespace, name, defaultBranch string) (*http.Response, []byte) {
	t.Helper()

	body := fmt.Sprintf(
		`{"namespace":%q,"name":%q,"defaultBranch":%q}`,
		namespace,
		name,
		defaultBranch,
	)
	return request(t, server, http.MethodPost, "/v1/repos", controlAuthorization, "application/json", body)
}

func requireStatus(t *testing.T, res *http.Response, body []byte, want int) {
	t.Helper()
	if res.StatusCode != want {
		t.Fatalf("status = %d, want %d; body = %q", res.StatusCode, want, string(body))
	}
}

func decodeJSON(t *testing.T, res *http.Response, body []byte, target any) {
	t.Helper()
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("invalid JSON response %q: %v", string(body), err)
	}
}

func assertFutureExpiryWithin(t *testing.T, value string, duration time.Duration) {
	t.Helper()
	expiresAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("expiresAt is not RFC3339: %q", value)
	}
	min := time.Now().Add(duration - time.Minute)
	max := time.Now().Add(duration + time.Minute)
	if expiresAt.Before(min) || expiresAt.After(max) {
		t.Fatalf("expiresAt = %s, want within one minute of %s", expiresAt, duration)
	}
}
