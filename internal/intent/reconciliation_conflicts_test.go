package intent_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
)

func TestRepositoryRecordsReconciliationConflictAsDurableWork(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	admission := &recordingAdmission{}
	repository, err := intent.NewRepository(initialContent, admission, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initial := repository.CurrentIntent()
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose B: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend B: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: initial.ID,
	}); err != nil {
		t.Fatalf("promote B prime: %v", err)
	}
	descendant, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose C: %v", err)
	}
	admissionsBeforeConflict := len(admission.admissions)

	request := intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-b-c",
		FromVersion:       original.Version.ID,
		ToVersion:         amended.Version.ID,
		DescendantVersion: descendant.Version.ID,
		ReportedBy:        "ion",
		AffectedPaths:     []string{"feature.txt", "feature.txt", " config.yaml "},
	}
	wrongOperation := request
	wrongOperation.IdempotencyKey = "proposal-b"
	if _, err := repository.RecordReconciliationConflict(ctx, wrongOperation); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("proposal key reused for conflict error = %v, want ErrIdempotencyConflict", err)
	}
	recorded, err := repository.RecordReconciliationConflict(ctx, request)
	if err != nil {
		t.Fatalf("record reconciliation conflict: %v", err)
	}
	if recorded.ID == "" {
		t.Fatalf("recorded identity = %#v, want conflict id", recorded)
	}
	if !reflect.DeepEqual(recorded.Change, descendant.Change) || !reflect.DeepEqual(recorded.Version, descendant.Version) {
		t.Fatalf("recorded descendant = %#v / %#v, want existing C %#v", recorded.Change, recorded.Version, descendant)
	}
	if len(recorded.Version.Dependencies) != 0 {
		t.Fatalf("descendant dependencies = %q, want conflict lineage kept out of promotion dependencies", recorded.Version.Dependencies)
	}
	if recorded.FromVersion != original.Version.ID || recorded.ToVersion != amended.Version.ID {
		t.Fatalf("conflict lineage = %q -> %q, want %q -> %q", recorded.FromVersion, recorded.ToVersion, original.Version.ID, amended.Version.ID)
	}
	if recorded.ReportedBy != "ion" {
		t.Fatalf("conflict reporter = %q, want authenticated ion", recorded.ReportedBy)
	}
	if !slices.Equal(recorded.AffectedPaths, []string{" config.yaml ", "feature.txt"}) {
		t.Fatalf("affected paths = %q, want exact sorted bounded evidence", recorded.AffectedPaths)
	}
	if len(admission.admissions) != admissionsBeforeConflict {
		t.Fatalf("conflict recording admitted content again: admissions %d -> %d", admissionsBeforeConflict, len(admission.admissions))
	}

	loaded, found, err := repository.ReconciliationConflict(ctx, recorded.ID)
	if err != nil || !found {
		t.Fatalf("read reconciliation conflict = %#v, %t, %v; want recorded conflict", loaded, found, err)
	}
	if !reflect.DeepEqual(loaded, recorded) {
		t.Fatalf("loaded conflict = %#v, want %#v", loaded, recorded)
	}
	loaded.AffectedPaths[0] = "corrupted-by-caller"
	reloaded, _, err := repository.ReconciliationConflict(ctx, recorded.ID)
	if err != nil {
		t.Fatalf("reload reconciliation conflict: %v", err)
	}
	if !slices.Equal(reloaded.AffectedPaths, []string{" config.yaml ", "feature.txt"}) {
		t.Fatalf("stored paths changed through returned value: %q", reloaded.AffectedPaths)
	}

	retried, err := repository.RecordReconciliationConflict(ctx, request)
	if err != nil {
		t.Fatalf("retry reconciliation conflict: %v", err)
	}
	if !reflect.DeepEqual(retried, recorded) {
		t.Fatalf("retried conflict = %#v, want %#v", retried, recorded)
	}
	retriedWithDifferentDiagnostics := request
	retriedWithDifferentDiagnostics.AffectedPaths = []string{"different-adapter-observation.txt"}
	retried, err = repository.RecordReconciliationConflict(ctx, retriedWithDifferentDiagnostics)
	if err != nil {
		t.Fatalf("retry reconciliation conflict with different diagnostics: %v", err)
	}
	if !reflect.DeepEqual(retried, recorded) {
		t.Fatalf("retry with different diagnostics = %#v, want original durable record %#v", retried, recorded)
	}
	conflicting := request
	conflicting.DescendantVersion = original.Version.ID
	if _, err := repository.RecordReconciliationConflict(ctx, conflicting); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("conflicting retry error = %v, want ErrIdempotencyConflict", err)
	}
	if _, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: request.IdempotencyKey,
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "eeeeeeee"},
		Producer:       "ion",
	}); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("conflict key reused for proposal error = %v, want ErrIdempotencyConflict", err)
	}
	next, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "noam",
	})
	if err != nil {
		t.Fatalf("propose D: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      next.Version.ID,
		ExpectedIntent: next.Version.BaseIntent,
	}); err != nil {
		t.Fatalf("promote D: %v", err)
	}
	stale := request
	stale.IdempotencyKey = "conflict-after-intent-advanced"
	if _, err := repository.RecordReconciliationConflict(ctx, stale); !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("stale reconciliation conflict error = %v, want ErrIntentAdvanced", err)
	}
}

