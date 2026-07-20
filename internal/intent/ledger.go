package intent

import (
	"context"
	"sync"
)

type Ledger interface {
	CurrentIntent(ctx context.Context) (Revision, bool, error)
	Revision(ctx context.Context, id RevisionID) (Revision, bool, error)
	Version(ctx context.Context, id VersionID) (Version, bool, error)
	ProposalByIdempotencyKey(ctx context.Context, key string) (Proposed, bool, error)
	Initialize(ctx context.Context, initial Revision) error
	RecordProposal(ctx context.Context, idempotencyKey string, change Change, version Version) error
	RecordPromotion(ctx context.Context, promotion Promotion, nextIntent Revision) error
}

type transientLedger struct {
	mu          sync.RWMutex
	current     Revision
	revisions   map[RevisionID]Revision
	changes     map[ChangeID]Change
	versions    map[VersionID]Version
	promotions  map[PromotionID]Promotion
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

func (ledger *transientLedger) Version(_ context.Context, id VersionID) (Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	version, found := ledger.versions[id]
	return version, found, nil
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

func (ledger *transientLedger) Initialize(_ context.Context, initial Revision) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.current = initial
	ledger.revisions = map[RevisionID]Revision{initial.ID: initial}
	ledger.changes = make(map[ChangeID]Change)
	ledger.versions = make(map[VersionID]Version)
	ledger.promotions = make(map[PromotionID]Promotion)
	ledger.idempotency = make(map[string]VersionID)
	return nil
}

func (ledger *transientLedger) RecordProposal(_ context.Context, key string, change Change, version Version) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.changes[change.ID] = change
	ledger.versions[version.ID] = version
	ledger.idempotency[key] = version.ID
	return nil
}

func (ledger *transientLedger) RecordPromotion(_ context.Context, promotion Promotion, nextIntent Revision) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.promotions[promotion.ID] = promotion
	ledger.revisions[nextIntent.ID] = nextIntent
	ledger.current = nextIntent
	return nil
}
