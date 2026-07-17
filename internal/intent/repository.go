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
	BaseIntent RevisionID
	Content    ContentRef
	Producer   string
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

type TrunkProjection interface {
	Advance(ctx context.Context, expected, next ContentRef) error
}

type Repository struct {
	promotionMu sync.Mutex
	stateMu     sync.RWMutex
	current     Revision
	projection  TrunkProjection
	revisions   map[RevisionID]Revision
	versions    map[VersionID]Version
}

func NewRepository(initial ContentRef, projection TrunkProjection) (*Repository, error) {
	if initial.Engine == "" || initial.Revision == "" {
		return nil, errors.New("initial content reference requires engine and revision")
	}
	if projection == nil {
		return nil, errors.New("trunk projection is required")
	}

	initialID, err := newID("intent")
	if err != nil {
		return nil, fmt.Errorf("create initial intent id: %w", err)
	}
	initialIntent := Revision{
		ID:      RevisionID(initialID),
		Content: initial,
	}
	return &Repository{
		current:    initialIntent,
		projection: projection,
		revisions: map[RevisionID]Revision{
			initialIntent.ID: initialIntent,
		},
		versions: make(map[VersionID]Version),
	}, nil
}

func (repository *Repository) CurrentIntent() Revision {
	repository.stateMu.RLock()
	defer repository.stateMu.RUnlock()
	return repository.current
}

func (repository *Repository) Propose(proposal Proposal) (Proposed, error) {
	if proposal.Content.Engine == "" || proposal.Content.Revision == "" {
		return Proposed{}, errors.New("proposed content reference requires engine and revision")
	}
	if proposal.Producer == "" {
		return Proposed{}, errors.New("proposal producer is required")
	}

	changeID, err := newID("change")
	if err != nil {
		return Proposed{}, fmt.Errorf("create change id: %w", err)
	}
	versionID, err := newID("version")
	if err != nil {
		return Proposed{}, fmt.Errorf("create version id: %w", err)
	}

	repository.stateMu.Lock()
	defer repository.stateMu.Unlock()
	if _, ok := repository.revisions[proposal.BaseIntent]; !ok {
		return Proposed{}, ErrIntentNotFound
	}

	change := Change{ID: ChangeID(changeID)}
	version := Version{
		ID:         VersionID(versionID),
		ChangeID:   change.ID,
		BaseIntent: proposal.BaseIntent,
		Content:    proposal.Content,
		Producer:   proposal.Producer,
	}
	repository.versions[version.ID] = version

	return Proposed{Change: change, Version: version}, nil
}

func (repository *Repository) Promote(ctx context.Context, request PromoteRequest) (Promoted, error) {
	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()

	repository.stateMu.RLock()
	version, ok := repository.versions[request.VersionID]
	current := repository.current
	repository.stateMu.RUnlock()
	if !ok {
		return Promoted{}, ErrVersionNotFound
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

	if err := repository.projection.Advance(ctx, current.Content, nextIntent.Content); err != nil {
		return Promoted{}, fmt.Errorf("advance trunk projection: %w", err)
	}

	repository.stateMu.Lock()
	repository.current = nextIntent
	repository.revisions[nextIntent.ID] = nextIntent
	repository.stateMu.Unlock()

	return Promoted{
		Promotion: promotion,
		Intent:    nextIntent,
	}, nil
}

func newID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(random[:]), nil
}
