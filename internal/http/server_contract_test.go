package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	httpapi "skyvalley.ac/m/v2/internal/http"
)

const controlAuthorization = "Bearer internal-admin-token"

func TestControlAPIContract(t *testing.T) {
	storageRoot := t.TempDir()
	server := httpapi.NewServer(httpapi.Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
		StorageRoot:   storageRoot,
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
		assertExternalRepoID(t, got.ID)
		if got.Repo != "differ/project-create" {
			t.Fatalf("repo = %q, want differ/project-create", got.Repo)
		}
		wantGitURL := "https://git.example.com/git/repos/" + got.ID + ".git"
		if got.GitURL != wantGitURL {
			t.Fatalf("gitUrl = %q, want %q", got.GitURL, wantGitURL)
		}
		if got.DefaultBranch != "main" {
			t.Fatalf("defaultBranch = %q, want main", got.DefaultBranch)
		}
		assertBareRepoStorage(t, storageRoot, got.ID, "main")
	})

	t.Run("create repo validates control input shape", func(t *testing.T) {
		tests := []struct {
			name string
			body string
			want string
		}{
			{
				name: "invalid namespace",
				body: `{"namespace":"differ/team","name":"project","defaultBranch":"main"}`,
				want: "namespace must be 1-128 characters and contain only letters, numbers, dot, underscore, or dash",
			},
			{
				name: "invalid name",
				body: `{"namespace":"differ","name":"project alpha","defaultBranch":"main"}`,
				want: "name must be 1-128 characters and contain only letters, numbers, dot, underscore, or dash",
			},
			{
				name: "invalid default branch",
				body: `{"namespace":"differ","name":"project-branch","defaultBranch":"main..bad"}`,
				want: "defaultBranch must be a valid branch name",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				res, body := request(t, server, http.MethodPost, "/v1/repos", controlAuthorization, "application/json", tt.body)

				requireStatus(t, res, body, http.StatusBadRequest)
				var got struct {
					Error string `json:"error"`
				}
				decodeJSON(t, res, body, &got)
				if got.Error != tt.want {
					t.Fatalf("error = %q, want %q", got.Error, tt.want)
				}
			})
		}
	})

	t.Run("create repo rejects trailing JSON", func(t *testing.T) {
		res, body := request(t, server, http.MethodPost, "/v1/repos", controlAuthorization, "application/json", `{"namespace":"differ","name":"json-trailing","defaultBranch":"main"} {}`)

		requireStatus(t, res, body, http.StatusBadRequest)
		var got struct {
			Error string `json:"error"`
		}
		decodeJSON(t, res, body, &got)
		if got.Error != "request body must be valid JSON for create repo" {
			t.Fatalf("error = %q, want valid JSON error", got.Error)
		}
	})

	t.Run("create repo rejects oversized JSON bodies", func(t *testing.T) {
		oversized := strings.Repeat(" ", 70*1024) + `{"namespace":"differ","name":"json-oversized","defaultBranch":"main"}`

		res, body := request(t, server, http.MethodPost, "/v1/repos", controlAuthorization, "application/json", oversized)

		requireStatus(t, res, body, http.StatusBadRequest)
		var got struct {
			Error string `json:"error"`
		}
		decodeJSON(t, res, body, &got)
		if got.Error != "request body must be valid JSON for create repo" {
			t.Fatalf("error = %q, want valid JSON error", got.Error)
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
		StorageRoot:   t.TempDir(),
	})

	t.Run("repo token can read and write", func(t *testing.T) {
		created := createRepoFixture(t, server, "readwrite-token")
		res, body := request(
			t,
			server,
			http.MethodPost,
			"/v1/repos/"+created.ID+"/tokens",
			controlAuthorization,
			"application/json",
			`{"scope":"readwrite","ttlSeconds":3600,"subject":"differ-bootstrap-job-abc"}`,
		)

		requireStatus(t, res, body, http.StatusCreated)
		var got struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
			GitURL    string `json:"gitUrl"`
			Scope     string `json:"scope"`
			Subject   string `json:"subject"`
		}
		decodeJSON(t, res, body, &got)
		if !strings.HasPrefix(got.Token, "gtd_") {
			t.Fatalf("token = %q, want gtd_ prefix", got.Token)
		}
		if got.Scope != "readwrite" {
			t.Fatalf("scope = %q, want readwrite", got.Scope)
		}
		if got.Subject != "differ-bootstrap-job-abc" {
			t.Fatalf("subject = %q, want differ-bootstrap-job-abc", got.Subject)
		}
		assertFutureExpiryWithin(t, got.ExpiresAt, time.Hour)
		wantGitURL := "https://git.example.com/git/repos/" + created.ID + ".git"
		if got.GitURL != wantGitURL {
			t.Fatalf("gitUrl = %q, want %q", got.GitURL, wantGitURL)
		}
		if strings.Contains(got.GitURL, got.Token) {
			t.Fatalf("gitUrl embeds token: %q", got.GitURL)
		}

		var raw map[string]any
		decodeJSON(t, res, body, &raw)
		if _, ok := raw["kind"]; ok {
			t.Fatalf("response includes kind: %#v", raw)
		}
	})

	t.Run("repo token creation is idempotent by caller key", func(t *testing.T) {
		created := createRepoFixture(t, server, "idempotent-token")
		requestBody := `{"scope":"readwrite","ttlSeconds":3600,"subject":"differ-import:imp_123:source-read"}`
		headers := map[string]string{
			"Authorization":   controlAuthorization,
			"Content-Type":    "application/json",
			"Idempotency-Key": "differ:import:imp_123:source-read-token",
		}

		firstRes, firstBody := requestWithHeaders(t, server, http.MethodPost, "/v1/repos/"+created.ID+"/tokens", headers, requestBody)
		secondRes, secondBody := requestWithHeaders(t, server, http.MethodPost, "/v1/repos/"+created.ID+"/tokens", headers, requestBody)

		requireStatus(t, firstRes, firstBody, http.StatusCreated)
		requireStatus(t, secondRes, secondBody, http.StatusCreated)
		if string(secondBody) != string(firstBody) {
			t.Fatalf("second idempotent response differed:\nfirst:  %s\nsecond: %s", string(firstBody), string(secondBody))
		}

		var got struct {
			Token string `json:"token"`
		}
		decodeJSON(t, secondRes, secondBody, &got)
		if got.Token == "" {
			t.Fatal("replayed token is empty")
		}
	})

	t.Run("repo token idempotency key conflicts on different request", func(t *testing.T) {
		created := createRepoFixture(t, server, "idempotent-token-conflict")
		headers := map[string]string{
			"Authorization":   controlAuthorization,
			"Content-Type":    "application/json",
			"Idempotency-Key": "differ:adaptation-run:run_123:push-token",
		}
		firstRequest := `{"scope":"write","ttlSeconds":3600,"subject":"differ-run:run_123:push"}`
		changedRequest := `{"scope":"read","ttlSeconds":3600,"subject":"differ-run:run_123:push"}`

		res, body := requestWithHeaders(t, server, http.MethodPost, "/v1/repos/"+created.ID+"/tokens", headers, firstRequest)
		requireStatus(t, res, body, http.StatusCreated)

		res, body = requestWithHeaders(t, server, http.MethodPost, "/v1/repos/"+created.ID+"/tokens", headers, changedRequest)

		requireStatus(t, res, body, http.StatusConflict)
		var got struct {
			Error string `json:"error"`
		}
		decodeJSON(t, res, body, &got)
		if got.Error != "idempotency key already used for a different token request" {
			t.Fatalf("error = %q, want idempotency conflict", got.Error)
		}
	})

	t.Run("repo token idempotency keys are scoped by repo", func(t *testing.T) {
		firstRepo := createRepoFixture(t, server, "idempotent-token-repo-one")
		secondRepo := createRepoFixture(t, server, "idempotent-token-repo-two")
		requestBody := `{"scope":"read","ttlSeconds":3600,"subject":"differ-reader-job"}`
		headers := map[string]string{
			"Authorization":   controlAuthorization,
			"Content-Type":    "application/json",
			"Idempotency-Key": "differ:shared:reader-token",
		}

		firstRes, firstBody := requestWithHeaders(t, server, http.MethodPost, "/v1/repos/"+firstRepo.ID+"/tokens", headers, requestBody)
		secondRes, secondBody := requestWithHeaders(t, server, http.MethodPost, "/v1/repos/"+secondRepo.ID+"/tokens", headers, requestBody)

		requireStatus(t, firstRes, firstBody, http.StatusCreated)
		requireStatus(t, secondRes, secondBody, http.StatusCreated)
		var first struct {
			Token  string `json:"token"`
			GitURL string `json:"gitUrl"`
		}
		var second struct {
			Token  string `json:"token"`
			GitURL string `json:"gitUrl"`
		}
		decodeJSON(t, firstRes, firstBody, &first)
		decodeJSON(t, secondRes, secondBody, &second)
		if first.Token == second.Token {
			t.Fatalf("same idempotency key across repos returned same token %q", first.Token)
		}
		if first.GitURL == second.GitURL {
			t.Fatalf("same idempotency key across repos returned same gitUrl %q", first.GitURL)
		}
	})

	t.Run("repo token can be issued for an external contributor subject", func(t *testing.T) {
		created := createRepoFixture(t, server, "contributor-token")
		res, body := request(
			t,
			server,
			http.MethodPost,
			"/v1/repos/"+created.ID+"/tokens",
			controlAuthorization,
			"application/json",
			`{"scope":"write","ttlSeconds":604800,"subject":"external:dev@example.com"}`,
		)

		requireStatus(t, res, body, http.StatusCreated)
		var got struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expiresAt"`
			Scope     string `json:"scope"`
			Subject   string `json:"subject"`
		}
		decodeJSON(t, res, body, &got)
		if !strings.HasPrefix(got.Token, "gtd_") {
			t.Fatalf("token = %q, want gtd_ prefix", got.Token)
		}
		if got.Scope != "write" {
			t.Fatalf("scope = %q, want write", got.Scope)
		}
		if got.Subject != "external:dev@example.com" {
			t.Fatalf("subject = %q, want external:dev@example.com", got.Subject)
		}
		assertFutureExpiryWithin(t, got.ExpiresAt, 7*24*time.Hour)
	})

	t.Run("repo token validates request shape", func(t *testing.T) {
		created := createRepoFixture(t, server, "invalid-token")
		tests := []struct {
			name string
			body string
			want string
		}{
			{
				name: "invalid scope",
				body: `{"scope":"admin","ttlSeconds":3600,"subject":"differ-bootstrap-job-abc"}`,
				want: "scope must be read, write, or readwrite",
			},
			{
				name: "missing subject",
				body: `{"scope":"read","ttlSeconds":3600,"subject":"   "}`,
				want: "subject is required",
			},
			{
				name: "invalid subject",
				body: `{"scope":"read","ttlSeconds":3600,"subject":"external dev@example.com"}`,
				want: "subject must be 1-128 characters and contain only letters, numbers, dot, underscore, dash, slash, colon, or at sign",
			},
			{
				name: "ttl too long",
				body: `{"scope":"read","ttlSeconds":604801,"subject":"differ-bootstrap-job-abc"}`,
				want: "ttlSeconds must be between 1 and 604800",
			},
			{
				name: "trailing JSON",
				body: `{"scope":"read","ttlSeconds":3600,"subject":"differ-reader-job"} {}`,
				want: "request body must be valid JSON for create repo token",
			},
			{
				name: "kind is not part of the token model",
				body: `{"scope":"read","ttlSeconds":3600,"subject":"differ-bootstrap-job-abc","kind":"internal"}`,
				want: "request body must be valid JSON for create repo token",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				res, body := request(
					t,
					server,
					http.MethodPost,
					"/v1/repos/"+created.ID+"/tokens",
					controlAuthorization,
					"application/json",
					tt.body,
				)

				requireStatus(t, res, body, http.StatusBadRequest)
				var got struct {
					Error string `json:"error"`
				}
				decodeJSON(t, res, body, &got)
				if got.Error != tt.want {
					t.Fatalf("error = %q, want %q", got.Error, tt.want)
				}
			})
		}
	})
}

