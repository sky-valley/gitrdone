package httpapi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultGitStorageRoot = ".storage"

type repoGitStorage interface {
	InitBareRepo(ctx context.Context, repoID string, defaultBranch string) error
	BareRepoPath(ctx context.Context, repoID string) (string, error)
}

type noopRepoGitStorage struct{}

func (noopRepoGitStorage) InitBareRepo(ctx context.Context, repoID string, defaultBranch string) error {
	return nil
}

func (noopRepoGitStorage) BareRepoPath(ctx context.Context, repoID string) (string, error) {
	return "", errRepoStorageNotFound
}

type filesystemGitStorage struct {
	root string
}

func newFilesystemGitStorage(root string) filesystemGitStorage {
	root = strings.TrimSpace(root)
	if root == "" {
		root = defaultGitStorageRoot
	}
	return filesystemGitStorage{root: root}
}

func (storage filesystemGitStorage) InitBareRepo(ctx context.Context, repoID string, defaultBranch string) error {
	repoPath := storage.repoPath(repoID)
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o700); err != nil {
		return fmt.Errorf("create git storage root: %w", err)
	}
	if err := runGit(ctx, "init", "--bare", repoPath); err != nil {
		return err
	}
	if err := runGit(ctx, "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch); err != nil {
		return err
	}
	return nil
}

func (storage filesystemGitStorage) BareRepoPath(ctx context.Context, repoID string) (string, error) {
	repoPath := storage.repoPath(repoID)
	info, err := os.Stat(repoPath)
	if err != nil {
		return "", errRepoStorageNotFound
	}
	if !info.IsDir() {
		return "", errRepoStorageNotFound
	}
	return repoPath, nil
}

func (storage filesystemGitStorage) repoPath(repoID string) string {
	return filepath.Join(storage.root, "repos", repoID+".git")
}

func runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, message)
	}
	return nil
}
