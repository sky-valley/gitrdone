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

type reviewResponseAttempt struct {
	Version        string `json:"version"`
	Concern        string `json:"concern"`
	Decision       string `json:"decision"`
	Rationale      string `json:"rationale"`
	IdempotencyKey string `json:"idempotencyKey"`
}

func rememberReview(ctx context.Context, workdir, repoID string, review reviewObligation) error {
	encoded, err := json.Marshal(review)
	if err != nil {
		return fmt.Errorf("encode review state: %w", err)
	}
	if err := gitRun(ctx, workdir, "config", "--local", reviewStateKey(repoID, reviewHandle(review)), string(encoded)); err != nil {
		return fmt.Errorf("store review state: %w", err)
	}
	return nil
}

func loadRememberedReview(ctx context.Context, workdir, repoID, handle string) (reviewObligation, bool, error) {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--get", reviewStateKey(repoID, handle))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err == nil {
		var review reviewObligation
		if json.Unmarshal(bytes.TrimSpace(output), &review) != nil || review.Version == "" || review.Concern == "" {
			return reviewObligation{}, false, errors.New("stored review state is invalid")
		}
		return review, true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return reviewObligation{}, false, nil
	}
	return reviewObligation{}, false, fmt.Errorf("read review state: %w", err)
}

func reviewStateKey(repoID, handle string) string {
	digest := sha256.Sum256([]byte(repoID))
	return "grd-review." + hex.EncodeToString(digest[:]) + "-" + handle + ".state"
}

func prepareReviewResponseAttempt(ctx context.Context, workdir, repoID string, review reviewObligation, decision, rationale string) (reviewResponseAttempt, error) {
	handle := reviewHandle(review)
	existing, found, err := loadReviewResponseAttempt(ctx, workdir, repoID, handle)
	if err != nil {
		return reviewResponseAttempt{}, err
	}
	if found && existing.Version == review.Version && existing.Concern == review.Concern && existing.Decision == decision && existing.Rationale == rationale {
		return existing, nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return reviewResponseAttempt{}, fmt.Errorf("create review response idempotency key: %w", err)
	}
	attempt := reviewResponseAttempt{
		Version:        review.Version,
		Concern:        review.Concern,
		Decision:       decision,
		Rationale:      rationale,
		IdempotencyKey: "grd-review-response-" + hex.EncodeToString(raw[:]),
	}
	encoded, err := json.Marshal(attempt)
	if err != nil {
		return reviewResponseAttempt{}, fmt.Errorf("encode review response attempt: %w", err)
	}
	if err := gitRun(ctx, workdir, "config", "--local", reviewResponseAttemptKey(repoID, handle), string(encoded)); err != nil {
		return reviewResponseAttempt{}, fmt.Errorf("store review response attempt: %w", err)
	}
	return attempt, nil
}

func loadReviewResponseAttempt(ctx context.Context, workdir, repoID, handle string) (reviewResponseAttempt, bool, error) {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--get", reviewResponseAttemptKey(repoID, handle))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err == nil {
		var attempt reviewResponseAttempt
		if json.Unmarshal(bytes.TrimSpace(output), &attempt) != nil || attempt.Version == "" || attempt.Concern == "" || attempt.Decision == "" || attempt.Rationale == "" || attempt.IdempotencyKey == "" {
			return reviewResponseAttempt{}, false, errors.New("stored review response attempt is invalid")
		}
		return attempt, true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return reviewResponseAttempt{}, false, nil
	}
	return reviewResponseAttempt{}, false, fmt.Errorf("read review response attempt: %w", err)
}

func forgetReviewResponseAttempt(ctx context.Context, workdir, repoID, handle string) error {
	command := exec.CommandContext(ctx, "git", "config", "--local", "--unset-all", reviewResponseAttemptKey(repoID, handle))
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 5 {
			return nil
		}
		return fmt.Errorf("clear review response attempt: %w", err)
	}
	return nil
}

func reviewResponseAttemptKey(repoID, handle string) string {
	digest := sha256.Sum256([]byte(repoID))
	return "grd-review." + hex.EncodeToString(digest[:]) + "-" + handle + ".attempt"
}
