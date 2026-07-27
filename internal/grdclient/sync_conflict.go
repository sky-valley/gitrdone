package grdclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func (client Client) reconcileConflictResolution(
	ctx context.Context,
	workdir string,
	origin remote,
	state continuationState,
	parent changeResponse,
	head string,
) error {
	conflict, err := client.reconciliationConflict(ctx, origin, state.ConflictID)
	if err != nil {
		return err
	}
	if err := validateReconciliationConflict(conflict, state, parent.LatestVersion.ID, "", ""); err != nil {
		return err
	}
	if conflict.State == "awaiting_judgement" {
		if err := requireCapturedConflictAncestor(ctx, workdir, conflict, head); err != nil {
			return err
		}
		return errors.New("reconciliation conflict is awaiting judgement")
	}

	resolvedChange, err := client.change(ctx, origin, conflict.Change.ID)
	if err != nil {
		return err
	}
	if err := validateResolvedConflictChange(conflict, parent, resolvedChange); err != nil {
		return err
	}
	if resolvedChange.LatestPromotion == nil {
		return errors.New("reconciliation resolution is awaiting judgement")
	}
	targetRevision := resolvedChange.LatestVersion.ContentRef.Revision
	current, err := client.currentIntent(ctx, origin)
	if err != nil {
		return err
	}
	if current.ID != resolvedChange.LatestPromotion.ToIntent ||
		current.ContentRef.Engine != "git" ||
		current.ContentRef.Revision != targetRevision {
		return errors.New("accepted intent does not match the reconciliation resolution")
	}
	if err := gitRun(ctx, workdir, "fetch", "--quiet", "origin"); err != nil {
		return fmt.Errorf("fetch accepted reconciliation resolution: %w", err)
	}
	if err := gitRun(ctx, workdir, "cat-file", "-e", targetRevision+"^{commit}"); err != nil {
		return errors.New("accepted reconciliation resolution is not available from origin")
	}

	alreadyApplied, err := isAncestor(ctx, workdir, targetRevision, head)
	if err != nil {
		return fmt.Errorf("check resolved workspace ancestry: %w", err)
	}
	if alreadyApplied {
		if err := forgetContinuationState(ctx, workdir, origin.repoID); err != nil {
			return err
		}
		fmt.Fprintf(client.Stdout, "Synced: %s\n", state.ParentTitle)
		fmt.Fprintf(client.Stdout, "Repository resolution: %s\n", conflict.Resolution.Rationale)
		fmt.Fprintln(client.Stdout, "Workspace already contains the accepted resolution.")
		return nil
	}
	if err := requireCapturedConflictAncestor(ctx, workdir, conflict, head); err != nil {
		return err
	}
	if err := requireLinearPortalReplay(ctx, workdir, conflict.Version.ContentRef.Revision, head); err != nil {
		return err
	}

	countText, err := gitOutput(ctx, workdir, "rev-list", "--count", conflict.Version.ContentRef.Revision+".."+head)
	if err != nil {
		return fmt.Errorf("count work after captured conflict: %w", err)
	}
	commitCount, err := strconv.Atoi(countText)
	if err != nil {
		return errors.New("count work after captured conflict: Git returned an invalid count")
	}
	recoveryRef := "refs/grd/recovery/" + head
	if err := ensureRecoveryRef(ctx, workdir, recoveryRef, head); err != nil {
		return err
	}

	if commitCount == 0 {
		if err := gitRun(ctx, workdir, "reset", "--hard", targetRevision); err != nil {
			return fmt.Errorf("update workspace to accepted reconciliation resolution; recover from %s", recoveryRef)
		}
	} else if err := gitRun(ctx, workdir, "rebase", "--onto", targetRevision, conflict.Version.ContentRef.Revision); err != nil {
		if abortErr := gitRun(ctx, workdir, "rebase", "--abort"); abortErr != nil {
			return fmt.Errorf("resolved-work replay conflicted and Git could not restore the workspace; recover from %s", recoveryRef)
		}
		if err := requireRestoredWorkspace(ctx, workdir, head); err != nil {
			return fmt.Errorf("resolved-work replay conflicted and workspace restoration could not be verified; recover from %s: %w", recoveryRef, err)
		}
		return fmt.Errorf("resolved-work replay conflicted; workspace restored from %s", recoveryRef)
	}
	if err := forgetContinuationState(ctx, workdir, origin.repoID); err != nil {
		return err
	}

	fmt.Fprintf(client.Stdout, "Synced: %s\n", state.ParentTitle)
	fmt.Fprintf(client.Stdout, "Repository resolution: %s\n", conflict.Resolution.Rationale)
	if commitCount == 0 {
		fmt.Fprintln(client.Stdout, "Workspace updated to accepted resolution.")
	} else if commitCount == 1 {
		fmt.Fprintln(client.Stdout, "Replayed: 1 newer local commit")
	} else {
		fmt.Fprintf(client.Stdout, "Replayed: %d newer local commits\n", commitCount)
	}
	fmt.Fprintf(client.Stdout, "Recovery: %s\n", recoveryRef)
	return nil
}

