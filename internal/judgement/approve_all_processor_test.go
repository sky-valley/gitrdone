package judgement_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/judgement"
)

func TestApproveAllProcessorRequestsPromotionAgainstCurrentIntent(t *testing.T) {
	service := &recordingPromotionService{
		current: intent.Revision{ID: "intent_a"},
	}
	processor := judgement.NewApproveAllProcessor(service)
	item := judgement.WorkItem{RepoID: "repo_a", VersionID: "version_b"}

	if err := processor.Process(context.Background(), item); err != nil {
		t.Fatalf("process: %v", err)
	}
	if service.repoID != item.RepoID || service.request.VersionID != item.VersionID || service.request.ExpectedIntent != service.current.ID {
		t.Fatalf("promotion request = %q, %#v; want current intent and pending Version", service.repoID, service.request)
	}
}

func TestApproveAllProcessorLeavesTemporarilyUnpromotableVersionsPending(t *testing.T) {
	for _, promoteErr := range []error{intent.ErrIntentAdvanced, intent.ErrPromotionPending, intent.ErrDependenciesPending} {
		t.Run(promoteErr.Error(), func(t *testing.T) {
			service := &recordingPromotionService{
				current:    intent.Revision{ID: "intent_a"},
				promoteErr: promoteErr,
			}
			processor := judgement.NewApproveAllProcessor(service)
			if err := processor.Process(context.Background(), judgement.WorkItem{RepoID: "repo_a", VersionID: "version_b"}); err != nil {
				t.Fatalf("processor returned expected wait condition: %v", err)
			}
		})
	}
}

func TestApproveAllProcessorReturnsUnexpectedFailures(t *testing.T) {
	want := errors.New("projection unavailable")
	service := &recordingPromotionService{
		current:    intent.Revision{ID: "intent_a"},
		promoteErr: want,
	}
	processor := judgement.NewApproveAllProcessor(service)
	if err := processor.Process(context.Background(), judgement.WorkItem{RepoID: "repo_a", VersionID: "version_b"}); !errors.Is(err, want) {
		t.Fatalf("processor error = %v, want %v", err, want)
	}
}

type recordingPromotionService struct {
	current    intent.Revision
	repoID     string
	request    intent.PromoteRequest
	promoteErr error
}

func (service *recordingPromotionService) CurrentIntent(context.Context, string) (intent.Revision, error) {
	return service.current, nil
}

func (service *recordingPromotionService) Promote(_ context.Context, repoID string, request intent.PromoteRequest) (intent.Promoted, error) {
	service.repoID = repoID
	service.request = request
	return intent.Promoted{}, service.promoteErr
}
