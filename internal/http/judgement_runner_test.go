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
	"github.com/sky-valley/gitrdone/internal/judgement"
)

const coveRunnerPurpose = "A calm workplace chat application for team channels, direct messages, and presence, deliberately designed without notification-driven urgency."

const coveRunnerPriorities = `# Priorities

## architecture-data-infrastructure
Reviewer: noam@skyvalley.ac
Prompt: Does this change alter architecture, data models, or infrastructure requirements such as databases, environment variables, services, or deployment topology?
`

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

	resources, err := buildServer(Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
		StorageRoot:   storageRoot,
	}, PendingRuntimeConfig{Workers: 1, ProcessorFactory: judgement.NewApproveAllProcessorFactory()}, repos, newMemoryIdempotencyStore(nil), storage)
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
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

func TestServerPendingRunnerRequiresExplicitProcessorFactory(t *testing.T) {
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage
	if _, err := buildServer(Config{ControlBearer: "admin", StorageRoot: storageRoot}, PendingRuntimeConfig{Workers: 1}, repos, newMemoryIdempotencyStore(nil), storage); err == nil {
		t.Fatal("server started judgement workers without an explicit processor factory")
	}
}

func TestServerPendingRunnerHoldsRepositoryGovernedConcernUntilAssignedApproval(t *testing.T) {
	ctx := context.Background()
	storageRoot := t.TempDir()
	storage := newFilesystemGitStorage(storageRoot)
	repos := newMemoryRepoStore(nil)
	repos.gitStorage = storage
	repo, err := repos.CreateRepo(ctx, createRepoInput{Namespace: "skyvalley", Name: "cove", DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	gitDir, err := storage.BareRepoPath(ctx, repo.ID)
	if err != nil {
		t.Fatalf("bare repo path: %v", err)
	}
	seedCoveJudgementRepo(t, gitDir)
	candidate := commitRunnerCandidate(t, gitDir)
	evaluator := &runtimeConcernEvaluator{result: judgement.ConcernResult{
		RequiresReview: true,
		Reason:         "candidate adds a database requirement",
		Evidence:       []string{"candidate.txt"},
	}}
	resources, err := buildServer(Config{
		BaseURL:       "https://git.example.com",
		ControlBearer: "internal-admin-token",
		StorageRoot:   storageRoot,
	}, PendingRuntimeConfig{
		Workers:          1,
		ProcessorFactory: judgement.NewConcernProcessorFactory(evaluator),
	}, repos, newMemoryIdempotencyStore(nil), storage)
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	t.Cleanup(func() { _ = resources.close() })
	initial, err := resources.judgement.CurrentIntent(ctx, formatRepoControlID(repo.ID))
	if err != nil {
		t.Fatalf("read initial intent: %v", err)
	}
	proposed, err := resources.judgement.Propose(ctx, formatRepoControlID(repo.ID), intentservice.Proposal{
		IdempotencyKey: "judged-runner-proposal",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: candidate},
		Producer:       "ion@skyvalley.ac",
	})
	if err != nil {
		t.Fatalf("propose candidate: %v", err)
	}

	assessed := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		assessment, found, err := resources.judgement.ConcernAssessment(ctx, formatRepoControlID(repo.ID), proposed.Version.ID)
		if err != nil {
			t.Fatalf("read concern assessment: %v", err)
		}
		if found {
			obligations := assessment.ReviewObligations()
			if len(obligations) != 1 || obligations[0].Reviewer != "noam@skyvalley.ac" {
				t.Fatalf("review obligations = %#v", obligations)
			}
			current, err := resources.judgement.CurrentIntent(ctx, formatRepoControlID(repo.ID))
			if err != nil {
				t.Fatalf("read held intent: %v", err)
			}
			if current.ID != initial.ID {
				t.Fatalf("current intent = %q, want held at %q", current.ID, initial.ID)
			}
			if evaluator.request.Purpose != coveRunnerPurpose+"\n" || !strings.Contains(evaluator.request.ChangeEvidence, "candidate.txt") {
				t.Fatalf("evaluator request = %#v", evaluator.request)
			}
			assessed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !assessed {
		t.Fatalf("pending Version %q was not assessed", proposed.Version.ID)
	}
	if _, err := resources.judgement.RecordReviewResponse(ctx, formatRepoControlID(repo.ID), intent.ReviewResponseRequest{
		IdempotencyKey: "approve-judged-runner-proposal",
		VersionID:      proposed.Version.ID,
		Concern:        "architecture-data-infrastructure",
		Reviewer:       "noam@skyvalley.ac",
		Decision:       intent.ReviewApproved,
		Rationale:      "database requirement reviewed",
	}); err != nil {
		t.Fatalf("record assigned approval: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := resources.judgement.CurrentIntent(ctx, formatRepoControlID(repo.ID))
		if err != nil {
			t.Fatalf("read approved intent: %v", err)
		}
		if current.Content.Revision == candidate {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("approved Version %q was not promoted", proposed.Version.ID)
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

func seedCoveJudgementRepo(t *testing.T, gitDir string) string {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "cove-seed")
	runIntentTestGit(t, "clone", gitDir, worktree)
	runIntentTestGit(t, "-C", worktree, "config", "user.name", "GitRDone Test")
	runIntentTestGit(t, "-C", worktree, "config", "user.email", "gitrdone@example.invalid")
	if err := os.MkdirAll(filepath.Join(worktree, ".gitrdone"), 0o700); err != nil {
		t.Fatalf("create governing directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".gitrdone", "purpose.md"), []byte(coveRunnerPurpose+"\n"), 0o600); err != nil {
		t.Fatalf("write purpose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".gitrdone", "priorities.md"), []byte(coveRunnerPriorities), 0o600); err != nil {
		t.Fatalf("write priorities: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runIntentTestGit(t, "-C", worktree, "add", ".")
	runIntentTestGit(t, "-C", worktree, "commit", "-m", "initial judged repository")
	runIntentTestGit(t, "-C", worktree, "push", "origin", "HEAD:main")
	return strings.TrimSpace(runIntentTestGit(t, "-C", worktree, "rev-parse", "HEAD"))
}

type runtimeConcernEvaluator struct {
	request judgement.ConcernRequest
	result  judgement.ConcernResult
}

func (evaluator *runtimeConcernEvaluator) Evaluate(_ context.Context, request judgement.ConcernRequest) (judgement.ConcernResult, error) {
	evaluator.request = request
	result := evaluator.result
	result.Provenance = intent.EvaluatorProvenance{
		Evaluator:        "test://runtime-concern-evaluator",
		ContractRevision: "test.concern-assessment/v1",
	}
	return result, nil
}
