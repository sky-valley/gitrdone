package grdclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
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
	state, found, err := loadSubmissionState(ctx, workdir, origin.repoID, head)
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
		workspace, workspaceFound, err := loadWorkspaceState(ctx, workdir, origin.repoID)
		if err != nil {
			return err
		}
		if workspaceFound {
			if workspace.ParentRevision == head {
				return errors.New("workspace has no new committed content")
			}
			if err := requireAncestor(ctx, workdir, workspace.ParentRevision, head); err != nil {
				return errors.New("workspace is not based on its submitted change; sync before submitting")
			}
			baseIntent = workspace.BaseIntent
			dependencies = []string{workspace.ParentVersion}
			dependencyTitle = workspace.ParentTitle
		} else if current.ContentRef.Revision == head {
			return errors.New("workspace has no new committed content")
		}
		idempotencyKey, err := newIdempotencyKey()
		if err != nil {
			return err
		}
		state = submissionState{
			BaseIntent:      baseIntent,
			IdempotencyKey:  idempotencyKey,
			Dependencies:    dependencies,
			DependencyTitle: dependencyTitle,
		}
		if err := rememberSubmissionState(ctx, workdir, origin.repoID, head, state); err != nil {
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
		if err := rememberWorkspaceState(ctx, workdir, origin.repoID, workspaceState{
			BaseIntent:     state.BaseIntent,
			ParentChange:   receipt.Change.ID,
			ParentVersion:  receipt.Version.ID,
			ParentRevision: head,
			ParentTitle:    title,
		}); err != nil {
			return err
		}
		fmt.Fprintf(client.Stdout, "Submitted: %s\n", title)
		if state.DependencyTitle != "" {
			fmt.Fprintf(client.Stdout, "Waiting on: %s\n", state.DependencyTitle)
		} else {
			fmt.Fprintln(client.Stdout, "Held")
		}
		fmt.Fprintln(client.Stdout, "Started a new working change on top of it.")
		return nil
	}
	if err := forgetWorkspaceState(ctx, workdir, origin.repoID); err != nil {
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
	state, found, err := loadWorkspaceState(ctx, workdir, origin.repoID)
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
	if found && current.ContentRef.Engine == "git" && current.ContentRef.Revision == state.ParentRevision {
		if err := forgetWorkspaceState(ctx, workdir, origin.repoID); err != nil {
			return err
		}
		found = false
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
		fmt.Fprintln(client.Stdout, "Based on: accepted intent")
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
	fmt.Fprintf(client.Stdout, "  %s — held\n", state.ParentTitle)
	return nil
}
