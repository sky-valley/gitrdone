package intent_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
)

func TestRepositoryRefusesToAmendAPromotedVersion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, statelessAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: proposed.Version.BaseIntent,
	}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	_, err = repository.Amend(ctx, amendmentRequest(proposed))
	if !errors.Is(err, intent.ErrVersionPromotionStarted) {
		t.Fatalf("amend promoted version error = %v, want ErrVersionPromotionStarted", err)
	}
}

func TestRepositoryRefusesToPromoteASupersededVersion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, statelessAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := repository.Amend(ctx, amendmentRequest(proposed)); err != nil {
		t.Fatalf("amend: %v", err)
	}

	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: proposed.Version.BaseIntent,
	})
	if !errors.Is(err, intent.ErrVersionAdvanced) {
		t.Fatalf("promote superseded version error = %v, want ErrVersionAdvanced", err)
	}
}

func TestRepositorySerializesAmendmentAgainstPromotion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, statelessAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		_, err := repository.Amend(ctx, amendmentRequest(proposed))
		results <- err
	}()
	go func() {
		defer group.Done()
		<-start
		_, err := repository.Promote(ctx, intent.PromoteRequest{
			VersionID:      proposed.Version.ID,
			ExpectedIntent: proposed.Version.BaseIntent,
		})
		results <- err
	}()
	close(start)
	group.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result == nil {
			successes++
			continue
		}
		if !errors.Is(result, intent.ErrVersionAdvanced) && !errors.Is(result, intent.ErrVersionPromotionStarted) {
			t.Fatalf("losing operation error = %v, want a terminal version-state error", result)
		}
	}
	if successes != 1 {
		t.Fatalf("successful competing operations = %d, want exactly 1", successes)
	}
}

func TestRepositoryDoesNotReuseAmendmentIdempotencyForAProposal(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, statelessAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	proposalKeyAmendment := amendmentRequest(proposed)
	proposalKeyAmendment.IdempotencyKey = "proposal-b"
	if _, err := repository.Amend(ctx, proposalKeyAmendment); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("amendment using proposal idempotency key error = %v, want ErrIdempotencyConflict", err)
	}
	amended, err := repository.Amend(ctx, amendmentRequest(proposed))
	if err != nil {
		t.Fatalf("amend: %v", err)
	}

	_, err = repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: amendedRequestKey,
		BaseIntent:     amended.Version.BaseIntent,
		Content:        amended.Version.Content,
		Producer:       amended.Version.Producer,
		Dependencies:   amended.Version.Dependencies,
	})
	if !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("proposal using amendment idempotency key error = %v, want ErrIdempotencyConflict", err)
	}
}

const amendedRequestKey = "amend-b"

func amendmentRequest(proposed intent.Proposed) intent.AmendRequest {
	return intent.AmendRequest{
		IdempotencyKey:  amendedRequestKey,
		ChangeID:        proposed.Change.ID,
		ExpectedVersion: proposed.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair the proposed change",
	}
}

type statelessAdmission struct{}

func (statelessAdmission) Admit(context.Context, intent.VersionID, intent.ContentRef) error {
	return nil
}

func TestRepositoryAmendmentAppendsAnImmutableVersionToTheSameChange(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
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
		t.Fatalf("propose original: %v", err)
	}
	request := intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository",
		Rationale:       "timeout path could duplicate the operation",
	}
	amended, err := repository.Amend(ctx, request)
	if err != nil {
		t.Fatalf("amend original: %v", err)
	}
	if amended.Change != original.Change {
		t.Fatalf("amended change = %#v, want original identity %#v", amended.Change, original.Change)
	}
	if amended.Version.ID == "" || amended.Version.ID == original.Version.ID {
		t.Fatalf("amended version id = %q, want new immutable version", amended.Version.ID)
	}
	if amended.Version.ChangeID != original.Change.ID || amended.Version.BaseIntent != original.Version.BaseIntent {
		t.Fatalf("amended version = %#v, want same change and base intent", amended.Version)
	}
	if amended.Amendment.FromVersion != original.Version.ID || amended.Amendment.ToVersion != amended.Version.ID {
		t.Fatalf("amendment = %#v, want %q -> %q", amended.Amendment, original.Version.ID, amended.Version.ID)
	}
	if amended.Amendment.Rationale != request.Rationale {
		t.Fatalf("amendment rationale = %q, want %q", amended.Amendment.Rationale, request.Rationale)
	}

	page, err := repository.Versions(ctx, intent.VersionQuery{ChangeID: original.Change.ID, Limit: 10})
	if err != nil {
		t.Fatalf("read versions: %v", err)
	}
	if len(page.Versions) != 2 || !reflect.DeepEqual(page.Versions[0], original.Version) || !reflect.DeepEqual(page.Versions[1], amended.Version) {
		t.Fatalf("versions = %#v, want original then amendment", page.Versions)
	}
	retried, err := repository.Amend(ctx, request)
	if err != nil {
		t.Fatalf("retry amendment: %v", err)
	}
	if !reflect.DeepEqual(retried, amended) {
		t.Fatalf("retried amendment = %#v, want original result %#v", retried, amended)
	}

	conflicting := request
	conflicting.Rationale = "different amendment"
	if _, err := repository.Amend(ctx, conflicting); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("conflicting amendment error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestRepositoryAmendmentRequiresTheExpectedLatestVersion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
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
		t.Fatalf("propose original: %v", err)
	}
	first, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b-1",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository",
		Rationale:       "first repair",
	})
	if err != nil {
		t.Fatalf("first amendment: %v", err)
	}
	_, err = repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b-stale",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b3b3b3b3"},
		Producer:        "repository",
		Rationale:       "stale repair",
	})
	if !errors.Is(err, intent.ErrVersionAdvanced) {
		t.Fatalf("stale amendment error = %v, want ErrVersionAdvanced after %q", err, first.Version.ID)
	}
}

func TestRepositoryAmendmentRefusesToStrandAdmittedDependents(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
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
		t.Fatalf("propose original: %v", err)
	}
	if _, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
		Dependencies:   []intent.VersionID{original.Version.ID},
	}); err != nil {
		t.Fatalf("propose dependent: %v", err)
	}

	_, err = repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository",
		Rationale:       "repair B",
	})
	if !errors.Is(err, intent.ErrDependentVersionsNeedReconciliation) {
		t.Fatalf("amendment error = %v, want ErrDependentVersionsNeedReconciliation", err)
	}
	page, err := repository.Versions(ctx, intent.VersionQuery{ChangeID: original.Change.ID, Limit: 10})
	if err != nil {
		t.Fatalf("read original versions: %v", err)
	}
	if len(page.Versions) != 1 || page.Versions[0].ID != original.Version.ID {
		t.Fatalf("original versions = %#v, want no stranded amendment", page.Versions)
	}
}
