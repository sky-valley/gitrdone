package httpapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestIntentRepositoryRegistryBindsControlRepoToGitAndFilesystemLedger(t *testing.T) {
	ctx := context.Background()
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage
	repo, err := repos.CreateRepo(ctx, createRepoInput{Namespace: "acme", Name: "judged", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	gitDir, err := storage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		t.Fatalf("bare repo path: %v", err)
	}
	initialCommit := seedBareRepo(t, gitDir)

	registry := newIntentRepositoryRegistry(storageRoot, repos, storage)
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("close registry: %v", err)
		}
	})
	resolved, err := registry.Resolve(ctx, formatRepoControlID(repo.ID))
	if err != nil {
		t.Fatalf("resolve intent repository: %v", err)
	}
	current := resolved.CurrentIntent()
	if current.ID == "" || current.Content.Engine != "git" || current.Content.Revision != initialCommit {
		t.Fatalf("current intent = %#v, want git:%s", current, initialCommit)
	}
	resolvedAgain, err := registry.Resolve(ctx, formatRepoControlID(repo.ID))
	if err != nil {
		t.Fatalf("resolve intent repository again: %v", err)
	}
	if resolvedAgain != resolved {
		t.Fatal("registry returned a different live repository for the same repo id")
	}

	journalPath := filepath.Join(storageRoot, "intent", repo.ID, "ledger.jsonl")
	if info, err := os.Stat(journalPath); err != nil || info.IsDir() {
		t.Fatalf("intent journal stat = %#v, %v; want file", info, err)
	}

	if _, err := registry.Resolve(ctx, "repo_missing"); err != intentservice.ErrRepositoryNotFound {
		t.Fatalf("missing repository error = %v, want ErrRepositoryNotFound", err)
	}
}

func seedBareRepo(t *testing.T, gitDir string) string {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "seed")
	runIntentTestGit(t, "clone", gitDir, worktree)
	runIntentTestGit(t, "-C", worktree, "config", "user.name", "GitRDone Test")
	runIntentTestGit(t, "-C", worktree, "config", "user.email", "gitrdone@example.invalid")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runIntentTestGit(t, "-C", worktree, "add", "README.md")
	runIntentTestGit(t, "-C", worktree, "commit", "-m", "initial")
	runIntentTestGit(t, "-C", worktree, "push", "origin", "HEAD:main")
	return strings.TrimSpace(runIntentTestGit(t, "-C", worktree, "rev-parse", "HEAD"))
}

func runIntentTestGit(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
