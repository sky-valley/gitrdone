package intent

import (
	"context"
	"errors"
	"sync"
)

type IntentStore interface {
	CurrentIntent(ctx context.Context) (Revision, bool, error)
	Revision(ctx context.Context, id RevisionID) (Revision, bool, error)
	Initialize(ctx context.Context, initial Revision) error
}

type ChangeStore interface {
	Change(ctx context.Context, id ChangeID) (Change, bool, error)
	Version(ctx context.Context, id VersionID) (Version, bool, error)
	LatestVersion(ctx context.Context, changeID ChangeID) (Version, bool, error)
	Versions(ctx context.Context, changeID ChangeID, after VersionID, limit int) ([]Version, bool, error)
	ProposalByIdempotencyKey(ctx context.Context, key string) (Proposed, bool, error)
	RecordProposal(ctx context.Context, idempotencyKey string, change Change, version Version) error
}

type PromotionJournal interface {
	PendingPromotion(ctx context.Context) (PreparedPromotion, bool, error)
	CompletedPromotion(ctx context.Context, versionID VersionID) (Promoted, bool, error)
	PreparePromotion(ctx context.Context, prepared PreparedPromotion) error
	CompletePromotion(ctx context.Context, promotionID PromotionID) error
}

type Ledger interface {
	IntentStore
	ChangeStore
	PromotionJournal
}

type transientLedger struct {
	mu          sync.RWMutex
	current     Revision
	revisions   map[RevisionID]Revision
	changes     map[ChangeID]Change
	versions    map[VersionID]Version
	versionIDs  map[ChangeID][]VersionID
	promotions  map[PromotionID]Promotion
	prepared    map[PromotionID]PreparedPromotion
	pending     PromotionID
	completed   map[VersionID]PromotionID
	idempotency map[string]VersionID
}

func (ledger *transientLedger) CurrentIntent(context.Context) (Revision, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	return ledger.current, ledger.current.ID != "", nil
}

func (ledger *transientLedger) Revision(_ context.Context, id RevisionID) (Revision, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	revision, found := ledger.revisions[id]
	return revision, found, nil
}

func (ledger *transientLedger) Change(_ context.Context, id ChangeID) (Change, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	change, found := ledger.changes[id]
	return change, found, nil
}

func (ledger *transientLedger) Version(_ context.Context, id VersionID) (Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	version, found := ledger.versions[id]
	return version, found, nil
}

func (ledger *transientLedger) LatestVersion(_ context.Context, changeID ChangeID) (Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	ids := ledger.versionIDs[changeID]
	if len(ids) == 0 {
		return Version{}, false, nil
	}
	return ledger.versions[ids[len(ids)-1]], true, nil
}

func (ledger *transientLedger) Versions(_ context.Context, changeID ChangeID, after VersionID, limit int) ([]Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	ids := ledger.versionIDs[changeID]
	start := 0
	if after != "" {
		start = -1
		for index, id := range ids {
			if id == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, ErrVersionNotFound
		}
	}
	end := min(start+limit, len(ids))
	versions := make([]Version, 0, end-start)
	for _, id := range ids[start:end] {
		versions = append(versions, ledger.versions[id])
	}
	return versions, end < len(ids), nil
}

func (ledger *transientLedger) ProposalByIdempotencyKey(_ context.Context, key string) (Proposed, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	versionID, found := ledger.idempotency[key]
	if !found {
		return Proposed{}, false, nil
	}
	version := ledger.versions[versionID]
	return Proposed{Change: ledger.changes[version.ChangeID], Version: version}, true, nil
}

func (ledger *transientLedger) PendingPromotion(context.Context) (PreparedPromotion, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	prepared, found := ledger.prepared[ledger.pending]
	return prepared, found, nil
}

func (ledger *transientLedger) CompletedPromotion(_ context.Context, versionID VersionID) (Promoted, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	promotionID, found := ledger.completed[versionID]
	if !found {
		return Promoted{}, false, nil
	}
	promotion := ledger.promotions[promotionID]
	return Promoted{Promotion: promotion, Intent: ledger.revisions[promotion.ToIntent]}, true, nil
}

func (ledger *transientLedger) Initialize(_ context.Context, initial Revision) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.current = initial
	ledger.revisions = map[RevisionID]Revision{initial.ID: initial}
	ledger.changes = make(map[ChangeID]Change)
	ledger.versions = make(map[VersionID]Version)
	ledger.versionIDs = make(map[ChangeID][]VersionID)
	ledger.promotions = make(map[PromotionID]Promotion)
	ledger.prepared = make(map[PromotionID]PreparedPromotion)
	ledger.completed = make(map[VersionID]PromotionID)
	ledger.idempotency = make(map[string]VersionID)
	return nil
}

func (ledger *transientLedger) RecordProposal(_ context.Context, key string, change Change, version Version) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.changes[change.ID] = change
	ledger.versions[version.ID] = version
	ledger.versionIDs[change.ID] = append(ledger.versionIDs[change.ID], version.ID)
	ledger.idempotency[key] = version.ID
	return nil
}

func (ledger *transientLedger) PreparePromotion(_ context.Context, prepared PreparedPromotion) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.pending != "" && ledger.pending != prepared.Promotion.ID {
		return ErrPromotionPending
	}
	ledger.prepared[prepared.Promotion.ID] = prepared
	ledger.pending = prepared.Promotion.ID
	return nil
}

func (ledger *transientLedger) CompletePromotion(_ context.Context, promotionID PromotionID) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	prepared, found := ledger.prepared[promotionID]
	if !found {
		if _, completed := ledger.promotions[promotionID]; completed {
			return nil
		}
		return errors.New("prepared promotion not found")
	}
	ledger.promotions[promotionID] = prepared.Promotion
	ledger.revisions[prepared.Intent.ID] = prepared.Intent
	ledger.completed[prepared.Promotion.VersionID] = promotionID
	ledger.current = prepared.Intent
	delete(ledger.prepared, promotionID)
	ledger.pending = ""
	return nil
}
