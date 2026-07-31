package judgement

import (
	"context"
	"errors"
	"fmt"

	"github.com/sky-valley/gitrdone/internal/intent"
)

type PromotionService interface {
	CurrentIntent(ctx context.Context, repoID string) (intent.Revision, error)
	Promote(ctx context.Context, repoID string, request intent.PromoteRequest) (intent.Promoted, error)
}

// ApproveAllProcessor is temporary execution plumbing for the no-op judgement
// policy. It is not the eventual judgement or workflow model.
type ApproveAllProcessor struct {
	service PromotionService
}

func NewApproveAllProcessor(service PromotionService) *ApproveAllProcessor {
	return &ApproveAllProcessor{service: service}
}

func (processor *ApproveAllProcessor) Process(ctx context.Context, item WorkItem) error {
	current, err := processor.service.CurrentIntent(ctx, item.RepoID)
	if err != nil {
		return fmt.Errorf("read current intent: %w", err)
	}
	_, err = processor.service.Promote(ctx, item.RepoID, intent.PromoteRequest{
		VersionID:      item.VersionID,
		ExpectedIntent: current.ID,
	})
	if errors.Is(err, intent.ErrIntentAdvanced) || errors.Is(err, intent.ErrPromotionPending) || errors.Is(err, intent.ErrDependenciesPending) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("promote pending Version: %w", err)
	}
	return nil
}
