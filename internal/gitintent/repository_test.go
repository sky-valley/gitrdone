package gitintent_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/gitrdone/internal/gitintent"
	"github.com/sky-valley/gitrdone/internal/intent"
)

func TestRepositoryAdmitsContentAndAdvancesTrunkWithCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	repository, err := gitintent.OpenRepository(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("new git intent repository: %v", err)
	}
	versionID := intent.VersionID("version_0123456789abcdef")
	initial := intent.ContentRef{Engine: "git", Revision: fixture.initial}
	proposed := intent.ContentRef{Engine: "git", Revision: fixture.proposed}
	if got, err := repository.Current(ctx); err != nil || got != initial {
		t.Fatalf("current trunk = %#v, error %v; want %#v, nil", got, err, initial)
	}

	if err := repository.Admit(ctx, versionID, proposed); err != nil {
		t.Fatalf("admit proposed content: %v", err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "show-ref", "--verify", "--hash", "refs/gitrdone/holding/"+string(versionID)); got != fixture.proposed {
		t.Fatalf("holding ref = %q, want %q", got, fixture.proposed)
	}
	if got := gitOutput(t, "ls-remote", fixture.gitDir, "refs/gitrdone/holding/*"); got != "" {
		t.Fatalf("advertised holding refs = %q, want none", got)
	}
	if err := repository.Admit(ctx, versionID, proposed); err != nil {
		t.Fatalf("retry same admission: %v", err)
	}
	if err := repository.Admit(ctx, versionID, initial); err == nil {
		t.Fatal("rebind holding ref to different content succeeded")
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "show-ref", "--verify", "--hash", "refs/gitrdone/holding/"+string(versionID)); got != fixture.proposed {
		t.Fatalf("holding ref after rejected rebind = %q, want %q", got, fixture.proposed)
	}

	if err := repository.Advance(ctx, initial, proposed); err != nil {
		t.Fatalf("advance trunk: %v", err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.proposed {
		t.Fatalf("trunk = %q, want %q", got, fixture.proposed)
	}
	if got, err := repository.Current(ctx); err != nil || got != proposed {
		t.Fatalf("current trunk after advance = %#v, error %v; want %#v, nil", got, err, proposed)
	}

	if err := repository.Advance(ctx, initial, initial); !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("stale advance error = %v, want ErrIntentAdvanced", err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.proposed {
		t.Fatalf("trunk after stale advance = %q, want %q", got, fixture.proposed)
	}
}

func TestRepositoryDoesNotHoldMissingContent(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	repository, err := gitintent.OpenRepository(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("new git intent repository: %v", err)
	}
	versionID := intent.VersionID("version_missing")

	err = repository.Admit(ctx, versionID, intent.ContentRef{
		Engine:   "git",
		Revision: strings.Repeat("f", 40),
	})
	if err == nil {
		t.Fatal("admit missing content succeeded")
	}
	if !errors.Is(err, intent.ErrContentNotAdmissible) {
		t.Fatalf("missing content error = %v, want ErrContentNotAdmissible", err)
	}
	cmd := exec.Command("git", "--git-dir", fixture.gitDir, "show-ref", "--verify", "refs/gitrdone/holding/"+string(versionID))
	if err := cmd.Run(); err == nil {
		t.Fatal("missing content created a holding ref")
	}
}

func TestRepositoryRejectsContentFromAnotherEngine(t *testing.T) {
	fixture := newGitFixture(t)
	repository, err := gitintent.OpenRepository(context.Background(), fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	err = repository.Admit(context.Background(), "version_wrong_engine", intent.ContentRef{Engine: "jj", Revision: fixture.proposed})
	if !errors.Is(err, intent.ErrContentNotAdmissible) {
		t.Fatalf("admit error = %v, want ErrContentNotAdmissible", err)
	}
}

func TestRepositoryBootstrapsTrunkExactlyOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	runGit(t, "--git-dir", fixture.gitDir, "update-ref", "-d", "refs/heads/main")
	repository, err := gitintent.OpenRepository(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	initial := intent.ContentRef{Engine: "git", Revision: fixture.initial}

	if err := repository.Bootstrap(ctx, initial); err != nil {
		t.Fatalf("bootstrap trunk: %v", err)
	}
	if err := repository.Bootstrap(ctx, initial); err != nil {
		t.Fatalf("retry same bootstrap: %v", err)
	}
	if err := repository.Bootstrap(ctx, intent.ContentRef{Engine: "git", Revision: fixture.proposed}); !errors.Is(err, gitintent.ErrTrunkAlreadyInitialized) {
		t.Fatalf("different bootstrap error = %v, want ErrTrunkAlreadyInitialized", err)
	}
	if err := repository.Bootstrap(ctx, intent.ContentRef{Engine: "git", Revision: strings.Repeat("f", 40)}); !errors.Is(err, gitintent.ErrTrunkAlreadyInitialized) {
		t.Fatalf("unavailable second bootstrap error = %v, want ErrTrunkAlreadyInitialized", err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.initial {
		t.Fatalf("trunk after rejected bootstrap = %q, want %q", got, fixture.initial)
	}
}

type gitFixture struct {
	gitDir   string
	initial  string
	proposed string
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runGit(t, "init", "-b", "main", repoDir)
	runGit(t, "-C", repoDir, "config", "user.name", "GitRDone Test")
	runGit(t, "-C", repoDir, "config", "user.email", "gitrdone@example.invalid")

	writeFixtureFile(t, filepath.Join(repoDir, "message.txt"), "initial\n")
	runGit(t, "-C", repoDir, "add", "message.txt")
	runGit(t, "-C", repoDir, "commit", "-m", "initial")
	initial := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")

	writeFixtureFile(t, filepath.Join(repoDir, "message.txt"), "proposed\n")
	runGit(t, "-C", repoDir, "add", "message.txt")
	runGit(t, "-C", repoDir, "commit", "-m", "proposed")
	proposed := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	runGit(t, "-C", repoDir, "reset", "--hard", initial)

	return gitFixture{
		gitDir:   filepath.Join(repoDir, ".git"),
		initial:  initial,
		proposed: proposed,
	}
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
