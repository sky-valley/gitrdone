package grdclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

type Client struct {
	HTTPClient *http.Client
	Stdout     io.Writer
}

func (client Client) Submit(ctx context.Context, workdir string) error {
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
		return errors.New("workspace has uncommitted changes; commit them before submitting")
	}

	origin, err := discoverRemote(ctx, workdir)
	if err != nil {
		return err
	}
	head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read workspace revision: %w", err)
	}
	title, err := gitOutput(ctx, workdir, "log", "-1", "--format=%s", "HEAD")
	if err != nil {
		return fmt.Errorf("read change title: %w", err)
	}
	state, found, err := loadSubmissionRetryState(ctx, workdir, origin.repoID, head)
	if err != nil {
		return err
	}
	if !found {
		current, err := client.currentIntent(ctx, origin)
		if err != nil {
			return err
		}
		if current.ContentRef.Engine != "git" || current.ContentRef.Revision == "" {
			return errors.New("current intent is not represented by Git content")
		}
		if err := requireAncestor(ctx, workdir, current.ContentRef.Revision, head); err != nil {
			return err
		}
		baseIntent := current.ID
		var dependencies []string
		var dependencyTitle string
		continuation, continuationFound, err := loadContinuationState(ctx, workdir, origin.repoID)
		if err != nil {
			return err
		}
		if continuationFound {
			if continuation.ParentRevision == head {
				return errors.New("workspace has no new committed content")
			}
			if err := requireAncestor(ctx, workdir, continuation.ParentRevision, head); err != nil {
				return errors.New("workspace is not based on its submitted change; sync before submitting")
			}
			baseIntent = continuation.BaseIntent
			dependencies = []string{continuation.ParentVersion}
			dependencyTitle = continuation.ParentTitle
		} else if current.ContentRef.Revision == head {
			return errors.New("workspace has no new committed content")
		}
		idempotencyKey, err := newIdempotencyKey()
		if err != nil {
			return err
		}
		state = submissionRetryState{
			BaseIntent:      baseIntent,
			IdempotencyKey:  idempotencyKey,
			Dependencies:    dependencies,
			DependencyTitle: dependencyTitle,
		}
		if err := rememberSubmissionRetryState(ctx, workdir, origin.repoID, head, state); err != nil {
			return err
		}
	}

	candidateRef := "refs/candidates/grd/" + head
	if err := gitRun(ctx, workdir, "push", "origin", "HEAD:"+candidateRef); err != nil {
		return fmt.Errorf("publish proposed content: %w", err)
	}
	receipt, err := client.propose(ctx, origin, state.BaseIntent, head, state.Dependencies, state.IdempotencyKey)
	if err != nil {
		return err
	}
	if receipt.Change.ID == "" || receipt.Version.ID == "" {
		return errors.New("proposal receipt is missing change identity")
	}
	if !slices.Equal(receipt.Version.Dependencies, state.Dependencies) {
		return errors.New("proposal receipt does not preserve submitted dependencies")
	}

	if receipt.Promotion == nil {
		if err := rememberContinuationState(ctx, workdir, origin.repoID, continuationState{
			BaseIntent:     state.BaseIntent,
			ParentChange:   receipt.Change.ID,
			ParentVersion:  receipt.Version.ID,
			ParentRevision: head,
			ParentTitle:    title,
		}); err != nil {
			return err
		}
		fmt.Fprintf(client.Stdout, "Submitted: %s\n", title)
		fmt.Fprintln(client.Stdout, "Admitted; judgement pending")
		if state.DependencyTitle != "" {
			fmt.Fprintf(client.Stdout, "Waiting on: %s\n", state.DependencyTitle)
		}
		fmt.Fprintln(client.Stdout, "Continue working on top of it.")
		return nil
	}
	if err := forgetContinuationState(ctx, workdir, origin.repoID); err != nil {
		return err
	}
	fmt.Fprintf(client.Stdout, "Submitted: %s\n", title)
	fmt.Fprintln(client.Stdout, "Promoted")
	fmt.Fprintln(client.Stdout, "You can keep working.")
	return nil
}

