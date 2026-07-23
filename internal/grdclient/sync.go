package grdclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func (client Client) Sync(ctx context.Context, workdir string) error {
	if client.Stdout == nil {
		client.Stdout = io.Discard
	}
	if client.HTTPClient == nil {
		client.HTTPClient = http.DefaultClient
	}
	status, err := gitOutput(ctx, workdir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("read workspace status: %w", err)
	}
	if status != "" {
		return errors.New("workspace has uncommitted changes; commit or discard them before syncing")
	}

	origin, err := discoverRemote(ctx, workdir)
	if err != nil {
		return err
	}
	state, found, err := loadContinuationState(ctx, workdir, origin.repoID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("workspace has no submitted parent awaiting reconciliation")
	}
	return client.reconcileAmendedParent(ctx, workdir, origin, state)
}

func (client Client) reconcileAmendedParent(ctx context.Context, workdir string, origin remote, state continuationState) error {
	head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read workspace revision: %w", err)
	}
	if err := requireSubmittedAncestor(ctx, workdir, state.ParentRevision, head); err != nil {
		return err
	}

	change, err := client.change(ctx, origin, state.ParentChange)
	if err != nil {
		return err
	}
	if change.ID != state.ParentChange {
		return errors.New("server returned a different submitted change")
	}
	if change.LatestAmendment == nil {
		if change.LatestVersion.ID == state.ParentVersion && change.LatestPromotion == nil {
			return errors.New("submitted change is awaiting judgement")
		}
		return errors.New("submitted change cannot yet be reconciled automatically")
	}
	if change.LatestAmendment.FromVersion != state.ParentVersion ||
		change.LatestAmendment.ToVersion != change.LatestVersion.ID ||
		strings.TrimSpace(change.LatestAmendment.Rationale) == "" {
		return errors.New("server returned an invalid amendment lineage")
	}
	if change.LatestPromotion == nil || change.LatestPromotion.Version != change.LatestVersion.ID {
		return errors.New("amended change has not been accepted yet")
	}
	if change.LatestVersion.ContentRef.Engine != "git" || change.LatestVersion.ContentRef.Revision == "" {
		return errors.New("amended change is not represented by Git content")
	}
	targetRevision := change.LatestVersion.ContentRef.Revision
	current, err := client.currentIntent(ctx, origin)
	if err != nil {
		return err
	}
	if current.ID != change.LatestPromotion.ToIntent || current.ContentRef.Engine != "git" || current.ContentRef.Revision != targetRevision {
		return errors.New("accepted intent does not match the amended change")
	}
	if state.ConflictID != "" {
		if _, err := client.awaitingReconciliationConflict(ctx, workdir, origin, state, change.LatestVersion.ID, head); err != nil {
			return err
		}
		return errors.New("reconciliation conflict is awaiting judgement")
	}

	if err := gitRun(ctx, workdir, "fetch", "--quiet", "origin"); err != nil {
		return fmt.Errorf("fetch accepted amendment: %w", err)
	}
	if err := gitRun(ctx, workdir, "cat-file", "-e", targetRevision+"^{commit}"); err != nil {
		return errors.New("accepted amendment is not available from origin")
	}
	countText, err := gitOutput(ctx, workdir, "rev-list", "--count", state.ParentRevision+".."+head)
	if err != nil {
		return fmt.Errorf("count local continuation: %w", err)
	}
	commitCount, err := strconv.Atoi(countText)
	if err != nil {
		return errors.New("count local continuation: Git returned an invalid count")
	}
	recoveryRef := "refs/grd/recovery/" + head
	if err := ensureRecoveryRef(ctx, workdir, recoveryRef, head); err != nil {
		return err
	}

	if commitCount == 0 {
		if err := gitRun(ctx, workdir, "reset", "--hard", targetRevision); err != nil {
			return fmt.Errorf("update workspace to accepted amendment; recover from %s", recoveryRef)
		}
	} else {
		if err := gitRun(ctx, workdir, "rebase", "--onto", targetRevision, state.ParentRevision); err != nil {
			affectedPaths, _ := unmergedPaths(ctx, workdir)
			if abortErr := gitRun(ctx, workdir, "rebase", "--abort"); abortErr != nil {
				return fmt.Errorf("automatic replay conflicted and Git could not restore the workspace; recover from %s", recoveryRef)
			}
			if err := requireRestoredWorkspace(ctx, workdir, head); err != nil {
				return fmt.Errorf("automatic replay conflicted and workspace restoration could not be verified; recover from %s: %w", recoveryRef, err)
			}
			candidateRef := "refs/candidates/grd/" + head
			if err := gitRun(ctx, workdir, "push", "origin", "HEAD:"+candidateRef); err != nil {
				return fmt.Errorf("publish conflicted continuation: %w", err)
			}
			descendant, err := client.propose(
				ctx,
				origin,
				change.LatestVersion.BaseIntent,
				head,
				nil,
				reconciliationDescendantIdempotencyKey(origin.repoID, state.ParentVersion, change.LatestVersion.ID, head),
			)
			if err != nil {
				return fmt.Errorf("admit conflicted continuation: %w", err)
			}
			if descendant.Change.ID == "" || descendant.Version.ID == "" ||
				len(descendant.Version.Dependencies) != 0 || descendant.Promotion != nil {
				return errors.New("server returned an invalid conflicted-continuation admission")
			}
			conflict, err := client.recordReconciliationConflict(
				ctx,
				origin,
				state.ParentVersion,
				change.LatestVersion.ID,
				descendant.Version.ID,
				affectedPaths,
			)
			if err != nil {
				return err
			}
			if err := validateReconciliationConflict(conflict, state, change.LatestVersion.ID, descendant.Version.ID, head); err != nil {
				return err
			}
			state.ConflictID = conflict.ID
			if err := rememberContinuationState(ctx, workdir, origin.repoID, state); err != nil {
				return err
			}
			fmt.Fprintf(client.Stdout, "Sync needs judgement: %s\n", state.ParentTitle)
			fmt.Fprintf(client.Stdout, "Repository amendment: %s\n", change.LatestAmendment.Rationale)
			fmt.Fprintf(client.Stdout, "Conflict recorded: %s\n", conflict.ID)
			if len(conflict.AffectedPaths) > 0 {
				fmt.Fprintln(client.Stdout, "Affected:")
				for _, path := range conflict.AffectedPaths {
					fmt.Fprintf(client.Stdout, "  %s\n", path)
				}
			}
			fmt.Fprintf(client.Stdout, "Workspace restored: %s\n", recoveryRef)
			return errors.New("reconciliation conflict is awaiting judgement")
		}
	}
	if err := forgetContinuationState(ctx, workdir, origin.repoID); err != nil {
		return err
	}

	fmt.Fprintf(client.Stdout, "Synced: %s\n", state.ParentTitle)
	fmt.Fprintf(client.Stdout, "Repository amendment: %s\n", change.LatestAmendment.Rationale)
	if commitCount == 0 {
		fmt.Fprintln(client.Stdout, "Workspace updated to accepted amendment.")
	} else if commitCount == 1 {
		fmt.Fprintln(client.Stdout, "Replayed: 1 local commit")
	} else {
		fmt.Fprintf(client.Stdout, "Replayed: %d local commits\n", commitCount)
	}
	fmt.Fprintf(client.Stdout, "Recovery: %s\n", recoveryRef)
	return nil
}

