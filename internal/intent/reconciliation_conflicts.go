package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const maxConflictPaths = 256
const maxConflictPathBytes = 4096
const maxConflictPathsTotalBytes = 48 * 1024

var ErrVersionNotPromoted = errors.New("change version is not promoted")
var ErrReconciliationConflictNotFound = errors.New("reconciliation conflict not found")
var ErrInvalidReconciliationConflict = errors.New("invalid reconciliation conflict")
var ErrInvalidReconciliationLineage = errors.New("invalid reconciliation lineage")

type ConflictID string

type ReconciliationConflict struct {
	ID            ConflictID
	Change        Change
	Version       Version
	FromVersion   VersionID
	ToVersion     VersionID
	ReportedBy    string
	AffectedPaths []string
}

type ReconciliationConflictRequest struct {
	IdempotencyKey    string
	FromVersion       VersionID
	ToVersion         VersionID
	DescendantVersion VersionID
	ReportedBy        string
	AffectedPaths     []string
}

type ReconciliationConflictQuery struct {
	After ConflictID
	Limit int
}

type ReconciliationConflictPage struct {
	Conflicts  []ReconciliationConflict
	NextCursor ConflictID
}

func (repository *Repository) RecordReconciliationConflict(ctx context.Context, request ReconciliationConflictRequest) (ReconciliationConflict, error) {
	if request.IdempotencyKey == "" {
		return ReconciliationConflict{}, fmt.Errorf("%w: idempotency key is required", ErrInvalidReconciliationConflict)
	}
	if request.FromVersion == "" || request.ToVersion == "" || request.DescendantVersion == "" {
		return ReconciliationConflict{}, fmt.Errorf("%w: versions are required", ErrInvalidReconciliationConflict)
	}
	if request.ReportedBy == "" {
		return ReconciliationConflict{}, fmt.Errorf("%w: reporter is required", ErrInvalidReconciliationConflict)
	}
	paths, err := NormalizeReconciliationConflictPaths(request.AffectedPaths)
	if err != nil {
		return ReconciliationConflict{}, err
	}

	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()
	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()

	existing, found, err := repository.conflicts.ReconciliationConflictByIdempotencyKey(ctx, request.IdempotencyKey)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read reconciliation conflict idempotency record: %w", err)
	}
	if found {
		if existing.FromVersion != request.FromVersion ||
			existing.ToVersion != request.ToVersion ||
			existing.Version.ID != request.DescendantVersion ||
			existing.ReportedBy != request.ReportedBy {
			return ReconciliationConflict{}, ErrIdempotencyConflict
		}
		return cloneReconciliationConflict(existing), nil
	}

	from, found, err := repository.changes.Version(ctx, request.FromVersion)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read reconciliation source version: %w", err)
	}
	if !found {
		return ReconciliationConflict{}, ErrVersionNotFound
	}
	to, found, err := repository.changes.Version(ctx, request.ToVersion)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read reconciliation target version: %w", err)
	}
	if !found {
		return ReconciliationConflict{}, ErrVersionNotFound
	}
	if from.ChangeID != to.ChangeID {
		return ReconciliationConflict{}, fmt.Errorf("%w: versions belong to different changes", ErrInvalidReconciliationLineage)
	}
	latest, found, err := repository.changes.LatestVersion(ctx, from.ChangeID)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read latest reconciled version: %w", err)
	}
	if !found || latest.ID != to.ID {
		return ReconciliationConflict{}, ErrVersionAdvanced
	}
	amendment, found, err := repository.amendments.Amendment(ctx, to.ID)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read reconciliation amendment: %w", err)
	}
	if !found || amendment.FromVersion != from.ID || amendment.ToVersion != to.ID {
		return ReconciliationConflict{}, fmt.Errorf("%w: versions are not an amendment lineage", ErrInvalidReconciliationLineage)
	}
	promoted, found, err := repository.promotions.CompletedPromotion(ctx, to.ID)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read reconciliation target promotion: %w", err)
	}
	if !found {
		return ReconciliationConflict{}, ErrVersionNotPromoted
	}
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read current intent for reconciliation conflict: %w", err)
	}
	if !found || promoted.Intent.ID != current.ID {
		return ReconciliationConflict{}, ErrIntentAdvanced
	}
	descendant, found, err := repository.changes.Version(ctx, request.DescendantVersion)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read reconciliation descendant version: %w", err)
	}
	if !found {
		return ReconciliationConflict{}, ErrVersionNotFound
	}
	if descendant.ChangeID == from.ChangeID || descendant.BaseIntent != from.BaseIntent {
		return ReconciliationConflict{}, fmt.Errorf("%w: descendant does not identify separate work from the reconciled base", ErrInvalidReconciliationLineage)
	}
	descendantChange, found, err := repository.changes.Change(ctx, descendant.ChangeID)
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("read reconciliation descendant change: %w", err)
	}
	if !found {
		return ReconciliationConflict{}, ErrChangeNotFound
	}

	conflictID, err := newID("conflict")
	if err != nil {
		return ReconciliationConflict{}, fmt.Errorf("create reconciliation conflict id: %w", err)
	}
	conflict := ReconciliationConflict{
		ID:            ConflictID(conflictID),
		Change:        descendantChange,
		Version:       descendant,
		FromVersion:   from.ID,
		ToVersion:     to.ID,
		ReportedBy:    request.ReportedBy,
		AffectedPaths: paths,
	}
	if err := repository.conflicts.RecordReconciliationConflict(ctx, request.IdempotencyKey, conflict); err != nil {
		return ReconciliationConflict{}, fmt.Errorf("record reconciliation conflict: %w", err)
	}
	return cloneReconciliationConflict(conflict), nil
}

