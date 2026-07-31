package httpapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
	"github.com/sky-valley/gitrdone/internal/judgement"
)

type filesystemPendingSource struct {
	registry *intentRepositoryRegistry
}

func newFilesystemPendingSource(registry *intentRepositoryRegistry) *filesystemPendingSource {
	return &filesystemPendingSource{registry: registry}
}

func (source *filesystemPendingSource) ListPending(ctx context.Context, after string, limit int) (judgement.PendingPage, error) {
	if limit < 1 || limit > 100 {
		return judgement.PendingPage{}, errors.New("global pending page limit must be between 1 and 100")
	}
	cursorRepoID, cursorVersionID, err := decodePendingCursor(after)
	if err != nil {
		return judgement.PendingPage{}, err
	}
	entries, err := os.ReadDir(filepath.Join(source.registry.storageRoot, "intent"))
	if errors.Is(err, os.ErrNotExist) {
		return judgement.PendingPage{}, nil
	}
	if err != nil {
		return judgement.PendingPage{}, fmt.Errorf("discover intent repositories: %w", err)
	}

	page := judgement.PendingPage{Items: make([]judgement.WorkItem, 0, limit)}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() < cursorRepoID {
			continue
		}
		repoID := entry.Name()
		controlRepoID := formatRepoControlID(repoID)
		if _, valid := parseRepoControlID(controlRepoID); !valid {
			continue
		}
		afterVersion := intent.VersionID("")
		if repoID == cursorRepoID {
			afterVersion = cursorVersionID
		}
		repository, err := source.registry.Resolve(ctx, controlRepoID)
		if errors.Is(err, intentservice.ErrRepositoryNotFound) {
			continue
		}
		if err != nil {
			return judgement.PendingPage{}, fmt.Errorf("open pending repository %s: %w", repoID, err)
		}
		pending, err := repository.PendingJudgements(ctx, intent.PendingJudgementQuery{
			After: afterVersion,
			Limit: limit - len(page.Items),
		})
		if err != nil {
			return judgement.PendingPage{}, fmt.Errorf("read pending repository %s: %w", repoID, err)
		}
		for _, version := range pending.Versions {
			page.Items = append(page.Items, judgement.WorkItem{
				RepoID:    controlRepoID,
				VersionID: version.ID,
			})
		}
		if pending.NextCursor != "" {
			page.NextCursor = encodePendingCursor(repoID, pending.NextCursor)
			return page, nil
		}
		if len(page.Items) == limit {
			last := page.Items[len(page.Items)-1]
			page.NextCursor = encodePendingCursor(repoID, last.VersionID)
			return page, nil
		}
	}
	return page, nil
}

func encodePendingCursor(repoID string, versionID intent.VersionID) string {
	return repoID + ":" + string(versionID)
}

func decodePendingCursor(cursor string) (string, intent.VersionID, error) {
	if cursor == "" {
		return "", "", nil
	}
	repoID, versionID, found := strings.Cut(cursor, ":")
	if !found || repoID == "" || versionID == "" {
		return "", "", errors.New("invalid global pending cursor")
	}
	return repoID, intent.VersionID(versionID), nil
}
