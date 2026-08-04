package gitcontent_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/gitrdone/internal/gitcontent"
	"github.com/sky-valley/gitrdone/internal/intent"
)

func TestSourceReadsAcceptedGuidanceAndComparesCandidateWithRealGit(t *testing.T) {
	gitDir, accepted, candidate := stageRepository(t)
	source, err := gitcontent.NewSource(staticLocator{path: gitDir})
	if err != nil {
		t.Fatalf("new Git content source: %v", err)
	}
	acceptedRef := intent.ContentRef{Engine: "git", Revision: accepted}
	candidateRef := intent.ContentRef{Engine: "git", Revision: candidate}

	purpose, err := source.ReadText(context.Background(), "repo_cove", acceptedRef, ".gitrdone/purpose.md")
	if err != nil {
		t.Fatalf("read purpose: %v", err)
	}
	if purpose != "calm workplace chat\n" {
		t.Fatalf("purpose = %q", purpose)
	}
	evidence, err := source.Compare(context.Background(), "repo_cove", acceptedRef, candidateRef)
	if err != nil {
		t.Fatalf("compare candidate: %v", err)
	}
	for _, want := range []string{"M\tinternal/store.ts", "diff --git a/internal/store.ts b/internal/store.ts", "+export const database = process.env.DATABASE_URL"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("comparison missing %q:\n%s", want, evidence)
		}
	}
}

func TestSourceRejectsNonGitContentAndUnsafePaths(t *testing.T) {
	source, err := gitcontent.NewSource(staticLocator{path: t.TempDir()})
	if err != nil {
		t.Fatalf("new Git content source: %v", err)
	}
	if _, err := source.ReadText(context.Background(), "repo_cove", intent.ContentRef{Engine: "jj", Revision: strings.Repeat("a", 40)}, ".gitrdone/purpose.md"); err == nil {
		t.Fatal("read non-Git content succeeded")
	}
	if _, err := source.ReadText(context.Background(), "repo_cove", intent.ContentRef{Engine: "git", Revision: strings.Repeat("a", 40)}, "../purpose.md"); err == nil {
		t.Fatal("read traversal-shaped path succeeded")
	}
}

type staticLocator struct {
	path string
}

func (locator staticLocator) BareRepoPath(context.Context, string) (string, error) {
	return locator.path, nil
}

func stageRepository(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	gitDir := filepath.Join(root, "repo.git")
	runGit(t, "init", "--initial-branch=main", worktree)
	runGit(t, "-C", worktree, "config", "user.name", "GitRDone Test")
	runGit(t, "-C", worktree, "config", "user.email", "gitrdone@example.invalid")
	writeFile(t, filepath.Join(worktree, ".gitrdone", "purpose.md"), "calm workplace chat\n")
	writeFile(t, filepath.Join(worktree, ".gitrdone", "priorities.md"), "# priorities\n")
	writeFile(t, filepath.Join(worktree, "internal", "store.ts"), "export const store = 'memory'\n")
	runGit(t, "-C", worktree, "add", ".")
	runGit(t, "-C", worktree, "commit", "-m", "accepted")
	accepted := gitOutput(t, "-C", worktree, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(worktree, "internal", "store.ts"), "export const database = process.env.DATABASE_URL\n")
	runGit(t, "-C", worktree, "add", ".")
	runGit(t, "-C", worktree, "commit", "-m", "candidate")
	candidate := gitOutput(t, "-C", worktree, "rev-parse", "HEAD")
	runGit(t, "clone", "--bare", worktree, gitDir)
	return gitDir, accepted, candidate
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	_ = gitOutput(t, args...)
}

func gitOutput(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
