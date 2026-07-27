package intent

import (
	"context"
	"errors"
	"slices"
)

func (ledger *transientLedger) ReconciliationConflict(_ context.Context, id ConflictID) (ReconciliationConflictInspection, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	conflict, found := ledger.conflicts[id]
	inspection := ReconciliationConflictInspection{ReconciliationConflict: conflict}
	if resolution, resolved := ledger.resolutions[id]; resolved {
		inspection.Resolution = &resolution
	}
	return cloneReconciliationConflictInspection(inspection), found, nil
}

func (ledger *transientLedger) ReconciliationConflicts(_ context.Context, after ConflictID, limit int) ([]ReconciliationConflictInspection, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	start := 0
	if after != "" {
		start = -1
		for index, id := range ledger.conflictIDs {
			if id == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, ErrReconciliationConflictNotFound
		}
	}
	end := min(start+limit, len(ledger.conflictIDs))
	conflicts := make([]ReconciliationConflictInspection, 0, end-start)
	for _, id := range ledger.conflictIDs[start:end] {
		inspection := ReconciliationConflictInspection{ReconciliationConflict: ledger.conflicts[id]}
		if resolution, resolved := ledger.resolutions[id]; resolved {
			inspection.Resolution = &resolution
		}
		conflicts = append(conflicts, cloneReconciliationConflictInspection(inspection))
	}
	return conflicts, end < len(ledger.conflictIDs), nil
}

func (ledger *transientLedger) ReconciliationConflictByIdempotencyKey(_ context.Context, key string) (ReconciliationConflictInspection, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return ReconciliationConflictInspection{}, false, nil
	}
	if record.operation != transientReconciliationConflictOperation {
		return ReconciliationConflictInspection{}, false, ErrIdempotencyConflict
	}
	conflict, found := ledger.conflicts[record.conflictID]
	inspection := ReconciliationConflictInspection{ReconciliationConflict: conflict}
	if resolution, resolved := ledger.resolutions[record.conflictID]; resolved {
		inspection.Resolution = &resolution
	}
	return cloneReconciliationConflictInspection(inspection), found, nil
}

func (ledger *transientLedger) RecordReconciliationConflict(_ context.Context, key string, conflict ReconciliationConflict) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		stored, conflictFound := ledger.conflicts[existing.conflictID]
		if existing.operation == transientReconciliationConflictOperation &&
			conflictFound && reconciliationConflictsEqual(stored, conflict) {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if err := validateReconciliationConflictRecord(
		ledger.changes,
		ledger.versions,
		ledger.amendments,
		ledger.current,
		ledger.promotions,
		ledger.completed,
		conflict,
	); err != nil {
		return err
	}
	ledger.conflicts[conflict.ID] = cloneReconciliationConflict(conflict)
	ledger.conflictIDs = append(ledger.conflictIDs, conflict.ID)
	ledger.idempotency[key] = transientIdempotencyRecord{
		operation:  transientReconciliationConflictOperation,
		versionID:  conflict.Version.ID,
		conflictID: conflict.ID,
	}
	return nil
}

func validateReconciliationConflictRecord(
	changes map[ChangeID]Change,
	versions map[VersionID]Version,
	amendments map[VersionID]Amendment,
	current Revision,
	promotions map[PromotionID]Promotion,
	completed map[VersionID]PromotionID,
	conflict ReconciliationConflict,
) error {
	if conflict.ID == "" || conflict.Change.ID == "" || conflict.Version.ID == "" ||
		conflict.Version.ChangeID != conflict.Change.ID ||
		conflict.FromVersion == "" || conflict.ToVersion == "" || conflict.ReportedBy == "" {
		return errors.New("invalid reconciliation conflict identity")
	}
	from, fromFound := versions[conflict.FromVersion]
	to, toFound := versions[conflict.ToVersion]
	amendment, amendmentFound := amendments[conflict.ToVersion]
	if !fromFound || !toFound || from.ChangeID != to.ChangeID ||
		!amendmentFound || amendment.FromVersion != from.ID || amendment.ToVersion != to.ID {
		return errors.New("invalid reconciliation conflict lineage")
	}
	promotionID, promoted := completed[to.ID]
	if !promoted {
		return ErrVersionNotPromoted
	}
	promotion, found := promotions[promotionID]
	if !found || promotion.ToIntent != current.ID {
		return ErrIntentAdvanced
	}
	descendant, descendantFound := versions[conflict.Version.ID]
	descendantChange, changeFound := changes[conflict.Change.ID]
	if !descendantFound || !changeFound ||
		descendant.ChangeID == from.ChangeID ||
		descendant.BaseIntent != from.BaseIntent ||
		descendant.ChangeID != descendantChange.ID ||
		descendantChange != conflict.Change ||
		!versionsEqual(descendant, conflict.Version) {
		return errors.New("invalid reconciliation descendant version")
	}
	paths, err := NormalizeReconciliationConflictPaths(conflict.AffectedPaths)
	if err != nil || !slices.Equal(paths, conflict.AffectedPaths) {
		return errors.New("invalid reconciliation conflict diagnostics")
	}
	return nil
}

func reconciliationConflictsEqual(left, right ReconciliationConflict) bool {
	return left.ID == right.ID &&
		left.Change == right.Change &&
		left.FromVersion == right.FromVersion &&
		left.ToVersion == right.ToVersion &&
		left.ReportedBy == right.ReportedBy &&
		versionsEqual(left.Version, right.Version) &&
		slices.Equal(left.AffectedPaths, right.AffectedPaths)
}
