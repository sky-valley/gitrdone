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