func (client Client) Status(ctx context.Context, workdir string) error {
	if client.Stdout == nil {
		client.Stdout = io.Discard
	}
	if client.HTTPClient == nil {
		client.HTTPClient = http.DefaultClient
	}
	origin, err := discoverRemote(ctx, workdir)
	if err != nil {
		return err
	}
	state, found, err := loadContinuationState(ctx, workdir, origin.repoID)
	if err != nil {
		return err
	}
	head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read workspace revision: %w", err)
	}
	status, err := gitOutput(ctx, workdir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("read workspace status: %w", err)
	}
	current, err := client.currentIntent(ctx, origin)
	if err != nil {
		return err
	}
	parentRelationship := "judgement pending"
	pendingAmendmentRationale := ""
	var pendingReconciliation *reconciliationConflictResponse
	resolvedReconciliationRationale := ""
	resolvedReconciliationStatus := ""
	if found {
		change, err := client.change(ctx, origin, state.ParentChange)
		if err != nil {
			return err
		}
		if change.ID != state.ParentChange {
			return errors.New("server returned a different submitted change")
		}
		switch {
		case change.LatestVersion.ID == state.ParentVersion:
			if change.LatestAmendment != nil {
				return errors.New("server returned an invalid latest amendment")
			}
			if change.LatestPromotion != nil {
				if change.LatestPromotion.Version != state.ParentVersion {
					return errors.New("server returned an invalid latest promotion")
				}
				if err := forgetContinuationState(ctx, workdir, origin.repoID); err != nil {
					return err
				}
				found = false
			}
		case change.LatestAmendment == nil ||
			change.LatestAmendment.FromVersion != state.ParentVersion ||
			change.LatestAmendment.ToVersion != change.LatestVersion.ID:
			return errors.New("server returned an unsupported submitted change lineage")
		case change.LatestPromotion == nil:
			if strings.TrimSpace(change.LatestAmendment.Rationale) == "" {
				return errors.New("server returned an invalid latest amendment")
			}
			parentRelationship = "amended; judgement pending"
			pendingAmendmentRationale = change.LatestAmendment.Rationale
		case change.LatestPromotion.Version != change.LatestVersion.ID:
			return errors.New("server returned an invalid latest promotion")
		default:
			if state.ConflictID == "" {
				parentRelationship = "amended and accepted; run grd sync"
				break
			}
			conflict, err := client.reconciliationConflictForWorkspace(ctx, workdir, origin, state, change.LatestVersion.ID, head)
			if err != nil {
				return err
			}
			parentRelationship = "amended and accepted"
			switch conflict.State {
			case "awaiting_judgement":
				pendingReconciliation = &conflict
			case "resolved":
				resolvedChange, err := client.change(ctx, origin, conflict.Change.ID)
				if err != nil {
					return err
				}
				if err := validateResolvedConflictChange(conflict, change, resolvedChange); err != nil {
					return err
				}
				resolvedReconciliationRationale = conflict.Resolution.Rationale
				if resolvedChange.LatestPromotion == nil {
					resolvedReconciliationStatus = "resolution awaiting judgement"
					break
				}
				if current.ID != resolvedChange.LatestPromotion.ToIntent ||
					current.ContentRef.Engine != "git" ||
					current.ContentRef.Revision != resolvedChange.LatestVersion.ContentRef.Revision {
					return errors.New("accepted intent does not match the reconciliation resolution")
				}
				resolvedReconciliationStatus = "resolved; run grd sync"
			default:
				return errors.New("server returned an invalid reconciliation conflict")
			}
		}
	}
	if !found {
		if current.ContentRef.Engine == "git" && current.ContentRef.Revision == head {
			working := "new change"
			if status != "" {
				working = "uncommitted work"
			}
			fmt.Fprintf(client.Stdout, "Working: %s\n", working)
			fmt.Fprintln(client.Stdout, "Based on: accepted intent")
			return nil
		}
		title, err := gitOutput(ctx, workdir, "log", "-1", "--format=%s", "HEAD")
		if err != nil {
			return fmt.Errorf("read change title: %w", err)
		}
		fmt.Fprintf(client.Stdout, "Working: %s\n", title)
		if current.ContentRef.Engine != "git" || current.ContentRef.Revision == "" {
			fmt.Fprintln(client.Stdout, "Based on: unknown (accepted intent is not Git content)")
			return nil
		}
		basedOnIntent, err := isAncestor(ctx, workdir, current.ContentRef.Revision, head)
		if err != nil {
			return fmt.Errorf("check relationship to accepted intent: %w", err)
		}
		if basedOnIntent {
			fmt.Fprintln(client.Stdout, "Based on: accepted intent")
		} else {
			fmt.Fprintln(client.Stdout, "Based on: unknown (workspace does not descend from accepted intent)")
		}
		return nil
	}
	if head != state.ParentRevision {
		if err := requireAncestor(ctx, workdir, state.ParentRevision, head); err != nil {
			return errors.New("workspace is no longer based on its submitted change")
		}
	}
	working := "new change"
	if head != state.ParentRevision {
		working, err = gitOutput(ctx, workdir, "log", "-1", "--format=%s", "HEAD")
		if err != nil {
			return fmt.Errorf("read change title: %w", err)
		}
	}
	fmt.Fprintf(client.Stdout, "Working: %s\n", working)
	fmt.Fprintln(client.Stdout, "Based on:")
	fmt.Fprintf(client.Stdout, "  %s — %s\n", state.ParentTitle, parentRelationship)
	if pendingAmendmentRationale != "" {
		fmt.Fprintf(client.Stdout, "Repository amendment: %s\n", pendingAmendmentRationale)
	}
	if pendingReconciliation != nil {
		fmt.Fprintln(client.Stdout, "Reconciliation: awaiting judgement")
		if len(pendingReconciliation.AffectedPaths) > 0 {
			fmt.Fprintln(client.Stdout, "Affected:")
			for _, path := range pendingReconciliation.AffectedPaths {
				fmt.Fprintf(client.Stdout, "  %s\n", path)
			}
		}
	}
	if resolvedReconciliationRationale != "" {
		fmt.Fprintf(client.Stdout, "Reconciliation: %s\n", resolvedReconciliationStatus)
		fmt.Fprintf(client.Stdout, "Repository resolution: %s\n", resolvedReconciliationRationale)
	}
	return nil
}
