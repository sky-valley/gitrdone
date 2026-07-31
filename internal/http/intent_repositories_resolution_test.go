package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestReconciliationResolutionRemainsPendingUntilExplicitPromotionAndSurvivesRegistryRestart(t *testing.T) {
	ctx := context.Background()
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage
	repo, err := repos.CreateRepo(ctx, createRepoInput{Namespace: "acme", Name: "resolved", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	gitDir, err := storage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		t.Fatalf("bare repo path: %v", err)
	}
	initialCommit := seedBareRepo(t, gitDir)
	worktree := filepath.Join(t.TempDir(), "work")
	runIntentTestGit(t, "clone", gitDir, worktree)
	runIntentTestGit(t, "-C", worktree, "config", "user.name", "GitRDone Test")
	runIntentTestGit(t, "-C", worktree, "config", "user.email", "gitrdone@example.invalid")

	writeResolutionFixtureFile(t, worktree, "feature.txt", "unsafe B\n")
	runIntentTestGit(t, "-C", worktree, "add", "feature.txt")
	runIntentTestGit(t, "-C", worktree, "commit", "-m", "B")
	originalCommit := strings.TrimSpace(runIntentTestGit(t, "-C", worktree, "rev-parse", "HEAD"))
	writeResolutionFixtureFile(t, worktree, "feature.txt", "conflicting C\n")
	runIntentTestGit(t, "-C", worktree, "commit", "-am", "C")
	descendantCommit := strings.TrimSpace(runIntentTestGit(t, "-C", worktree, "rev-parse", "HEAD"))

	runIntentTestGit(t, "-C", worktree, "checkout", "-b", "b-prime", initialCommit)
	writeResolutionFixtureFile(t, worktree, "feature.txt", "repository B prime\n")
	runIntentTestGit(t, "-C", worktree, "add", "feature.txt")
	runIntentTestGit(t, "-C", worktree, "commit", "-m", "B prime")
	amendedCommit := strings.TrimSpace(runIntentTestGit(t, "-C", worktree, "rev-parse", "HEAD"))
	runIntentTestGit(t, "-C", worktree, "checkout", "-b", "c-prime")
	writeResolutionFixtureFile(t, worktree, "feature.txt", "judged C prime\n")
	runIntentTestGit(t, "-C", worktree, "commit", "-am", "C prime")
	resolvedCommit := strings.TrimSpace(runIntentTestGit(t, "-C", worktree, "rev-parse", "HEAD"))
	runIntentTestGit(t, "-C", worktree, "push", "origin",
		originalCommit+":refs/candidates/B",
		descendantCommit+":refs/candidates/C",
		amendedCommit+":refs/candidates/B-prime",
		resolvedCommit+":refs/candidates/C-prime",
	)

	firstRegistry := newIntentRepositoryRegistry(storageRoot, repos, storage)
	repositoryService, err := firstRegistry.Resolve(ctx, formatRepoControlID(repo.ID))
	if err != nil {
		t.Fatalf("resolve repository: %v", err)
	}
	repository := repositoryService
	initial := repository.CurrentIntent()
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: originalCommit},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose B: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: amendedCommit},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend B: %v", err)
	}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: initial.ID,
	})
	if err != nil {
		t.Fatalf("promote B prime: %v", err)
	}
	descendant, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: descendantCommit},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose C: %v", err)
	}
	conflict, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-b-c",
		FromVersion:       original.Version.ID,
		ToVersion:         amended.Version.ID,
		DescendantVersion: descendant.Version.ID,
		ExpectedIntent:    repository.CurrentIntent().ID,
		ReportedBy:        "ion",
		AffectedPaths:     []string{"feature.txt"},
	})
	if err != nil {
		t.Fatalf("record conflict: %v", err)
	}
	request := intentservice.ReconciliationResolutionRequest{
		IdempotencyKey:  "resolve-b-c",
		ConflictID:      conflict.ID,
		ExpectedVersion: descendant.Version.ID,
		ExpectedIntent:  promoted.Intent.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: resolvedCommit},
		Producer:        "repository-agent",
		ResolvedBy:      "judgement-agent",
		Rationale:       "resolved the competing feature edits",
	}
	judgement := intentservice.New(firstRegistry)

	resolved, err := judgement.ResolveReconciliationConflict(ctx, formatRepoControlID(repo.ID), request)
	if err != nil {
		t.Fatalf("resolve conflict: %v", err)
	}
	if resolved.Change.ID != descendant.Change.ID || resolved.Version.ID == descendant.Version.ID {
		t.Fatalf("resolved identity = %#v, want new version of C", resolved)
	}
	if got := strings.TrimSpace(runIntentTestGit(t, "--git-dir", gitDir, "rev-parse", "refs/heads/main")); got != amendedCommit {
		t.Fatalf("canonical trunk before judgement = %q, want B prime %q", got, amendedCommit)
	}
	resolutionPromotion, err := judgement.Promote(ctx, formatRepoControlID(repo.ID), intent.PromoteRequest{
		VersionID:      resolved.Version.ID,
		ExpectedIntent: promoted.Intent.ID,
	})
	if err != nil {
		t.Fatalf("promote resolved conflict: %v", err)
	}
	if resolutionPromotion.Promotion.VersionID != resolved.Version.ID {
		t.Fatalf("resolution promotion = %#v, want C prime", resolutionPromotion)
	}
	if got := strings.TrimSpace(runIntentTestGit(t, "--git-dir", gitDir, "rev-parse", "refs/heads/main")); got != resolvedCommit {
		t.Fatalf("canonical trunk = %q, want resolved C prime %q", got, resolvedCommit)
	}
	if err := firstRegistry.Close(); err != nil {
		t.Fatalf("close first registry: %v", err)
	}

	restartedRegistry := newIntentRepositoryRegistry(storageRoot, repos, storage)
	t.Cleanup(func() {
		if err := restartedRegistry.Close(); err != nil {
			t.Errorf("close restarted registry: %v", err)
		}
	})
	restartedJudgement := intentservice.New(restartedRegistry)
	retried, err := restartedJudgement.ResolveReconciliationConflict(ctx, formatRepoControlID(repo.ID), request)
	if err != nil {
		t.Fatalf("retry resolution after registry restart: %v", err)
	}
	if !reflect.DeepEqual(retried, resolved) {
		t.Fatalf("retried resolution = %#v, want %#v", retried, resolved)
	}
	restartedRepositoryService, err := restartedRegistry.Resolve(ctx, formatRepoControlID(repo.ID))
	if err != nil {
		t.Fatalf("resolve restarted repository: %v", err)
	}
	restartedRepository := restartedRepositoryService
	loaded, found, err := restartedRepository.ReconciliationConflict(ctx, conflict.ID)
	if err != nil || !found || loaded.Resolution == nil {
		t.Fatalf("restored conflict = %#v, %t, %v; want durable resolution", loaded, found, err)
	}
	if restartedRepository.CurrentIntent().Content.Revision != resolvedCommit {
		t.Fatalf("restored current intent = %#v, want C prime %q", restartedRepository.CurrentIntent(), resolvedCommit)
	}
}

func writeResolutionFixtureFile(t *testing.T, worktree, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
