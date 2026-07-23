package intentfs

import (
	"context"
	"errors"

	"github.com/sky-valley/gitrdone/internal/intent"
)

func (ledger *Ledger) ReconciliationConflict(ctx context.Context, id intent.ConflictID) (intent.ReconciliationConflict, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.ReconciliationConflict{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.ReconciliationConflict{}, false, errors.New("journal is closed")
	}
	conflict, found := ledger.state.conflicts[id]
	return cloneReconciliationConflict(conflict), found, nil
}

func (ledger *Ledger) ReconciliationConflicts(ctx context.Context, after intent.ConflictID, limit int) ([]intent.ReconciliationConflict, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	start := 0
	if after != "" {
		start = -1
		for index, id := range ledger.state.conflictIDs {
			if id == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, intent.ErrReconciliationConflictNotFound
		}
	}
	end := min(start+limit, len(ledger.state.conflictIDs))
	conflicts := make([]intent.ReconciliationConflict, 0, end-start)
	for _, id := range ledger.state.conflictIDs[start:end] {
		conflicts = append(conflicts, cloneReconciliationConflict(ledger.state.conflicts[id]))
	}
	return conflicts, end < len(ledger.state.conflictIDs), nil
}

func (ledger *Ledger) ReconciliationConflictByIdempotencyKey(ctx context.Context, key string) (intent.ReconciliationConflict, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.ReconciliationConflict{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.ReconciliationConflict{}, false, errors.New("journal is closed")
	}
	record, found := ledger.state.idempotency[key]
	if !found {
		return intent.ReconciliationConflict{}, false, nil
	}
	if record.operation != reconciliationConflictOperation {
		return intent.ReconciliationConflict{}, false, intent.ErrIdempotencyConflict
	}
	conflict, found := ledger.state.conflicts[record.conflictID]
	return cloneReconciliationConflict(conflict), found, nil
}

func (ledger *Ledger) RecordReconciliationConflict(ctx context.Context, key string, conflict intent.ReconciliationConflict) error {
	copy := cloneReconciliationConflict(conflict)
	return ledger.append(ctx, journalRecord{
		Format:                 journalFormat,
		Kind:                   reconciliationConflictRecorded,
		IdempotencyKey:         key,
		ReconciliationConflict: &copy,
	})
}
