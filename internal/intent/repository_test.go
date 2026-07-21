package intent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
)

func TestRepositoryProposeThenPromoteAgainstCurrentIntent(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	proposedContent := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	admission := &recordingAdmission{}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewRepository(initialContent, admission, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()

	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        proposedContent,
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	if proposed.Change.ID == "" {
		t.Fatal("change id is empty")
	}
	if proposed.Version.ID == "" {
		t.Fatal("version id is empty")
	}
	if proposed.Version.ChangeID != proposed.Change.ID {
		t.Fatalf("version change id = %q, want %q", proposed.Version.ChangeID, proposed.Change.ID)
	}
	if proposed.Version.BaseIntent != initialIntent.ID {
		t.Fatalf("version base intent = %q, want %q", proposed.Version.BaseIntent, initialIntent.ID)
	}
	if proposed.Version.Content != proposedContent {
		t.Fatalf("version content = %#v, want %#v", proposed.Version.Content, proposedContent)
	}
	if proposed.Version.Producer != "ion" {
		t.Fatalf("version producer = %q, want ion", proposed.Version.Producer)
	}
	if got := repository.CurrentIntent(); got != initialIntent {
		t.Fatalf("current intent after propose = %#v, want %#v", got, initialIntent)
	}
	if len(projection.advances) != 0 {
		t.Fatalf("projection advances after propose = %d, want 0", len(projection.advances))
	}
	if len(admission.admissions) != 1 {
		t.Fatalf("content admissions = %d, want 1", len(admission.admissions))
	}
	if admission.admissions[0].versionID != proposed.Version.ID || admission.admissions[0].content != proposedContent {
		t.Fatalf("content admission = %#v, want version %q content %#v", admission.admissions[0], proposed.Version.ID, proposedContent)
	}

	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	currentIntent := repository.CurrentIntent()
	if currentIntent.ID == "" || currentIntent.ID == initialIntent.ID {
		t.Fatalf("current intent id = %q, want a new non-empty id", currentIntent.ID)
	}
	if currentIntent.PreviousID != initialIntent.ID {
		t.Fatalf("current intent previous id = %q, want %q", currentIntent.PreviousID, initialIntent.ID)
	}
	if currentIntent.Content != proposedContent {
		t.Fatalf("current intent content = %#v, want %#v", currentIntent.Content, proposedContent)
	}
	if promoted.Intent != currentIntent {
		t.Fatalf("promoted intent = %#v, want current intent %#v", promoted.Intent, currentIntent)
	}

	if promoted.Promotion.ID == "" {
		t.Fatal("promotion id is empty")
	}
	if promoted.Promotion.FromIntent != initialIntent.ID {
		t.Fatalf("promotion from intent = %q, want %q", promoted.Promotion.FromIntent, initialIntent.ID)
	}
	if promoted.Promotion.ToIntent != currentIntent.ID {
		t.Fatalf("promotion to intent = %q, want %q", promoted.Promotion.ToIntent, currentIntent.ID)
	}
	if promoted.Promotion.VersionID != proposed.Version.ID {
		t.Fatalf("promotion version id = %q, want %q", promoted.Promotion.VersionID, proposed.Version.ID)
	}

	if len(projection.advances) != 1 {
		t.Fatalf("projection advances = %d, want 1", len(projection.advances))
	}
	if projection.advances[0].expected != initialContent {
		t.Fatalf("projection expected content = %#v, want %#v", projection.advances[0].expected, initialContent)
	}
	if projection.advances[0].next != proposedContent {
		t.Fatalf("projection next content = %#v, want %#v", projection.advances[0].next, proposedContent)
	}
}

func TestRepositoryHoldsStaleProposalInsteadOfAdvancingIntent(t *testing.T) {
	ctx := context.Background()
	admission := &recordingAdmission{}
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewRepository(initialContent, admission, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	staleIntent := repository.CurrentIntent()

	first, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     staleIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("first propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      first.Version.ID,
		ExpectedIntent: staleIntent.ID,
	})
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}
	currentIntent := repository.CurrentIntent()

	stale, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-2",
		BaseIntent:     staleIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("stale propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      stale.Version.ID,
		ExpectedIntent: staleIntent.ID,
	})
	if !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("stale promote error = %v, want ErrIntentAdvanced", err)
	}
	if got := repository.CurrentIntent(); got != currentIntent {
		t.Fatalf("current intent after stale promote = %#v, want %#v", got, currentIntent)
	}
	if len(projection.advances) != 1 {
		t.Fatalf("projection advances = %d, want 1", len(projection.advances))
	}
}

func TestRepositoryKeepsProposalWhenProjectionFails(t *testing.T) {
	ctx := context.Background()
	projectionErr := errors.New("projection unavailable")
	admission := &recordingAdmission{}
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent, err: projectionErr}
	repository, err := intent.NewRepository(initialContent, admission, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()

	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("promote error = %v, want projection error", err)
	}
	if got := repository.CurrentIntent(); got != initialIntent {
		t.Fatalf("current intent after projection failure = %#v, want %#v", got, initialIntent)
	}

	projection.err = nil
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	}); err != nil {
		t.Fatalf("retry promote: %v", err)
	}
}

func TestRepositoryDoesNotRecordContentWhenAdmissionFails(t *testing.T) {
	ctx := context.Background()
	admissionErr := errors.New("content missing")
	admission := &recordingAdmission{err: admissionErr}
	repository, err := intent.NewRepository(
		intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"},
		admission,
		&recordingProjection{},
	)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()

	_, err = repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if !errors.Is(err, admissionErr) {
		t.Fatalf("propose error = %v, want admission error", err)
	}
	if len(admission.admissions) != 1 {
		t.Fatalf("content admissions = %d, want 1", len(admission.admissions))
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      admission.admissions[0].versionID,
		ExpectedIntent: initialIntent.ID,
	})
	if !errors.Is(err, intent.ErrVersionNotFound) {
		t.Fatalf("promote unrecorded version error = %v, want ErrVersionNotFound", err)
	}
}

type recordingAdmission struct {
	admissions []contentAdmission
	err        error
}

type contentAdmission struct {
	versionID intent.VersionID
	content   intent.ContentRef
}

func (admission *recordingAdmission) Admit(_ context.Context, versionID intent.VersionID, content intent.ContentRef) error {
	admission.admissions = append(admission.admissions, contentAdmission{versionID: versionID, content: content})
	return admission.err
}

type recordingProjection struct {
	current  intent.ContentRef
	advances []projectionAdvance
	err      error
}

type projectionAdvance struct {
	expected intent.ContentRef
	next     intent.ContentRef
}

func (projection *recordingProjection) Advance(_ context.Context, expected, next intent.ContentRef) error {
	projection.advances = append(projection.advances, projectionAdvance{expected: expected, next: next})
	if projection.err == nil {
		projection.current = next
	}
	return projection.err
}

func (projection *recordingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}