func (client Client) awaitingReconciliationConflict(
	ctx context.Context,
	workdir string,
	origin remote,
	state continuationState,
	toVersion string,
	head string,
) (reconciliationConflictResponse, error) {
	conflict, err := client.reconciliationConflict(ctx, origin, state.ConflictID)
	if err != nil {
		return reconciliationConflictResponse{}, err
	}
	if err := validateReconciliationConflict(conflict, state, toVersion, "", ""); err != nil {
		return reconciliationConflictResponse{}, err
	}
	basedOnCapturedWork, err := isAncestor(ctx, workdir, conflict.Version.ContentRef.Revision, head)
	if err != nil {
		return reconciliationConflictResponse{}, fmt.Errorf("check relationship to captured conflict work: %w", err)
	}
	if !basedOnCapturedWork {
		return reconciliationConflictResponse{}, errors.New("workspace no longer descends from its captured reconciliation conflict")
	}
	return conflict, nil
}

func validateReconciliationConflict(conflict reconciliationConflictResponse, state continuationState, toVersion, descendantVersion, descendantRevision string) error {
	if conflict.ID == "" || (state.ConflictID != "" && conflict.ID != state.ConflictID) ||
		conflict.State != "awaiting_judgement" ||
		conflict.Change.ID == "" ||
		conflict.Version.ID == "" ||
		conflict.Version.Change != conflict.Change.ID ||
		conflict.FromVersion != state.ParentVersion ||
		conflict.ToVersion != toVersion ||
		conflict.ReportedBy == "" ||
		conflict.Version.ContentRef.Engine != "git" ||
		conflict.Version.ContentRef.Revision == "" ||
		len(conflict.Version.Dependencies) != 0 {
		return errors.New("server returned an invalid reconciliation conflict")
	}
	if descendantVersion != "" && conflict.Version.ID != descendantVersion {
		return errors.New("server recorded a different reconciliation descendant version")
	}
	if descendantRevision != "" && conflict.Version.ContentRef.Revision != descendantRevision {
		return errors.New("server recorded different reconciliation descendant content")
	}
	return nil
}

func unmergedPaths(ctx context.Context, workdir string) ([]string, error) {
	command := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=U", "-z")
	command.Dir = workdir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect unmerged paths: %w", err)
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			paths = append(paths, string(part))
		}
	}
	return paths, nil
}

func requireRestoredWorkspace(ctx context.Context, workdir, expectedHead string) error {
	head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read restored workspace revision: %w", err)
	}
	if head != expectedHead {
		return errors.New("workspace revision was not restored")
	}
	status, err := gitOutput(ctx, workdir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("read restored workspace status: %w", err)
	}
	if status != "" {
		return errors.New("workspace conflict state was not cleared")
	}
	return nil
}

func requireSubmittedAncestor(ctx context.Context, workdir string, parent string, head string) error {
	if err := gitRun(ctx, workdir, "merge-base", "--is-ancestor", parent, head); err != nil {
		return errors.New("workspace is no longer based on its submitted change")
	}
	return nil
}

func ensureRecoveryRef(ctx context.Context, workdir string, ref string, revision string) error {
	existing, err := gitOutput(ctx, workdir, "rev-parse", "--verify", "--quiet", ref)
	if err == nil {
		if existing != revision {
			return fmt.Errorf("recovery ref %s already protects different work", ref)
		}
		return nil
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		return fmt.Errorf("inspect recovery ref: %w", err)
	}
	if err := gitRun(ctx, workdir, "update-ref", ref, revision, strings.Repeat("0", len(revision))); err != nil {
		return fmt.Errorf("create recovery ref: %w", err)
	}
	return nil
}
