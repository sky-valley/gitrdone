package gitintent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
)

const maxGitErrorBytes = 4 * 1024
const ReservedRefNamespace = "refs/gitrdone/"
const holdingNamespace = ReservedRefNamespace + "holding"

var fullObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

var ErrTrunkAlreadyInitialized = errors.New("trunk is already initialized")

type Repository struct {
	gitDir   string
	trunkRef string
}

func OpenRepository(ctx context.Context, gitDir, trunkRef string) (*Repository, error) {
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return nil, errors.New("git directory is required")
	}
	trunkRef = strings.TrimSpace(trunkRef)
	if !strings.HasPrefix(trunkRef, "refs/heads/") {
		return nil, errors.New("trunk ref must be under refs/heads")
	}
	repository := &Repository{gitDir: gitDir, trunkRef: trunkRef}
	if err := repository.run(ctx, "check-ref-format", trunkRef); err != nil {
		return nil, fmt.Errorf("validate trunk ref: %w", err)
	}
	if err := repository.run(
		ctx,
		"config",
		"--local",
		"--replace-all",
		"transfer.hideRefs",
		holdingNamespace,
		"^"+regexp.QuoteMeta(holdingNamespace)+"$",
	); err != nil {
		return nil, fmt.Errorf("hide holding refs: %w", err)
	}
	return repository, nil
}

func (repository *Repository) Admit(ctx context.Context, versionID intent.VersionID, content intent.ContentRef) error {
	oid, err := repository.admissibleCommit(ctx, content)
	if err != nil {
		return err
	}

	holdingRef := holdingNamespace + "/" + string(versionID)
	if err := repository.run(ctx, "check-ref-format", holdingRef); err != nil {
		return fmt.Errorf("validate holding ref: %w", err)
	}
	current, found, err := repository.readRef(ctx, holdingRef)
	if err != nil {
		return fmt.Errorf("read holding ref: %w", err)
	}
	if found {
		if current == oid {
			return nil
		}
		return errors.New("holding ref already contains different content")
	}
	if err := repository.run(ctx, "update-ref", holdingRef, oid, ""); err != nil {
		current, found, readErr := repository.readRef(ctx, holdingRef)
		if readErr == nil && found && current == oid {
			return nil
		}
		return fmt.Errorf("create holding ref: %w", err)
	}
	return nil
}

func (repository *Repository) Bootstrap(ctx context.Context, content intent.ContentRef) error {
	oid, err := gitObjectID(content)
	if err != nil {
		return fmt.Errorf("%w: %v", intent.ErrContentNotAdmissible, err)
	}
	current, found, err := repository.readRef(ctx, repository.trunkRef)
	if err != nil {
		return fmt.Errorf("read trunk ref: %w", err)
	}
	if found {
		if current == oid {
			return nil
		}
		return ErrTrunkAlreadyInitialized
	}
	if err := repository.validateCommit(ctx, oid); err != nil {
		return err
	}
	if err := repository.run(ctx, "update-ref", repository.trunkRef, oid, ""); err != nil {
		current, found, readErr := repository.readRef(ctx, repository.trunkRef)
		if readErr == nil && found {
			if current == oid {
				return nil
			}
			return ErrTrunkAlreadyInitialized
		}
		return fmt.Errorf("initialize trunk ref: %w", err)
	}
	return nil
}

func (repository *Repository) admissibleCommit(ctx context.Context, content intent.ContentRef) (string, error) {
	oid, err := gitObjectID(content)
	if err != nil {
		return "", fmt.Errorf("%w: %v", intent.ErrContentNotAdmissible, err)
	}
	if err := repository.validateCommit(ctx, oid); err != nil {
		return "", err
	}
	return oid, nil
}

func (repository *Repository) validateCommit(ctx context.Context, oid string) error {
	if err := repository.run(ctx, "cat-file", "-e", oid+"^{commit}"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%w: proposed git commit is unavailable", intent.ErrContentNotAdmissible)
		}
		return fmt.Errorf("validate proposed git commit: %w", err)
	}
	return nil
}

func (repository *Repository) Current(ctx context.Context) (intent.ContentRef, error) {
	current, found, err := repository.readRef(ctx, repository.trunkRef)
	if err != nil {
		return intent.ContentRef{}, fmt.Errorf("read trunk ref: %w", err)
	}
	if !found {
		return intent.ContentRef{}, errors.New("trunk ref not found")
	}
	return intent.ContentRef{Engine: "git", Revision: current}, nil
}

func (repository *Repository) Advance(ctx context.Context, expected, next intent.ContentRef) error {
	expectedOID, err := gitObjectID(expected)
	if err != nil {
		return fmt.Errorf("expected trunk content: %w", err)
	}
	nextOID, err := gitObjectID(next)
	if err != nil {
		return fmt.Errorf("next trunk content: %w", err)
	}
	if err := repository.run(ctx, "cat-file", "-e", nextOID+"^{commit}"); err != nil {
		return fmt.Errorf("validate next git commit: %w", err)
	}

	current, found, err := repository.readRef(ctx, repository.trunkRef)
	if err != nil {
		return fmt.Errorf("read trunk ref: %w", err)
	}
	if !found || current != expectedOID {
		return intent.ErrIntentAdvanced
	}
	if err := repository.run(ctx, "update-ref", repository.trunkRef, nextOID, expectedOID); err != nil {
		current, found, readErr := repository.readRef(ctx, repository.trunkRef)
		if readErr == nil && (!found || current != expectedOID) {
			return intent.ErrIntentAdvanced
		}
		return fmt.Errorf("advance trunk ref: %w", err)
	}
	return nil
}

func gitObjectID(content intent.ContentRef) (string, error) {
	if content.Engine != "git" {
		return "", fmt.Errorf("content engine %q is not git", content.Engine)
	}
	if !fullObjectID.MatchString(content.Revision) {
		return "", errors.New("git content revision must be a full lowercase object id")
	}
	return content.Revision, nil
}

func (repository *Repository) readRef(ctx context.Context, ref string) (string, bool, error) {
	output, err := repository.output(ctx, "rev-parse", "--verify", "--quiet", ref)
	if err == nil {
		return strings.TrimSpace(output), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, err
}

func (repository *Repository) run(ctx context.Context, args ...string) error {
	_, err := repository.output(ctx, args...)
	return err
}

func (repository *Repository) output(ctx context.Context, args ...string) (string, error) {
	gitArgs := append([]string{"--git-dir", repository.gitDir}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = gitProcessEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", contextErr
		}
		message := strings.TrimSpace(string(output))
		if len(message) > maxGitErrorBytes {
			message = message[:maxGitErrorBytes]
		}
		if message != "" {
			return "", fmt.Errorf("git command failed: %w: %s", err, message)
		}
		return "", fmt.Errorf("git command failed: %w", err)
	}
	return string(output), nil
}

func gitProcessEnv() []string {
	env := make([]string, 0, 4)
	if path := strings.TrimSpace(os.Getenv("PATH")); path != "" {
		env = append(env, "PATH="+path)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
}