func TestControlAuthContract(t *testing.T) {
	server := httpapi.NewServer(httpapi.Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
		StorageRoot:   t.TempDir(),
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
			body:        `{"scope":"readwrite","ttlSeconds":3600,"subject":"differ-bootstrap-job-abc"}`,
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

func request(t *testing.T, server http.Handler, method, target, auth, contentType, body string) (*http.Response, []byte) {
	t.Helper()
	headers := map[string]string{}
	if auth != "" {
		headers["Authorization"] = auth
	}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return requestWithHeaders(t, server, method, target, headers, body)
}

func requestWithHeaders(t *testing.T, server http.Handler, method, target string, headers map[string]string, body string) (*http.Response, []byte) {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
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
	assertExternalRepoID(t, created.ID)
	if created.Repo != namespace+"/"+name {
		t.Fatalf("created repo = %q, want %s/%s", created.Repo, namespace, name)
	}
	wantGitURL := "https://git.example.com/git/repos/" + created.ID + ".git"
	if created.GitURL != wantGitURL {
		t.Fatalf("created gitUrl = %q, want %q", created.GitURL, wantGitURL)
	}
	return created
}

func assertBareRepoStorage(t *testing.T, storageRoot string, externalRepoID string, defaultBranch string) {
	t.Helper()

	repoUUID := strings.TrimPrefix(externalRepoID, "repo_")
	repoPath := filepath.Join(storageRoot, "repos", repoUUID+".git")
	info, err := os.Stat(repoPath)
	if err != nil {
		t.Fatalf("bare repo path %q is not accessible: %v", repoPath, err)
	}
	if !info.IsDir() {
		t.Fatalf("bare repo path %q is not a directory", repoPath)
	}

	head, err := os.ReadFile(filepath.Join(repoPath, "HEAD"))
	if err != nil {
		t.Fatalf("read bare repo HEAD: %v", err)
	}
	wantHead := "ref: refs/heads/" + defaultBranch + "\n"
	if string(head) != wantHead {
		t.Fatalf("HEAD = %q, want %q", string(head), wantHead)
	}

	config, err := os.ReadFile(filepath.Join(repoPath, "config"))
	if err != nil {
		t.Fatalf("read bare repo config: %v", err)
	}
	if !strings.Contains(string(config), "bare = true") {
		t.Fatalf("bare repo config does not mark repository bare: %q", string(config))
	}
	for _, name := range []string{"objects", "refs"} {
		info, err := os.Stat(filepath.Join(repoPath, name))
		if err != nil {
			t.Fatalf("bare repo missing %s directory: %v", name, err)
		}
		if !info.IsDir() {
			t.Fatalf("bare repo %s path is not a directory", name)
		}
	}
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

func assertExternalRepoID(t *testing.T, value string) {
	t.Helper()
	if !strings.HasPrefix(value, "repo_") {
		t.Fatalf("id = %q, want repo_ prefix", value)
	}
	uuid := strings.TrimPrefix(value, "repo_")
	if len(uuid) != 36 {
		t.Fatalf("uuid = %q, length %d, want 36", uuid, len(uuid))
	}
	if uuid[14] != '4' {
		t.Fatalf("uuid = %q, want version 4", uuid)
	}
	switch uuid[19] {
	case '8', '9', 'a', 'b':
	default:
		t.Fatalf("uuid = %q, want RFC 4122 variant", uuid)
	}
}
