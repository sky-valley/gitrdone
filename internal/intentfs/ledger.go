package intentfs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sky-valley/gitrdone/internal/intent"
	"golang.org/x/sys/unix"
)

const journalFormat = 1
const maxRecordBytes = 1024 * 1024

const (
	repositoryInitialized = "repository_initialized"
	proposalRecorded      = "proposal_recorded"
	promotionPrepared     = "promotion_prepared"
	promotionCompleted    = "promotion_completed"
	promotionRecorded     = "promotion_recorded"
)

type Ledger struct {
	mu          sync.Mutex
	file        *os.File
	syncJournal func() error
	state       journalState
	closed      bool
	failed      error
}

var _ intent.Ledger = (*Ledger)(nil)

type journalState struct {
	current     intent.Revision
	revisions   map[intent.RevisionID]intent.Revision
	changes     map[intent.ChangeID]intent.Change
	versions    map[intent.VersionID]intent.Version
	versionIDs  map[intent.ChangeID][]intent.VersionID
	promotions  map[intent.PromotionID]intent.Promotion
	prepared    map[intent.PromotionID]intent.PreparedPromotion
	pending     intent.PromotionID
	completed   map[intent.VersionID]intent.PromotionID
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

func Open(path string) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("journal path is required")
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock journal: %w", err)
	}
	if created {
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
			_ = file.Close()
			return nil, fmt.Errorf("sync journal directory: %w", err)
		}
	}
	ledger := &Ledger{file: file, syncJournal: file.Sync, state: newJournalState()}
	if err := ledger.replay(); err != nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
		return nil, err
	}
	return ledger, nil
}

func (ledger *Ledger) Close() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil
	}
	ledger.closed = true
	unlockErr := unix.Flock(int(ledger.file.Fd()), unix.LOCK_UN)
	closeErr := ledger.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock journal: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close journal: %w", closeErr)
	}
	return nil
}

func (ledger *Ledger) CurrentIntent(ctx context.Context) (intent.Revision, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Revision{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Revision{}, false, errors.New("journal is closed")
	}
	return ledger.state.current, ledger.state.current.ID != "", nil
}

func (ledger *Ledger) Revision(ctx context.Context, id intent.RevisionID) (intent.Revision, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Revision{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Revision{}, false, errors.New("journal is closed")
	}
	revision, found := ledger.state.revisions[id]
	return revision, found, nil
}

func (ledger *Ledger) Change(ctx context.Context, id intent.ChangeID) (intent.Change, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Change{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Change{}, false, errors.New("journal is closed")
	}
	change, found := ledger.state.changes[id]
	return change, found, nil
}

func (ledger *Ledger) Version(ctx context.Context, id intent.VersionID) (intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Version{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Version{}, false, errors.New("journal is closed")
	}
	version, found := ledger.state.versions[id]
	return version, found, nil
}

func (ledger *Ledger) LatestVersion(ctx context.Context, changeID intent.ChangeID) (intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Version{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Version{}, false, errors.New("journal is closed")
	}
	ids := ledger.state.versionIDs[changeID]
	if len(ids) == 0 {
		return intent.Version{}, false, nil
	}
	return ledger.state.versions[ids[len(ids)-1]], true, nil
}

func (ledger *Ledger) Versions(ctx context.Context, changeID intent.ChangeID, after intent.VersionID, limit int) ([]intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	ids := ledger.state.versionIDs[changeID]
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
			return nil, false, intent.ErrVersionNotFound
		}
	}
	end := min(start+limit, len(ids))
	versions := make([]intent.Version, 0, end-start)
	for _, id := range ids[start:end] {
		versions = append(versions, ledger.state.versions[id])
	}
	return versions, end < len(ids), nil
}

func (ledger *Ledger) ProposalByIdempotencyKey(ctx context.Context, key string) (intent.Proposed, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Proposed{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Proposed{}, false, errors.New("journal is closed")
	}
	versionID, found := ledger.state.idempotency[key]
	if !found {
		return intent.Proposed{}, false, nil
	}
	version := ledger.state.versions[versionID]
	return intent.Proposed{
		Change:  ledger.state.changes[version.ChangeID],
		Version: version,
	}, true, nil
}

func (ledger *Ledger) PendingPromotion(ctx context.Context) (intent.PreparedPromotion, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.PreparedPromotion{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.PreparedPromotion{}, false, errors.New("journal is closed")
	}
	prepared, found := ledger.state.prepared[ledger.state.pending]
	return prepared, found, nil
}

func (ledger *Ledger) CompletedPromotion(ctx context.Context, versionID intent.VersionID) (intent.Promoted, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Promoted{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Promoted{}, false, errors.New("journal is closed")
	}
	promotionID, found := ledger.state.completed[versionID]
	if !found {
		return intent.Promoted{}, false, nil
	}
	promotion := ledger.state.promotions[promotionID]
	return intent.Promoted{
		Promotion: promotion,
		Intent:    ledger.state.revisions[promotion.ToIntent],
	}, true, nil
}

func (ledger *Ledger) Initialize(ctx context.Context, initial intent.Revision) error {
	return ledger.append(ctx, journalRecord{
		Format:  journalFormat,
		Kind:    repositoryInitialized,
		Initial: &initial,
	})
}

