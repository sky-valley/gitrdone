package intent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

var ErrIntentAdvanced = errors.New("canonical intent advanced")
var ErrIntentNotFound = errors.New("intent not found")
var ErrVersionNotFound = errors.New("change version not found")
var ErrIdempotencyConflict = errors.New("idempotency key already used for a different proposal")
var ErrPromotionPending = errors.New("another promotion is pending reconciliation")

type ContentRef struct {
	Engine   string
	Revision string
}

type RevisionID string

type ChangeID string

type VersionID string

type PromotionID string

type Revision struct {
	ID         RevisionID
	PreviousID RevisionID
	Content    ContentRef
}

type Change struct {
	ID ChangeID
}

type Version struct {
	ID         VersionID
	ChangeID   ChangeID
	BaseIntent RevisionID
	Content    ContentRef
	Producer   string
}

type Promotion struct {
	ID         PromotionID
	FromIntent RevisionID
	ToIntent   RevisionID
	VersionID  VersionID
}

type Proposal struct {
	IdempotencyKey string
	BaseIntent     RevisionID
	Content        ContentRef
	Producer       string
}

type Proposed struct {
	Change  Change
	Version Version
}

type PromoteRequest struct {
	VersionID      VersionID
	ExpectedIntent RevisionID
}

type Promoted struct {
	Promotion Promotion
	Intent    Revision
}

type PreparedPromotion struct {
	Promotion Promotion
	Intent    Revision
}

type ContentAdmission interface {
	Admit(ctx context.Context, versionID VersionID, content ContentRef) error
}

type TrunkProjection interface {
	Current(ctx context.Context) (ContentRef, error)
	Advance(ctx context.Context, expected, next ContentRef) error
}

type Repository struct {
	proposalMu  sync.Mutex
	promotionMu sync.Mutex
	stateMu     sync.RWMutex
	current     Revision
	admission   ContentAdmission
	projection  TrunkProjection
	ledger      Ledger
	conflict    *ReconciliationConflict
}

func NewRepository(initial ContentRef, admission ContentAdmission, projection TrunkProjection) (*Repository, error) {
	return OpenRepository(context.Background(), initial, &transientLedger{}, admission, projection)
}

func OpenRepository(ctx context.Context, initial ContentRef, ledger Ledger, admission ContentAdmission, projection TrunkProjection) (*Repository, error) {
	if initial.Engine == "" || initial.Revision == "" {
		return nil, errors.New("initial content reference requires engine and revision")
	}
	if ledger == nil {
		return nil, errors.New("intent ledger is required")
	}
	if admission == nil {
		return nil, errors.New("content admission is required")
	}
	if projection == nil {
		return nil, errors.New("trunk projection is required")
	}

	current, found, err := ledger.CurrentIntent(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current intent: %w", err)
	}
	if !found {
		initialID, err := newID("intent")
		if err != nil {
			return nil, fmt.Errorf("create initial intent id: %w", err)
		}
		current = Revision{
			ID:      RevisionID(initialID),
			Content: initial,
		}
		if err := ledger.Initialize(ctx, current); err != nil {
			return nil, fmt.Errorf("initialize intent ledger: %w", err)
		}
	}

	repository := &Repository{
		current:    current,
		admission:  admission,
		projection: projection,
		ledger:     ledger,
	}
	if err := repository.Reconcile(ctx); err != nil {
		var conflict *ReconciliationConflict
		if !errors.As(err, &conflict) {
			return nil, fmt.Errorf("reconcile repository: %w", err)
		}
	}
	return repository, nil
}

func (repository *Repository) CurrentIntent() Revision {
	repository.stateMu.RLock()
	defer repository.stateMu.RUnlock()
	return repository.current
}

func (repository *Repository) ReconciliationConflict() (ReconciliationConflict, bool) {
	repository.stateMu.RLock()
	defer repository.stateMu.RUnlock()
	if repository.conflict == nil {
		return ReconciliationConflict{}, false
	}
	return *repository.conflict, true
}

