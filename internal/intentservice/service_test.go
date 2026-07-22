package intentservice_test

import (
	"context"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestApproveAllServiceAdmitsBeforeAttemptingPromotion(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewRepository(initialContent, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})
	initial := repository.CurrentIntent()

	admission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "control-api",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if admission.Proposed.Change.ID == "" || admission.Proposed.Version.Producer != "control-api" {
		t.Fatalf("admitted proposal = %#v", admission.Proposed)
	}
	if admission.Promotion == nil || admission.Promotion.Promotion.VersionID != admission.Proposed.Version.ID {
		t.Fatalf("promotion = %#v, want admitted version", admission.Promotion)
	}

	staleAdmission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-stale",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "control-api",
	})
	if err != nil {
		t.Fatalf("propose stale change: %v", err)
	}
	if staleAdmission.Proposed.Change.ID == "" || staleAdmission.Promotion != nil {
		t.Fatalf("stale admission = %#v, want admitted without promotion", staleAdmission)
	}
}

type staticResolver struct {
	repository *intent.Repository
}

func (resolver staticResolver) Resolve(context.Context, string) (intentservice.Repository, error) {
	return resolver.repository, nil
}

func (resolver staticResolver) Bootstrap(_ context.Context, _ string, content intent.ContentRef) (intent.Revision, error) {
	current := resolver.repository.CurrentIntent()
	if current.Content != content {
		return intent.Revision{}, intentservice.ErrRepositoryAlreadyInitialized
	}
	return current, nil
}

type acceptingAdmission struct{}

func (acceptingAdmission) Admit(context.Context, intent.VersionID, intent.ContentRef) error {
	return nil
}

type recordingProjection struct {
	current intent.ContentRef
}

func (projection *recordingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *recordingProjection) Advance(_ context.Context, _ intent.ContentRef, next intent.ContentRef) error {
	projection.current = next
	return nil
}
