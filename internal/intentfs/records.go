package intentfs

import (
	"errors"
	"fmt"
	"slices"

	"github.com/sky-valley/gitrdone/internal/intent"
)

const (
	repositoryInitialized = "repository_initialized"
	proposalRecorded      = "proposal_recorded"
	promotionPrepared     = "promotion_prepared"
	promotionCompleted    = "promotion_completed"
	promotionRecorded     = "promotion_recorded"
)

type journalState struct {
	current     intent.Revision
	revisions   map[intent.RevisionID]intent.Revision
	changes     map[intent.ChangeID]intent.Change
	versions    map[intent.VersionID]intent.Version
	versionIDs  map[intent.ChangeID][]intent.VersionID
	dependents  map[intent.VersionID][]intent.VersionID
	promotions  map[intent.PromotionID]intent.Promotion
	prepared    map[intent.PromotionID]intent.PreparedPromotion
	pending     intent.PromotionID
	completed   map[intent.VersionID]intent.PromotionID
	byIntent    map[intent.RevisionID]intent.PromotionID
	idempotency map[string]intent.VersionID
}

type journalRecord struct {
	Format         int                `json:"format"`
	Kind           string             `json:"kind"`
	Initial        *intent.Revision   `json:"initial,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
	Change         *intent.Change     `json:"change,omitempty"`
	Version        *intent.Version    `json:"version,omitempty"`
	Promotion      *intent.Promotion  `json:"promotion,omitempty"`
	PromotionID    intent.PromotionID `json:"promotion_id,omitempty"`
	NextIntent     *intent.Revision   `json:"next_intent,omitempty"`
}

func validateRecord(state *journalState, record journalRecord) error {
	if record.Format != journalFormat {
		return fmt.Errorf("unsupported journal format %d", record.Format)
	}
	switch record.Kind {
	case repositoryInitialized:
		return validateInitialization(state, record)
	case proposalRecorded:
		return validateProposal(state, record)
	case promotionPrepared:
		return validatePreparedPromotion(state, record)
	case promotionCompleted:
		return validateCompletedPromotion(state, record)
	case promotionRecorded:
		return validatePromotion(state, record)
	default:
		return fmt.Errorf("unknown journal record kind %q", record.Kind)
	}
}

func validateInitialization(state *journalState, record journalRecord) error {
	if record.Initial == nil || record.Initial.ID == "" || record.Initial.PreviousID != "" || record.Initial.Content.Engine == "" || record.Initial.Content.Revision == "" {
		return errors.New("invalid repository initialization")
	}
	if state.current.ID != "" {
		if state.current == *record.Initial && len(state.revisions) == 1 {
			return nil
		}
		return errors.New("repository is already initialized")
	}
	return nil
}

func validateProposal(state *journalState, record journalRecord) error {
	if state.current.ID == "" {
		return errors.New("proposal precedes repository initialization")
	}
	if record.IdempotencyKey == "" || record.Change == nil || record.Version == nil {
		return errors.New("invalid proposal record")
	}
	if existingVersionID, ok := state.idempotency[record.IdempotencyKey]; ok {
		existingVersion, versionFound := state.versions[existingVersionID]
		existingChange, changeFound := state.changes[record.Change.ID]
		if versionFound && changeFound && sameVersion(existingVersion, *record.Version) && existingChange == *record.Change {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	if record.Change.ID == "" || record.Version.ID == "" || record.Version.ChangeID != record.Change.ID {
		return errors.New("invalid proposal identity")
	}
	if _, found := state.revisions[record.Version.BaseIntent]; !found {
		return errors.New("proposal base intent is not recorded")
	}
	if record.Version.Content.Engine == "" || record.Version.Content.Revision == "" || record.Version.Producer == "" {
		return errors.New("invalid proposal version")
	}
	seenDependencies := make(map[intent.VersionID]struct{}, len(record.Version.Dependencies))
	for _, dependencyID := range record.Version.Dependencies {
		if dependencyID == "" {
			return errors.New("invalid proposal dependency")
		}
		if _, duplicate := seenDependencies[dependencyID]; duplicate {
			return errors.New("duplicate proposal dependency")
		}
		seenDependencies[dependencyID] = struct{}{}
		if _, found := state.versions[dependencyID]; !found {
			return errors.New("proposal dependency is not recorded")
		}
	}
	if _, found := state.changes[record.Change.ID]; found {
		return errors.New("duplicate change id")
	}
	if _, found := state.versions[record.Version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}

func sameVersion(left, right intent.Version) bool {
	return left.ID == right.ID &&
		left.ChangeID == right.ChangeID &&
		left.BaseIntent == right.BaseIntent &&
		left.Content == right.Content &&
		left.Producer == right.Producer &&
		slices.Equal(left.Dependencies, right.Dependencies)
}

func cloneVersion(version intent.Version) intent.Version {
	version.Dependencies = slices.Clone(version.Dependencies)
	return version
}

func validatePromotion(state *journalState, record journalRecord) error {
	if record.Promotion == nil || record.NextIntent == nil {
		return errors.New("invalid promotion record")
	}
	if existing, found := state.promotions[record.Promotion.ID]; found {
		revision, revisionFound := state.revisions[record.NextIntent.ID]
		if revisionFound && existing == *record.Promotion && revision == *record.NextIntent {
			return nil
		}
		return errors.New("promotion id is already recorded differently")
	}
	promotion := *record.Promotion
	nextIntent := *record.NextIntent
	version, versionFound := state.versions[promotion.VersionID]
	if promotion.ID == "" || !versionFound {
		return errors.New("promotion references an unknown version")
	}
	if promotion.FromIntent != state.current.ID || promotion.ToIntent != nextIntent.ID || nextIntent.PreviousID != state.current.ID || nextIntent.Content != version.Content {
		return errors.New("promotion does not advance the current intent to its version content")
	}
	if _, found := state.revisions[nextIntent.ID]; found {
		return errors.New("duplicate intent revision id")
	}
	return nil
}

func validatePreparedPromotion(state *journalState, record journalRecord) error {
	if record.Promotion == nil || record.NextIntent == nil {
		return errors.New("invalid prepared promotion record")
	}
	prepared := intent.PreparedPromotion{Promotion: *record.Promotion, Intent: *record.NextIntent}
	if existing, found := state.prepared[prepared.Promotion.ID]; found {
		if existing == prepared {
			return nil
		}
		return errors.New("promotion id is already prepared differently")
	}
	if existing, found := state.promotions[prepared.Promotion.ID]; found {
		revision := state.revisions[existing.ToIntent]
		if existing == prepared.Promotion && revision == prepared.Intent {
			return nil
		}
		return errors.New("promotion id is already completed differently")
	}
	if state.pending != "" {
		return intent.ErrPromotionPending
	}
	version, found := state.versions[prepared.Promotion.VersionID]
	if prepared.Promotion.ID == "" || !found {
		return errors.New("prepared promotion references an unknown version")
	}
	if _, exists := state.completed[version.ID]; exists {
		return errors.New("version already has a completed promotion")
	}
	if prepared.Promotion.FromIntent != state.current.ID || prepared.Promotion.ToIntent != prepared.Intent.ID || prepared.Intent.PreviousID != state.current.ID || prepared.Intent.Content != version.Content {
		return errors.New("prepared promotion does not advance current intent to its version content")
	}
	if _, found := state.revisions[prepared.Intent.ID]; found {
		return errors.New("duplicate intent revision id")
	}
	return nil
}

func validateCompletedPromotion(state *journalState, record journalRecord) error {
	if record.PromotionID == "" {
		return errors.New("completed promotion id is required")
	}
	if _, found := state.promotions[record.PromotionID]; found {
		return nil
	}
	if _, found := state.prepared[record.PromotionID]; !found {
		return errors.New("prepared promotion not found")
	}
	return nil
}

func newJournalState() journalState {
	return journalState{
		revisions:   make(map[intent.RevisionID]intent.Revision),
		changes:     make(map[intent.ChangeID]intent.Change),
		versions:    make(map[intent.VersionID]intent.Version),
		versionIDs:  make(map[intent.ChangeID][]intent.VersionID),
		dependents:  make(map[intent.VersionID][]intent.VersionID),
		promotions:  make(map[intent.PromotionID]intent.Promotion),
		prepared:    make(map[intent.PromotionID]intent.PreparedPromotion),
		completed:   make(map[intent.VersionID]intent.PromotionID),
		byIntent:    make(map[intent.RevisionID]intent.PromotionID),
		idempotency: make(map[string]intent.VersionID),
	}
}

func applyValidatedRecord(state *journalState, record journalRecord) {
	switch record.Kind {
	case repositoryInitialized:
		if state.current.ID == "" {
			state.current = *record.Initial
			state.revisions[record.Initial.ID] = *record.Initial
		}
	case proposalRecorded:
		if _, exists := state.idempotency[record.IdempotencyKey]; !exists {
			state.changes[record.Change.ID] = *record.Change
			state.versions[record.Version.ID] = cloneVersion(*record.Version)
			state.versionIDs[record.Change.ID] = append(state.versionIDs[record.Change.ID], record.Version.ID)
			for _, dependencyID := range record.Version.Dependencies {
				state.dependents[dependencyID] = append(state.dependents[dependencyID], record.Version.ID)
			}
			state.idempotency[record.IdempotencyKey] = record.Version.ID
		}
	case promotionPrepared:
		if _, exists := state.promotions[record.Promotion.ID]; exists {
			return
		}
		if _, exists := state.prepared[record.Promotion.ID]; !exists {
			prepared := intent.PreparedPromotion{Promotion: *record.Promotion, Intent: *record.NextIntent}
			state.prepared[prepared.Promotion.ID] = prepared
			state.pending = prepared.Promotion.ID
		}
	case promotionCompleted:
		if _, exists := state.promotions[record.PromotionID]; !exists {
			prepared := state.prepared[record.PromotionID]
			state.revisions[prepared.Intent.ID] = prepared.Intent
			state.promotions[prepared.Promotion.ID] = prepared.Promotion
			state.completed[prepared.Promotion.VersionID] = prepared.Promotion.ID
			state.byIntent[prepared.Promotion.ToIntent] = prepared.Promotion.ID
			state.current = prepared.Intent
			delete(state.prepared, prepared.Promotion.ID)
			state.pending = ""
		}
	case promotionRecorded:
		if _, exists := state.promotions[record.Promotion.ID]; !exists {
			state.revisions[record.NextIntent.ID] = *record.NextIntent
			state.promotions[record.Promotion.ID] = *record.Promotion
			state.completed[record.Promotion.VersionID] = record.Promotion.ID
			state.byIntent[record.Promotion.ToIntent] = record.Promotion.ID
			state.current = *record.NextIntent
		}
	}
}
