package grdclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

type submissionRetryState struct {
	BaseIntent      string   `json:"baseIntent"`
	IdempotencyKey  string   `json:"idempotencyKey"`
	Dependencies    []string `json:"dependencies,omitempty"`
	DependencyTitle string   `json:"dependencyTitle,omitempty"`
}

type continuationState struct {
	BaseIntent     string `json:"baseIntent"`
	ParentChange   string `json:"parentChange"`
	ParentVersion  string `json:"parentVersion"`
	ParentRevision string `json:"parentRevision"`
	ParentTitle    string `json:"parentTitle"`
	ConflictID     string `json:"conflictId,omitempty"`
}

func loadSubmissionRetryState(ctx context.Context, workdir string, repoID string, revision string) (submissionRetryState, bool, error) {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--get", submissionRetryStateKey(repoID, revision))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err == nil {
		var state submissionRetryState
		if json.Unmarshal(bytes.TrimSpace(output), &state) != nil || state.BaseIntent == "" || state.IdempotencyKey == "" {
			return submissionRetryState{}, false, errors.New("stored submission retry state is invalid")
		}
		return state, true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return submissionRetryState{}, false, nil
	}
	return submissionRetryState{}, false, fmt.Errorf("read submission retry state: %w", err)
}

func loadContinuationState(ctx context.Context, workdir string, repoID string) (continuationState, bool, error) {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--get", continuationStateKey(repoID))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err == nil {
		var state continuationState
		if json.Unmarshal(bytes.TrimSpace(output), &state) != nil || state.BaseIntent == "" || state.ParentChange == "" || state.ParentVersion == "" || state.ParentRevision == "" || state.ParentTitle == "" {
			return continuationState{}, false, errors.New("stored workspace continuation state is invalid")
		}
		return state, true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return continuationState{}, false, nil
	}
	return continuationState{}, false, fmt.Errorf("read workspace continuation state: %w", err)
}

func rememberContinuationState(ctx context.Context, workdir string, repoID string, state continuationState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode workspace continuation state: %w", err)
	}
	if err := gitRun(ctx, workdir, "config", "--local", continuationStateKey(repoID), string(encoded)); err != nil {
		return fmt.Errorf("store workspace continuation state: %w", err)
	}
	return nil
}

func forgetContinuationState(ctx context.Context, workdir string, repoID string) error {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--unset-all", continuationStateKey(repoID))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 5 {
			return nil
		}
		return fmt.Errorf("clear workspace continuation state: %w", err)
	}
	return nil
}

func rememberSubmissionRetryState(ctx context.Context, workdir string, repoID string, revision string, state submissionRetryState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode submission retry state: %w", err)
	}
	if err := gitRun(ctx, workdir, "config", "--local", submissionRetryStateKey(repoID, revision), string(encoded)); err != nil {
		return fmt.Errorf("store submission retry state: %w", err)
	}
	return nil
}

func submissionRetryStateKey(repoID string, revision string) string {
	digest := sha256.Sum256([]byte(repoID + "\x00" + revision))
	return "grd-submission." + hex.EncodeToString(digest[:]) + ".state"
}

func continuationStateKey(repoID string) string {
	digest := sha256.Sum256([]byte(repoID))
	return "grd-workspace." + hex.EncodeToString(digest[:]) + ".state"
}

func reconciliationDescendantIdempotencyKey(repoID, fromVersion, toVersion, descendantRevision string) string {
	return reconciliationIdempotencyKey("descendant", repoID, fromVersion, toVersion, descendantRevision)
}

func reconciliationConflictIdempotencyKey(repoID, fromVersion, toVersion, descendantVersion, expectedIntent string) string {
	return reconciliationIdempotencyKey("record", repoID, fromVersion, toVersion, descendantVersion+"\x00"+expectedIntent)
}

func reconciliationIdempotencyKey(operation, repoID, fromVersion, toVersion, descendant string) string {
	digest := sha256.Sum256([]byte(operation + "\x00" + repoID + "\x00" + fromVersion + "\x00" + toVersion + "\x00" + descendant))
	return "grd-conflict-" + operation + "-" + hex.EncodeToString(digest[:])
}

func newIdempotencyKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create submission idempotency key: %w", err)
	}
	return "grd-submit-" + hex.EncodeToString(raw[:]), nil
}
