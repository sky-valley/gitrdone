package httpapi_test

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type journeyWorld struct {
	t               *testing.T
	server          gitSmartHTTPFixture
	canonicalRemote string
}

type journeyIntent struct {
	ID         string `json:"id"`
	ContentRef struct {
		Engine   string `json:"engine"`
		Revision string `json:"revision"`
	} `json:"contentRef"`
}

type journeyWorkspace struct {
	t     *testing.T
	path  string
	token string
}

type journeyCommandResult struct {
	stdout string
	stderr string
	err    error
}

func newJourneyWorld(t *testing.T) *journeyWorld {
	t.Helper()

	server := newGitSmartHTTPFixture(t, "journey")
	bootstrapToken := createRepoTokenFixture(t, server.handler, server.repo.ID, "readwrite", "journey-bootstrap")
	canonicalRemote := server.tokenizedGitURL(bootstrapToken.Token)
	seed := newGitWorktree(t, "README.md", "initial\n")
	requireGitSuccess(t, "add bootstrap remote", "-C", seed, "remote", "add", "origin", canonicalRemote)
	requireGitSuccess(t, "publish bootstrap content", "-C", seed, "push", "origin", "HEAD:refs/candidates/bootstrap")
	bootstrapIntentFixture(t, server, gitRevParse(t, seed, "HEAD"))

	return &journeyWorld{
		t:               t,
		server:          server,
		canonicalRemote: canonicalRemote,
	}
}

func (world *journeyWorld) cloneWorkspace(actor string) *journeyWorkspace {
	world.t.Helper()

	token := createRepoTokenFixture(world.t, world.server.handler, world.server.repo.ID, "readwrite", actor)
	path := filepath.Join(world.t.TempDir(), "workspace")
	requireGitSuccess(world.t, "clone workspace for "+actor, "clone", "--branch", "main", world.server.tokenizedGitURL(token.Token), path)
	requireGitSuccess(world.t, "configure workspace email for "+actor, "-C", path, "config", "user.email", actor+"@example.invalid")
	requireGitSuccess(world.t, "configure workspace name for "+actor, "-C", path, "config", "user.name", actor)
	return &journeyWorkspace{t: world.t, path: path, token: token.Token}
}

func (world *journeyWorld) currentIntent() journeyIntent {
	world.t.Helper()

	res, body := request(world.t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/intent", controlAuthorization, "", "")
	requireStatus(world.t, res, body, http.StatusOK)
	var current journeyIntent
	decodeJSON(world.t, res, body, &current)
	return current
}

func (world *journeyWorld) canonicalHead() string {
	world.t.Helper()
	return gitRemoteRef(world.t, world.canonicalRemote, "refs/heads/main")
}

func (world *journeyWorld) buildGRD() string {
	world.t.Helper()

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		world.t.Fatal("locate journey fixture source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	binary := filepath.Join(world.t.TempDir(), "grd")
	command := exec.Command("go", "build", "-o", binary, "./cmd/grd")
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err != nil {
		world.t.Fatalf("build grd client: %v\n%s", err, output)
	}
	return binary
}

func (workspace *journeyWorkspace) head() string {
	workspace.t.Helper()
	return gitRevParse(workspace.t, workspace.path, "HEAD")
}

func (workspace *journeyWorkspace) isClean() bool {
	workspace.t.Helper()
	output, err := runGitForTest("-C", workspace.path, "status", "--porcelain")
	if err != nil {
		workspace.t.Fatalf("read workspace status: %v\n%s", err, output)
	}
	return strings.TrimSpace(output) == ""
}

func (workspace *journeyWorkspace) commitFile(path string, content string, message string) string {
	workspace.t.Helper()

	writeGitFile(workspace.t, workspace.path, path, content)
	requireGitSuccess(workspace.t, "stage "+path, "-C", workspace.path, "add", path)
	requireGitSuccess(workspace.t, "commit "+path, "-C", workspace.path, "commit", "-m", message)
	return workspace.head()
}

func (workspace *journeyWorkspace) run(binary string, args ...string) journeyCommandResult {
	workspace.t.Helper()

	command := exec.Command(binary, args...)
	command.Dir = workspace.path
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return journeyCommandResult{
		stdout: stdout.String(),
		stderr: stderr.String(),
		err:    err,
	}
}

func TestJourneyWorldStagesManagedRepositoryAndWorkspaceAtAcceptedIntent(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")

	accepted := world.currentIntent()
	if accepted.ID == "" {
		t.Fatal("accepted intent id is empty")
	}
	if accepted.ContentRef.Engine != "git" {
		t.Fatalf("accepted intent engine = %q, want git", accepted.ContentRef.Engine)
	}
	if got := ion.head(); got != accepted.ContentRef.Revision {
		t.Fatalf("Ion workspace HEAD = %q, want accepted revision %q", got, accepted.ContentRef.Revision)
	}
	if got := world.canonicalHead(); got != accepted.ContentRef.Revision {
		t.Fatalf("canonical main = %q, want accepted revision %q", got, accepted.ContentRef.Revision)
	}
	if !ion.isClean() {
		t.Fatal("Ion workspace is dirty after staging")
	}
}