func (ledger *Ledger) RecordProposal(ctx context.Context, idempotencyKey string, change intent.Change, version intent.Version) error {
	return ledger.append(ctx, journalRecord{
		Format:         journalFormat,
		Kind:           proposalRecorded,
		IdempotencyKey: idempotencyKey,
		Change:         &change,
		Version:        &version,
	})
}

func (ledger *Ledger) PreparePromotion(ctx context.Context, prepared intent.PreparedPromotion) error {
	return ledger.append(ctx, journalRecord{
		Format:     journalFormat,
		Kind:       promotionPrepared,
		Promotion:  &prepared.Promotion,
		NextIntent: &prepared.Intent,
	})
}

func (ledger *Ledger) CompletePromotion(ctx context.Context, promotionID intent.PromotionID) error {
	return ledger.append(ctx, journalRecord{
		Format:      journalFormat,
		Kind:        promotionCompleted,
		PromotionID: promotionID,
	})
}

func (ledger *Ledger) append(ctx context.Context, record journalRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return errors.New("journal is closed")
	}
	if ledger.failed != nil {
		return fmt.Errorf("journal cannot accept writes after a failed rollback: %w", ledger.failed)
	}

	if err := validateRecord(&ledger.state, record); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode journal record: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxRecordBytes {
		return errors.New("journal record is too large")
	}
	info, err := ledger.file.Stat()
	if err != nil {
		return fmt.Errorf("stat journal before append: %w", err)
	}
	written, writeErr := ledger.file.Write(data)
	if writeErr != nil || written != len(data) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		if rollbackErr := truncateAndSync(ledger.file, info.Size()); rollbackErr != nil {
			writeErr = errors.Join(writeErr, fmt.Errorf("rollback partial journal record: %w", rollbackErr))
			ledger.failed = writeErr
		}
		return fmt.Errorf("append journal record: %w", writeErr)
	}
	if syncErr := ledger.syncJournal(); syncErr != nil {
		if rollbackErr := truncateAndSync(ledger.file, info.Size()); rollbackErr != nil {
			ledger.failed = errors.Join(syncErr, fmt.Errorf("rollback unsynced journal record: %w", rollbackErr))
			return fmt.Errorf("sync journal record: %w", ledger.failed)
		}
		return fmt.Errorf("sync journal record: %w", syncErr)
	}
	applyValidatedRecord(&ledger.state, record)
	return nil
}

func (ledger *Ledger) replay() error {
	if err := truncateIncompleteTail(ledger.file); err != nil {
		return fmt.Errorf("recover journal tail: %w", err)
	}
	if _, err := ledger.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek journal: %w", err)
	}
	scanner := bufio.NewScanner(ledger.file)
	scanner.Buffer(make([]byte, 64*1024), maxRecordBytes)
	line := 0
	for scanner.Scan() {
		line++
		var record journalRecord
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return fmt.Errorf("decode journal line %d: %w", line, err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return fmt.Errorf("decode journal line %d: trailing data: %w", line, err)
		}
		if err := validateRecord(&ledger.state, record); err != nil {
			return fmt.Errorf("apply journal line %d: %w", line, err)
		}
		applyValidatedRecord(&ledger.state, record)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read journal: %w", err)
	}
	if _, err := ledger.file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek journal end: %w", err)
	}
	return nil
}

func truncateIncompleteTail(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}

	const blockSize = int64(4096)
	end := info.Size()
	for end > 0 {
		start := end - blockSize
		if start < 0 {
			start = 0
		}
		block := make([]byte, end-start)
		if _, err := file.ReadAt(block, start); err != nil {
			return err
		}
		if newline := bytes.LastIndexByte(block, '\n'); newline >= 0 {
			return truncateAndSync(file, start+int64(newline)+1)
		}
		end = start
	}
	return truncateAndSync(file, 0)
}

func truncateAndSync(file *os.File, size int64) error {
	if err := file.Truncate(size); err != nil {
		return err
	}
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
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
		if versionFound && changeFound && existingVersion == *record.Version && existingChange == *record.Change {
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
	if _, found := state.changes[record.Change.ID]; found {
		return errors.New("duplicate change id")
	}
	if _, found := state.versions[record.Version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
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
		promotions:  make(map[intent.PromotionID]intent.Promotion),
		prepared:    make(map[intent.PromotionID]intent.PreparedPromotion),
		completed:   make(map[intent.VersionID]intent.PromotionID),
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
			state.versions[record.Version.ID] = *record.Version
			state.versionIDs[record.Change.ID] = append(state.versionIDs[record.Change.ID], record.Version.ID)
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
			state.current = prepared.Intent
			delete(state.prepared, prepared.Promotion.ID)
			state.pending = ""
		}
	case promotionRecorded:
		if _, exists := state.promotions[record.Promotion.ID]; !exists {
			state.revisions[record.NextIntent.ID] = *record.NextIntent
			state.promotions[record.Promotion.ID] = *record.Promotion
			state.completed[record.Promotion.VersionID] = record.Promotion.ID
			state.current = *record.NextIntent
		}
	}
}
