package httpapi

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
	"github.com/sky-valley/gitrdone/internal/judgement"
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

func TestIntentRepositoryRegistryBootstrapSurvivesRestartAndCannotBeReplaced(t *testing.T) {
	ctx := context.Background()
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage
	repo, err := repos.CreateRepo(ctx, createRepoInput{Namespace: "acme", Name: "bootstrap", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	gitDir, err := storage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		t.Fatalf("bare repo path: %v", err)
	}
	initialCommit := seedBareRepo(t, gitDir)
	runIntentTestGit(t, "--git-dir", gitDir, "update-ref", "refs/candidates/bootstrap", initialCommit)
	runIntentTestGit(t, "--git-dir", gitDir, "update-ref", "-d", "refs/heads/main")
	content := intent.ContentRef{Engine: "git", Revision: initialCommit}

	firstRegistry := newIntentRepositoryRegistry(storageRoot, repos, storage)
	first, err := firstRegistry.Bootstrap(ctx, formatRepoControlID(repo.ID), content)
	if err != nil {
		t.Fatalf("bootstrap intent: %v", err)
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
	retried, err := restartedRegistry.Bootstrap(ctx, formatRepoControlID(repo.ID), content)
	if err != nil {
		t.Fatalf("retry bootstrap after restart: %v", err)
	}
	if retried.ID != first.ID || retried.Content != first.Content {
		t.Fatalf("retried intent = %#v, want %#v", retried, first)
	}
	different := intent.ContentRef{Engine: "git", Revision: strings.Repeat("f", 40)}
	if _, err := restartedRegistry.Bootstrap(ctx, formatRepoControlID(repo.ID), different); !errors.Is(err, intentservice.ErrRepositoryAlreadyInitialized) {
		t.Fatalf("replacement bootstrap error = %v, want ErrRepositoryAlreadyInitialized", err)
	}
	if got := strings.TrimSpace(runIntentTestGit(t, "--git-dir", gitDir, "rev-parse", "refs/heads/main")); got != initialCommit {
		t.Fatalf("main after replacement attempt = %q, want %q", got, initialCommit)
	}
}

func TestIntentRepositoryRegistryListsPendingVersionsGloballyAfterRestart(t *testing.T) {
	ctx := context.Background()
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage

	firstRegistry := newIntentRepositoryRegistry(storageRoot, repos, storage)
	want := make(map[judgement.WorkItem]struct{})
	for index, name := range []string{"alpha", "beta"} {
		repo, err := repos.CreateRepo(ctx, createRepoInput{Namespace: "acme", Name: name, DefaultBranch: "main"})
		if err != nil {
			t.Fatalf("create repo %s: %v", name, err)
		}
		gitDir, err := storage.BareRepoPath(ctx, repo.ID)
		if err != nil {
			t.Fatalf("bare repo path for %s: %v", name, err)
		}
		initialCommit := seedBareRepo(t, gitDir)
		repository, err := firstRegistry.Resolve(ctx, formatRepoControlID(repo.ID))
		if err != nil {
			t.Fatalf("resolve repo %s: %v", name, err)
		}
		proposed, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: "global-pending-" + name,
			BaseIntent:     repository.CurrentIntent().ID,
			Content:        intent.ContentRef{Engine: "git", Revision: initialCommit},
			Producer:       "actor-" + string(rune('a'+index)),
		})
		if err != nil {
			t.Fatalf("propose in repo %s: %v", name, err)
		}
		want[judgement.WorkItem{RepoID: formatRepoControlID(repo.ID), VersionID: proposed.Version.ID}] = struct{}{}
	}
	if err := firstRegistry.Close(); err != nil {
		t.Fatalf("close first registry: %v", err)
	}

	restarted := newIntentRepositoryRegistry(storageRoot, repos, storage)
	t.Cleanup(func() { _ = restarted.Close() })
	pendingSource := newFilesystemPendingSource(restarted)
	got := make(map[judgement.WorkItem]struct{})
	cursor := ""
	for {
		page, err := pendingSource.ListPending(ctx, cursor, 1)
		if err != nil {
			t.Fatalf("list global pending after %q: %v", cursor, err)
		}
		for _, item := range page.Items {
			got[item] = struct{}{}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("global pending = %#v, want %#v", got, want)
	}
	for item := range want {
		if _, found := got[item]; !found {
			t.Fatalf("global pending missing %#v: %#v", item, got)
		}
	}
}

func TestIntentRepositoryRegistryExcludesArchivedReposFromPendingJudgement(t *testing.T) {
	ctx := context.Background()
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage
	repo, err := repos.CreateRepo(ctx, createRepoInput{Namespace: "acme", Name: "archived", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	gitDir, err := storage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		t.Fatalf("bare repo path: %v", err)
	}
	initialCommit := seedBareRepo(t, gitDir)
	registry := newIntentRepositoryRegistry(storageRoot, repos, storage)
	repository, err := registry.Resolve(ctx, formatRepoControlID(repo.ID))
	if err != nil {
		t.Fatalf("resolve repo: %v", err)
	}
	if _, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "archived-pending",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: initialCommit},
		Producer:       "ion",
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := repos.ArchiveRepo(ctx, archiveRepoInput{ID: repo.ID}); err != nil {
		t.Fatalf("archive repo: %v", err)
	}

	if _, err := registry.Resolve(ctx, formatRepoControlID(repo.ID)); !errors.Is(err, intentservice.ErrRepositoryNotFound) {
		t.Fatalf("resolve archived repo error = %v, want repository not found", err)
	}
	page, err := newFilesystemPendingSource(registry).ListPending(ctx, "", 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("pending archived work = %#v, want none", page.Items)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}
}

func TestFilesystemPendingSourceDoesNotClaimVersionsWaitingForHumanReview(t *testing.T) {
	ctx := context.Background()
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage
	repo, err := repos.CreateRepo(ctx, createRepoInput{Namespace: "acme", Name: "held", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	gitDir, err := storage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		t.Fatalf("bare repo path: %v", err)
	}
	initialCommit := seedBareRepo(t, gitDir)
	registry := newIntentRepositoryRegistry(storageRoot, repos, storage)
	t.Cleanup(func() { _ = registry.Close() })
	repository, err := registry.Resolve(ctx, formatRepoControlID(repo.ID))
	if err != nil {
		t.Fatalf("resolve repo: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "human-review-wait",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: initialCommit},
		Producer:       "ion@example.com",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := repository.RecordConcernAssessment(ctx, intent.ConcernAssessment{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		Evaluations: []intent.ConcernEvaluation{{
			Concern:        "architecture-data-infrastructure",
			Prompt:         "Does this change modify architecture, data models, or infrastructure requirements?",
			Reviewer:       "noam@example.com",
			RequiresReview: true,
			Reason:         "the candidate adds a database",
			Evidence:       []string{"migrations/001.sql"},
		}},
	}); err != nil {
		t.Fatalf("record judgement: %v", err)
	}

	pending, err := repository.PendingJudgements(ctx, intent.PendingJudgementQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list repository pending: %v", err)
	}
	if len(pending.Versions) != 1 {
		t.Fatalf("repository pending = %#v, want held Version retained", pending)
	}
	page, err := newFilesystemPendingSource(registry).ListPending(ctx, "", 10)
	if err != nil {
		t.Fatalf("list runnable pending work: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("runnable pending work = %#v, want human-review wait excluded", page.Items)
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