func (repository *Repository) Propose(ctx context.Context, proposal Proposal) (Proposed, error) {
	if proposal.IdempotencyKey == "" {
		return Proposed{}, errors.New("proposal idempotency key is required")
	}
	if proposal.Content.Engine == "" || proposal.Content.Revision == "" {
		return Proposed{}, errors.New("proposed content reference requires engine and revision")
	}
	if proposal.Producer == "" {
		return Proposed{}, errors.New("proposal producer is required")
	}

	repository.proposalMu.Lock()
	defer repository.proposalMu.Unlock()

	existing, found, err := repository.ledger.ProposalByIdempotencyKey(ctx, proposal.IdempotencyKey)
	if err != nil {
		return Proposed{}, fmt.Errorf("read proposal idempotency record: %w", err)
	}
	if found {
		if existing.Version.BaseIntent != proposal.BaseIntent || existing.Version.Content != proposal.Content || existing.Version.Producer != proposal.Producer {
			return Proposed{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	_, found, err = repository.ledger.Revision(ctx, proposal.BaseIntent)
	if err != nil {
		return Proposed{}, fmt.Errorf("read proposal base intent: %w", err)
	}
	if !found {
		return Proposed{}, ErrIntentNotFound
	}

	changeID, err := newID("change")
	if err != nil {
		return Proposed{}, fmt.Errorf("create change id: %w", err)
	}
	versionID, err := newID("version")
	if err != nil {
		return Proposed{}, fmt.Errorf("create version id: %w", err)
	}

	version := Version{
		ID:         VersionID(versionID),
		ChangeID:   ChangeID(changeID),
		BaseIntent: proposal.BaseIntent,
		Content:    proposal.Content,
		Producer:   proposal.Producer,
	}
	if err := repository.admission.Admit(ctx, version.ID, version.Content); err != nil {
		return Proposed{}, fmt.Errorf("admit proposed content: %w", err)
	}
	change := Change{ID: version.ChangeID}
	if err := repository.ledger.RecordProposal(ctx, proposal.IdempotencyKey, change, version); err != nil {
		return Proposed{}, fmt.Errorf("record proposal: %w", err)
	}

	return Proposed{Change: change, Version: version}, nil
}

func (repository *Repository) Promote(ctx context.Context, request PromoteRequest) (Promoted, error) {
	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()

	completed, found, err := repository.ledger.CompletedPromotion(ctx, request.VersionID)
	if err != nil {
		return Promoted{}, fmt.Errorf("read completed promotion: %w", err)
	}
	if found {
		if completed.Promotion.FromIntent != request.ExpectedIntent {
			return Promoted{}, ErrIntentAdvanced
		}
		repository.stateMu.Lock()
		repository.current = completed.Intent
		repository.conflict = nil
		repository.stateMu.Unlock()
		return completed, nil
	}
	pending, found, err := repository.ledger.PendingPromotion(ctx)
	if err != nil {
		return Promoted{}, fmt.Errorf("read pending promotion: %w", err)
	}
	if found {
		if pending.Promotion.VersionID != request.VersionID || pending.Promotion.FromIntent != request.ExpectedIntent {
			return Promoted{}, ErrPromotionPending
		}
		return repository.reconcileAndRemember(ctx, pending)
	}

	version, found, err := repository.ledger.Version(ctx, request.VersionID)
	if err != nil {
		return Promoted{}, fmt.Errorf("read change version: %w", err)
	}
	if !found {
		return Promoted{}, ErrVersionNotFound
	}
	current, found, err := repository.ledger.CurrentIntent(ctx)
	if err != nil {
		return Promoted{}, fmt.Errorf("read current intent: %w", err)
	}
	if !found {
		return Promoted{}, errors.New("intent ledger is not initialized")
	}
	if request.ExpectedIntent != current.ID || version.BaseIntent != current.ID {
		return Promoted{}, ErrIntentAdvanced
	}

	nextIntentID, err := newID("intent")
	if err != nil {
		return Promoted{}, fmt.Errorf("create next intent id: %w", err)
	}
	promotionID, err := newID("promotion")
	if err != nil {
		return Promoted{}, fmt.Errorf("create promotion id: %w", err)
	}

	nextIntent := Revision{
		ID:         RevisionID(nextIntentID),
		PreviousID: current.ID,
		Content:    version.Content,
	}
	promotion := Promotion{
		ID:         PromotionID(promotionID),
		FromIntent: current.ID,
		ToIntent:   nextIntent.ID,
		VersionID:  version.ID,
	}
	prepared := PreparedPromotion{Promotion: promotion, Intent: nextIntent}
	if err := repository.ledger.PreparePromotion(ctx, prepared); err != nil {
		return Promoted{}, fmt.Errorf("prepare promotion: %w", err)
	}
	return repository.reconcileAndRemember(ctx, prepared)
}

func (repository *Repository) Reconcile(ctx context.Context) error {
	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()

	pending, found, err := repository.ledger.PendingPromotion(ctx)
	if err != nil {
		return fmt.Errorf("read pending promotion: %w", err)
	}
	if !found {
		repository.setReconciliationConflict(nil)
		return nil
	}
	_, err = repository.reconcileAndRemember(ctx, pending)
	return err
}

func (repository *Repository) reconcileAndRemember(ctx context.Context, prepared PreparedPromotion) (Promoted, error) {
	promoted, err := repository.reconcilePrepared(ctx, prepared)
	if err != nil {
		var conflict *ReconciliationConflict
		if errors.As(err, &conflict) {
			repository.setReconciliationConflict(conflict)
		}
		return Promoted{}, err
	}
	repository.setReconciliationConflict(nil)
	return promoted, nil
}

func (repository *Repository) reconcilePrepared(ctx context.Context, prepared PreparedPromotion) (Promoted, error) {
	from, found, err := repository.ledger.Revision(ctx, prepared.Promotion.FromIntent)
	if err != nil {
		return Promoted{}, fmt.Errorf("read promotion base intent: %w", err)
	}
	if !found {
		return Promoted{}, ErrIntentNotFound
	}
	actual, err := repository.projection.Current(ctx)
	if err != nil {
		return Promoted{}, fmt.Errorf("read trunk projection: %w", err)
	}
	if actual == from.Content {
		advanceErr := repository.projection.Advance(ctx, from.Content, prepared.Intent.Content)
		if advanceErr == nil {
			actual = prepared.Intent.Content
		} else if !errors.Is(advanceErr, ErrIntentAdvanced) {
			return Promoted{}, fmt.Errorf("advance trunk projection: %w", advanceErr)
		} else {
			actual, err = repository.projection.Current(ctx)
			if err != nil {
				return Promoted{}, fmt.Errorf("reread trunk projection after compare-and-swap failure: %w", err)
			}
			if actual == from.Content {
				return Promoted{}, fmt.Errorf("advance trunk projection: %w", advanceErr)
			}
		}
	}
	if actual != prepared.Intent.Content {
		return Promoted{}, &ReconciliationConflict{
			Prepared: prepared,
			Expected: from.Content,
			Actual:   actual,
		}
	}
	if err := repository.ledger.CompletePromotion(ctx, prepared.Promotion.ID); err != nil {
		return Promoted{}, fmt.Errorf("complete promotion: %w", err)
	}

	repository.stateMu.Lock()
	repository.current = prepared.Intent
	repository.stateMu.Unlock()

	return Promoted{
		Promotion: prepared.Promotion,
		Intent:    prepared.Intent,
	}, nil
}

func (repository *Repository) setReconciliationConflict(conflict *ReconciliationConflict) {
	repository.stateMu.Lock()
	defer repository.stateMu.Unlock()
	repository.conflict = conflict
}

type ReconciliationConflict struct {
	Prepared PreparedPromotion
	Expected ContentRef
	Actual   ContentRef
}

func (conflict *ReconciliationConflict) Error() string {
	return "trunk projection diverged from the prepared promotion"
}

func newID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(random[:]), nil
}
