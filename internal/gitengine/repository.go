package gitengine

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

// admittedRefNamespace retains the original on-disk ref path for compatibility.
// These refs prove content admission; they do not encode a judgement outcome.
const admittedRefNamespace = ReservedRefNamespace + "holding"

var fullObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

var ErrTrunkAlreadyInitialized = errors.New("trunk is already initialized")

type Adapter struct {
	gitDir   string
	trunkRef string
}

func OpenAdapter(ctx context.Context, gitDir, trunkRef string) (*Adapter, error) {
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return nil, errors.New("git directory is required")
	}
	trunkRef = strings.TrimSpace(trunkRef)
	if !strings.HasPrefix(trunkRef, "refs/heads/") {
		return nil, errors.New("trunk ref must be under refs/heads")
	}
	repository := &Adapter{gitDir: gitDir, trunkRef: trunkRef}
	if err := repository.run(ctx, "check-ref-format", trunkRef); err != nil {
		return nil, fmt.Errorf("validate trunk ref: %w", err)
	}
	if err := repository.run(
		ctx,
		"config",
		"--local",
		"--replace-all",
		"transfer.hideRefs",
		admittedRefNamespace,
		"^"+regexp.QuoteMeta(admittedRefNamespace)+"$",
	); err != nil {
		return nil, fmt.Errorf("hide admitted-content refs: %w", err)
	}
	return repository, nil
}

func (repository *Adapter) Admit(ctx context.Context, versionID intent.VersionID, content intent.ContentRef) error {
	oid, err := repository.admissibleCommit(ctx, content)
	if err != nil {
		return err
	}

	admittedRef := admittedRefNamespace + "/" + string(versionID)
	if err := repository.run(ctx, "check-ref-format", admittedRef); err != nil {
		return fmt.Errorf("validate admitted-content ref: %w", err)
	}
	current, found, err := repository.readRef(ctx, admittedRef)
	if err != nil {
		return fmt.Errorf("read admitted-content ref: %w", err)
	}
	if found {
		if current == oid {
			return nil
		}
		return errors.New("admitted-content ref already contains different content")
	}
	if err := repository.run(ctx, "update-ref", admittedRef, oid, ""); err != nil {
		current, found, readErr := repository.readRef(ctx, admittedRef)
		if readErr == nil && found && current == oid {
			return nil
		}
		return fmt.Errorf("create admitted-content ref: %w", err)
	}
	return nil
}

func (repository *Adapter) Bootstrap(ctx context.Context, content intent.ContentRef) error {
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

func (repository *Adapter) admissibleCommit(ctx context.Context, content intent.ContentRef) (string, error) {
	oid, err := gitObjectID(content)
	if err != nil {
		return "", fmt.Errorf("%w: %v", intent.ErrContentNotAdmissible, err)
	}
	if err := repository.validateCommit(ctx, oid); err != nil {
		return "", err
	}
	return oid, nil
}

func (repository *Adapter) validateCommit(ctx context.Context, oid string) error {
	if err := repository.run(ctx, "cat-file", "-e", oid+"^{commit}"); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%w: proposed git commit is unavailable", intent.ErrContentNotAdmissible)
		}
		return fmt.Errorf("validate proposed git commit: %w", err)
	}
	return nil
}

func (repository *Adapter) Current(ctx context.Context) (intent.ContentRef, error) {
	current, found, err := repository.readRef(ctx, repository.trunkRef)
	if err != nil {
		return intent.ContentRef{}, fmt.Errorf("read trunk ref: %w", err)
	}
	if !found {
		return intent.ContentRef{}, errors.New("trunk ref not found")
	}
	return intent.ContentRef{Engine: "git", Revision: current}, nil
}

func (repository *Adapter) Advance(ctx context.Context, expected, next intent.ContentRef) error {
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

func (repository *Adapter) readRef(ctx context.Context, ref string) (string, bool, error) {
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

func (repository *Adapter) run(ctx context.Context, args ...string) error {
	_, err := repository.output(ctx, args...)
	return err
}

func (repository *Adapter) output(ctx context.Context, args ...string) (string, error) {
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