func TestRepositoryReconciliationConflictRequiresAcceptedAmendmentLineage(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, statelessAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose B: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend B: %v", err)
	}
	descendant, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose C: %v", err)
	}

	_, err = repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-before-acceptance",
		FromVersion:       original.Version.ID,
		ToVersion:         amended.Version.ID,
		DescendantVersion: descendant.Version.ID,
		ReportedBy:        "ion",
	})
	if !errors.Is(err, intent.ErrVersionNotPromoted) {
		t.Fatalf("unaccepted amendment conflict error = %v, want ErrVersionNotPromoted", err)
	}
}

func TestRepositoryListsReconciliationConflictsInDurableRecordingOrder(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, statelessAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initial := repository.CurrentIntent()
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose B: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend B: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: initial.ID,
	}); err != nil {
		t.Fatalf("promote B prime: %v", err)
	}
	empty, err := repository.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{Limit: 1})
	if err != nil {
		t.Fatalf("list empty conflict history: %v", err)
	}
	if len(empty.Conflicts) != 0 || empty.NextCursor != "" {
		t.Fatalf("empty conflict history = %#v, want empty page", empty)
	}
	if _, err := repository.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{}); err == nil {
		t.Fatal("listed conflicts with an invalid zero page limit")
	}

	recorded := make([]intent.ReconciliationConflict, 0, 2)
	for index, revision := range []string{"cccccccc", "dddddddd"} {
		descendant, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: "proposal-descendant-" + revision,
			BaseIntent:     initial.ID,
			Content:        intent.ContentRef{Engine: "git", Revision: revision},
			Producer:       "ion",
		})
		if err != nil {
			t.Fatalf("propose descendant %d: %v", index, err)
		}
		conflict, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
			IdempotencyKey:    "conflict-" + revision,
			FromVersion:       original.Version.ID,
			ToVersion:         amended.Version.ID,
			DescendantVersion: descendant.Version.ID,
			ReportedBy:        "ion",
			AffectedPaths:     []string{revision + ".txt"},
		})
		if err != nil {
			t.Fatalf("record conflict %d: %v", index, err)
		}
		recorded = append(recorded, conflict)
	}
	if _, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-cccccccc",
		FromVersion:       original.Version.ID,
		ToVersion:         amended.Version.ID,
		DescendantVersion: recorded[0].Version.ID,
		ReportedBy:        "ion",
	}); err != nil {
		t.Fatalf("retry first conflict: %v", err)
	}

	first, err := repository.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{Limit: 1})
	if err != nil {
		t.Fatalf("list first conflict page: %v", err)
	}
	if len(first.Conflicts) != 1 || !reflect.DeepEqual(first.Conflicts[0], recorded[0]) || first.NextCursor != recorded[0].ID {
		t.Fatalf("first page = %#v, want first conflict and cursor %q", first, recorded[0].ID)
	}
	first.Conflicts[0].AffectedPaths[0] = "corrupted-by-caller"

	second, err := repository.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{
		After: first.NextCursor,
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("list second conflict page: %v", err)
	}
	if len(second.Conflicts) != 1 || !reflect.DeepEqual(second.Conflicts[0], recorded[1]) || second.NextCursor != "" {
		t.Fatalf("second page = %#v, want second conflict and no cursor", second)
	}
	reloaded, err := repository.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{Limit: 2})
	if err != nil {
		t.Fatalf("reload conflict history: %v", err)
	}
	if !reflect.DeepEqual(reloaded.Conflicts, recorded) {
		t.Fatalf("reloaded conflicts = %#v, want defensive copies %#v", reloaded.Conflicts, recorded)
	}
	if _, err := repository.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{
		After: "conflict_unknown",
		Limit: 1,
	}); !errors.Is(err, intent.ErrReconciliationConflictNotFound) {
		t.Fatalf("invalid cursor error = %v, want ErrReconciliationConflictNotFound", err)
	}
}

func TestReconciliationConflictPathsPreserveGitNamesWithinOneBoundedContract(t *testing.T) {
	got, err := intent.NormalizeReconciliationConflictPaths([]string{" trailing ", "leading ", " trailing "})
	if err != nil {
		t.Fatalf("normalize exact Git paths: %v", err)
	}
	if !slices.Equal(got, []string{" trailing ", "leading "}) {
		t.Fatalf("normalized paths = %q, want exact sorted names", got)
	}
	if got, err := intent.NormalizeReconciliationConflictPaths(nil); err != nil || len(got) != 0 {
		t.Fatalf("optional paths = %q, %v; want empty diagnostics", got, err)
	}
	for name, paths := range map[string][]string{
		"empty":     {""},
		"nul":       {"bad\x00path"},
		"one huge":  {strings.Repeat("x", 4097)},
		"aggregate": makeDistinctLargePaths(13),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := intent.NormalizeReconciliationConflictPaths(paths); !errors.Is(err, intent.ErrInvalidReconciliationConflict) {
				t.Fatalf("normalize error = %v, want ErrInvalidReconciliationConflict", err)
			}
		})
	}
}

func makeDistinctLargePaths(count int) []string {
	paths := make([]string, count)
	for index := range paths {
		paths[index] = string(rune('a'+index)) + strings.Repeat("x", 4095)
	}
	return paths
}