func (repository *Repository) ReconciliationConflict(ctx context.Context, id ConflictID) (ReconciliationConflict, bool, error) {
	conflict, found, err := repository.conflicts.ReconciliationConflict(ctx, id)
	if err != nil {
		return ReconciliationConflict{}, false, fmt.Errorf("read reconciliation conflict: %w", err)
	}
	return cloneReconciliationConflict(conflict), found, nil
}

func (repository *Repository) ReconciliationConflicts(ctx context.Context, query ReconciliationConflictQuery) (ReconciliationConflictPage, error) {
	if query.Limit < 1 || query.Limit > 100 {
		return ReconciliationConflictPage{}, errors.New("reconciliation conflict page limit must be between 1 and 100")
	}
	conflicts, more, err := repository.conflicts.ReconciliationConflicts(ctx, query.After, query.Limit)
	if err != nil {
		return ReconciliationConflictPage{}, fmt.Errorf("read reconciliation conflicts: %w", err)
	}
	page := ReconciliationConflictPage{Conflicts: conflicts}
	if more && len(conflicts) > 0 {
		page.NextCursor = conflicts[len(conflicts)-1].ID
	}
	return page, nil
}

func NormalizeReconciliationConflictPaths(paths []string) ([]string, error) {
	if len(paths) > maxConflictPaths {
		return nil, fmt.Errorf("%w: affected paths must be bounded", ErrInvalidReconciliationConflict)
	}
	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	totalBytes := 0
	for _, path := range paths {
		if path == "" || len(path) > maxConflictPathBytes || strings.ContainsRune(path, '\x00') {
			return nil, fmt.Errorf("%w: affected path is invalid", ErrInvalidReconciliationConflict)
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		totalBytes += len(path)
		if totalBytes > maxConflictPathsTotalBytes {
			return nil, fmt.Errorf("%w: affected paths exceed the aggregate bound", ErrInvalidReconciliationConflict)
		}
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func cloneReconciliationConflict(conflict ReconciliationConflict) ReconciliationConflict {
	conflict.Version = cloneVersion(conflict.Version)
	conflict.AffectedPaths = slices.Clone(conflict.AffectedPaths)
	return conflict
}