func (client Client) reconciliationConflictForWorkspace(
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
	if err := requireCapturedConflictAncestor(ctx, workdir, conflict, head); err != nil {
		return reconciliationConflictResponse{}, err
	}
	return conflict, nil
}

func requireCapturedConflictAncestor(ctx context.Context, workdir string, conflict reconciliationConflictResponse, head string) error {
	basedOnCapturedWork, err := isAncestor(ctx, workdir, conflict.Version.ContentRef.Revision, head)
	if err != nil {
		return fmt.Errorf("check relationship to captured conflict work: %w", err)
	}
	if !basedOnCapturedWork {
		return errors.New("workspace no longer descends from its captured reconciliation conflict")
	}
	return nil
}

func requireLinearPortalReplay(ctx context.Context, workdir, base, head string) error {
	merges, err := gitOutput(ctx, workdir, "rev-list", "--min-parents=2", base+".."+head)
	if err != nil {
		return fmt.Errorf("inspect newer local history: %w", err)
	}
	if merges != "" {
		return errors.New("newer local work contains merge commits; automatic portal replay is not safe")
	}
	return nil
}

func validateResolvedConflictChange(conflict reconciliationConflictResponse, parent changeResponse, change changeResponse) error {
	if conflict.Resolution == nil ||
		parent.LatestPromotion == nil ||
		conflict.Resolution.BaseIntent != parent.LatestPromotion.ToIntent ||
		change.ID != conflict.Change.ID ||
		change.LatestVersion.ID != conflict.Resolution.ToVersion ||
		change.LatestVersion.BaseIntent != conflict.Resolution.BaseIntent ||
		change.LatestVersion.ContentRef.Engine != "git" ||
		change.LatestVersion.ContentRef.Revision == "" {
		return errors.New("server returned an invalid reconciliation resolution")
	}
	if change.LatestPromotion != nil &&
		(change.LatestPromotion.FromIntent != conflict.Resolution.BaseIntent ||
			change.LatestPromotion.Version != change.LatestVersion.ID) {
		return errors.New("server returned an invalid reconciliation resolution")
	}
	return nil
}

func validateReconciliationConflict(conflict reconciliationConflictResponse, state continuationState, toVersion, descendantVersion, descendantRevision string) error {
	if conflict.ID == "" || (state.ConflictID != "" && conflict.ID != state.ConflictID) ||
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
	switch conflict.State {
	case "awaiting_judgement":
		if conflict.Resolution != nil {
			return errors.New("server returned an invalid reconciliation conflict")
		}
	case "resolved":
		if conflict.Resolution == nil ||
			conflict.Resolution.ID == "" ||
			conflict.Resolution.FromVersion != conflict.Version.ID ||
			conflict.Resolution.ToVersion == "" ||
			conflict.Resolution.ToVersion == conflict.Version.ID ||
			conflict.Resolution.BaseIntent == "" ||
			strings.TrimSpace(conflict.Resolution.ResolvedBy) == "" ||
			strings.TrimSpace(conflict.Resolution.Rationale) == "" {
			return errors.New("server returned an invalid reconciliation conflict")
		}
	default:
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
