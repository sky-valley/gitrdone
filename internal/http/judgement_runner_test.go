package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestServerPendingRunnerPromotesPendingVersionAsynchronously(t *testing.T) {
	ctx := context.Background()
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage
	repo, err := repos.CreateRepo(ctx, createRepoInput{Namespace: "acme", Name: "coordinated", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	gitDir, err := storage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		t.Fatalf("bare repo path: %v", err)
	}
	seedBareRepo(t, gitDir)
	candidate := commitRunnerCandidate(t, gitDir)

	resources := buildServer(Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
		StorageRoot:   storageRoot,
	}, PendingRuntimeConfig{Workers: 1}, repos, newMemoryIdempotencyStore(nil), storage)
	t.Cleanup(func() { _ = resources.close() })
	initial, err := resources.judgement.CurrentIntent(ctx, formatRepoControlID(repo.ID))
	if err != nil {
		t.Fatalf("read initial intent: %v", err)
	}
	proposed, err := resources.judgement.Propose(ctx, formatRepoControlID(repo.ID), intentservice.Proposal{
		IdempotencyKey: "runner-proposal",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: candidate},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose candidate: %v", err)
	}
	if proposed.Version.ID == "" {
		t.Fatal("proposal did not return a Version")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := resources.judgement.CurrentIntent(ctx, formatRepoControlID(repo.ID))
		if err != nil {
			t.Fatalf("read current intent: %v", err)
		}
		if current.Content.Revision == candidate {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending Version %q was not promoted by the runner", proposed.Version.ID)
}

func commitRunnerCandidate(t *testing.T, gitDir string) string {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "candidate")
	runIntentTestGit(t, "clone", gitDir, worktree)
	runIntentTestGit(t, "-C", worktree, "config", "user.name", "GitRDone Test")
	runIntentTestGit(t, "-C", worktree, "config", "user.email", "gitrdone@example.invalid")
	if err := os.WriteFile(filepath.Join(worktree, "candidate.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	runIntentTestGit(t, "-C", worktree, "add", "candidate.txt")
	runIntentTestGit(t, "-C", worktree, "commit", "-m", "candidate")
	runIntentTestGit(t, "-C", worktree, "push", "origin", "HEAD:refs/candidates/runner")
	return strings.TrimSpace(runIntentTestGit(t, "-C", worktree, "rev-parse", "HEAD"))
}
