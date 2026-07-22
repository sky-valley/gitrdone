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

type submissionState struct {
	BaseIntent      string   `json:"baseIntent"`
	IdempotencyKey  string   `json:"idempotencyKey"`
	Dependencies    []string `json:"dependencies,omitempty"`
	DependencyTitle string   `json:"dependencyTitle,omitempty"`
}

type workspaceState struct {
	BaseIntent     string `json:"baseIntent"`
	ParentChange   string `json:"parentChange"`
	ParentVersion  string `json:"parentVersion"`
	ParentRevision string `json:"parentRevision"`
	ParentTitle    string `json:"parentTitle"`
}

func loadSubmissionState(ctx context.Context, workdir string, repoID string, revision string) (submissionState, bool, error) {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--get", submissionStateKey(repoID, revision))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err == nil {
		var state submissionState
		if json.Unmarshal(bytes.TrimSpace(output), &state) != nil || state.BaseIntent == "" || state.IdempotencyKey == "" {
			return submissionState{}, false, errors.New("stored submission retry state is invalid")
		}
		return state, true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return submissionState{}, false, nil
	}
	return submissionState{}, false, fmt.Errorf("read submission retry state: %w", err)
}

func loadWorkspaceState(ctx context.Context, workdir string, repoID string) (workspaceState, bool, error) {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--get", workspaceStateKey(repoID))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err == nil {
		var state workspaceState
		if json.Unmarshal(bytes.TrimSpace(output), &state) != nil || state.BaseIntent == "" || state.ParentChange == "" || state.ParentVersion == "" || state.ParentRevision == "" || state.ParentTitle == "" {
			return workspaceState{}, false, errors.New("stored workspace continuation state is invalid")
		}
		return state, true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return workspaceState{}, false, nil
	}
	return workspaceState{}, false, fmt.Errorf("read workspace continuation state: %w", err)
}

func rememberWorkspaceState(ctx context.Context, workdir string, repoID string, state workspaceState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode workspace continuation state: %w", err)
	}
	if err := gitRun(ctx, workdir, "config", "--local", workspaceStateKey(repoID), string(encoded)); err != nil {
		return fmt.Errorf("store workspace continuation state: %w", err)
	}
	return nil
}

func forgetWorkspaceState(ctx context.Context, workdir string, repoID string) error {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--unset-all", workspaceStateKey(repoID))
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

func rememberSubmissionState(ctx context.Context, workdir string, repoID string, revision string, state submissionState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode submission retry state: %w", err)
	}
	if err := gitRun(ctx, workdir, "config", "--local", submissionStateKey(repoID, revision), string(encoded)); err != nil {
		return fmt.Errorf("store submission retry state: %w", err)
	}
	return nil
}

func submissionStateKey(repoID string, revision string) string {
	digest := sha256.Sum256([]byte(repoID + "\x00" + revision))
	return "grd-submission." + hex.EncodeToString(digest[:]) + ".state"
}

func workspaceStateKey(repoID string) string {
	digest := sha256.Sum256([]byte(repoID))
	return "grd-workspace." + hex.EncodeToString(digest[:]) + ".state"
}

func newIdempotencyKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create submission idempotency key: %w", err)
	}
	return "grd-submit-" + hex.EncodeToString(raw[:]), nil
}
