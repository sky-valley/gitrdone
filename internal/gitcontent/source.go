package gitcontent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
)

const (
	maxGuidanceBytes    = 256 * 1024
	maxChangedPathBytes = 128 * 1024
	maxPatchBytes       = 1024 * 1024
	maxGitErrorBytes    = 4 * 1024
)

var fullObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type RepositoryLocator interface {
	BareRepoPath(ctx context.Context, repoID string) (string, error)
}

type Source struct {
	locator RepositoryLocator
}

func NewSource(locator RepositoryLocator) (*Source, error) {
	if locator == nil {
		return nil, errors.New("Git content source requires a repository locator")
	}
	return &Source{locator: locator}, nil
}

func (source *Source) ReadText(ctx context.Context, repoID string, content intent.ContentRef, filePath string) (string, error) {
	oid, err := gitObjectID(content)
	if err != nil {
		return "", err
	}
	if !safeTreePath(filePath) {
		return "", errors.New("repository content path must be a clean relative path")
	}
	gitDir, err := source.repositoryPath(ctx, repoID)
	if err != nil {
		return "", err
	}
	output, err := gitOutput(ctx, gitDir, maxGuidanceBytes, "show", oid+":"+filePath)
	if err != nil {
		return "", fmt.Errorf("read repository content %s: %w", filePath, err)
	}
	return output, nil
}

func (source *Source) Compare(ctx context.Context, repoID string, base, candidate intent.ContentRef) (string, error) {
	baseOID, err := gitObjectID(base)
	if err != nil {
		return "", fmt.Errorf("comparison base: %w", err)
	}
	candidateOID, err := gitObjectID(candidate)
	if err != nil {
		return "", fmt.Errorf("comparison candidate: %w", err)
	}
	gitDir, err := source.repositoryPath(ctx, repoID)
	if err != nil {
		return "", err
	}
	diffOptions := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames"}
	changedArgs := append(slices.Clone(diffOptions), "--name-status", baseOID, candidateOID)
	changed, err := gitOutput(ctx, gitDir, maxChangedPathBytes, changedArgs...)
	if err != nil {
		return "", fmt.Errorf("list changed repository paths: %w", err)
	}
	patchArgs := append(slices.Clone(diffOptions), "--stat", "--patch", baseOID, candidateOID)
	patch, err := gitOutput(ctx, gitDir, maxPatchBytes, patchArgs...)
	if err != nil {
		return "", fmt.Errorf("read repository comparison: %w", err)
	}
	if strings.TrimSpace(changed) == "" && strings.TrimSpace(patch) == "" {
		return "", errors.New("repository comparison is empty")
	}
	return "Changed paths:\n" + changed + "\n\nPatch:\n" + patch, nil
}

func (source *Source) repositoryPath(ctx context.Context, repoID string) (string, error) {
	if strings.TrimSpace(repoID) == "" {
		return "", errors.New("repository id is required")
	}
	gitDir, err := source.locator.BareRepoPath(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("locate repository content: %w", err)
	}
	if strings.TrimSpace(gitDir) == "" {
		return "", errors.New("repository content location is empty")
	}
	return gitDir, nil
}

func gitObjectID(content intent.ContentRef) (string, error) {
	if content.Engine != "git" {
		return "", fmt.Errorf("content engine %q is not Git", content.Engine)
	}
	if !fullObjectID.MatchString(content.Revision) {
		return "", errors.New("Git content revision must be a full lowercase object id")
	}
	return content.Revision, nil
}

func safeTreePath(filePath string) bool {
	return filePath != "" &&
		filePath == strings.TrimSpace(filePath) &&
		!strings.HasPrefix(filePath, "/") &&
		!strings.Contains(filePath, ":") &&
		path.Clean(filePath) == filePath &&
		filePath != "." &&
		!strings.HasPrefix(filePath, "../")
}

func gitOutput(ctx context.Context, gitDir string, limit int, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"--git-dir", gitDir}, args...)...)
	command.Env = gitEnvironment()
	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: maxGitErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, message)
	}
	if stdout.overflow {
		return "", fmt.Errorf("Git output exceeds %d bytes", limit)
	}
	return stdout.String(), nil
}

func gitEnvironment() []string {
	environment := []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"LC_ALL=C",
		"LANG=C",
	}
	if value := strings.TrimSpace(os.Getenv("PATH")); value != "" {
		environment = append(environment, "PATH="+value)
	}
	return environment
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining < len(value) {
		buffer.overflow = true
		if remaining < 0 {
			remaining = 0
		}
		value = value[:remaining]
	}
	_, _ = buffer.buffer.Write(value)
	return originalLength, nil
}

func (buffer *limitedBuffer) String() string {
	return buffer.buffer.String()
}
