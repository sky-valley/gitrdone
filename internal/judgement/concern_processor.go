package judgement

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/reviewidentity"
)

type Concern struct {
	Name     string
	Prompt   string
	Reviewer string
}

type ConcernRequest struct {
	RepoID          string
	Version         intent.Version
	GoverningIntent intent.Revision
	Concern         Concern
}

type ConcernResult struct {
	RequiresReview bool
	Reason         string
	Evidence       []string
}

type ConcernEvaluator interface {
	Evaluate(ctx context.Context, request ConcernRequest) (ConcernResult, error)
}

type ConcernService interface {
	ConcernAssessment(ctx context.Context, repoID string, versionID intent.VersionID) (intent.ConcernAssessment, bool, error)
	ConcernAssessmentContext(ctx context.Context, repoID string, versionID intent.VersionID) (intent.ConcernAssessmentContext, error)
	RecordConcernAssessment(ctx context.Context, repoID string, assessment intent.ConcernAssessment) (intent.ConcernAssessment, error)
	Promote(ctx context.Context, repoID string, request intent.PromoteRequest) (intent.Promoted, error)
}

type ConcernProcessor struct {
	service   ConcernService
	evaluator ConcernEvaluator
	concerns  []Concern
}

func NewConcernProcessor(service ConcernService, evaluator ConcernEvaluator, concerns []Concern) (*ConcernProcessor, error) {
	if service == nil || evaluator == nil {
		return nil, errors.New("assessment processor requires service and evaluator")
	}
	if len(concerns) == 0 {
		return nil, errors.New("assessment processor requires at least one concern")
	}
	normalized := make([]Concern, len(concerns))
	names := make(map[string]struct{}, len(concerns))
	for index, concern := range concerns {
		concern.Name = strings.TrimSpace(concern.Name)
		concern.Prompt = strings.TrimSpace(concern.Prompt)
		reviewer, validReviewer := reviewidentity.Canonical(concern.Reviewer)
		concern.Reviewer = reviewer
		if concern.Name == "" || concern.Prompt == "" || concern.Reviewer == "" {
			return nil, errors.New("assessment concern requires name, one-line prompt, and reviewer")
		}
		if !validReviewer {
			return nil, errors.New("assessment concern reviewer must be a canonical email subject")
		}
		if strings.ContainsAny(concern.Prompt, "\r\n") {
			return nil, errors.New("assessment concern prompt must be one line")
		}
		if _, duplicate := names[concern.Name]; duplicate {
			return nil, errors.New("assessment concern names must be unique")
		}
		names[concern.Name] = struct{}{}
		normalized[index] = concern
	}
	return &ConcernProcessor{service: service, evaluator: evaluator, concerns: normalized}, nil
}

func (processor *ConcernProcessor) Process(ctx context.Context, item WorkItem) error {
	recorded, found, err := processor.service.ConcernAssessment(ctx, item.RepoID, item.VersionID)
	if err != nil {
		return fmt.Errorf("read existing assessment: %w", err)
	}
	if !found {
		recorded, err = processor.evaluate(ctx, item)
		if err != nil {
			return err
		}
	}
	if len(recorded.ReviewObligations()) > 0 {
		return nil
	}
	_, err = processor.service.Promote(ctx, item.RepoID, intent.PromoteRequest{
		VersionID:      recorded.VersionID,
		ExpectedIntent: recorded.GoverningIntent,
	})
	if errors.Is(err, intent.ErrIntentAdvanced) || errors.Is(err, intent.ErrPromotionPending) || errors.Is(err, intent.ErrDependenciesPending) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("promote cleared Version: %w", err)
	}
	return nil
}

func (processor *ConcernProcessor) evaluate(ctx context.Context, item WorkItem) (intent.ConcernAssessment, error) {
	assessmentContext, err := processor.service.ConcernAssessmentContext(ctx, item.RepoID, item.VersionID)
	if err != nil {
		return intent.ConcernAssessment{}, fmt.Errorf("read assessment context: %w", err)
	}
	evaluations := make([]intent.ConcernEvaluation, 0, len(processor.concerns))
	for _, concern := range processor.concerns {
		version := assessmentContext.Version
		version.Dependencies = slices.Clone(version.Dependencies)
		assessment, err := processor.evaluator.Evaluate(ctx, ConcernRequest{
			RepoID:          item.RepoID,
			Version:         version,
			GoverningIntent: assessmentContext.GoverningIntent,
			Concern:         concern,
		})
		if err != nil {
			return intent.ConcernAssessment{}, fmt.Errorf("evaluate concern %q: %w", concern.Name, err)
		}
		if err := validateAssessment(assessment); err != nil {
			return intent.ConcernAssessment{}, fmt.Errorf("evaluate concern %q: %w", concern.Name, err)
		}
		evaluations = append(evaluations, intent.ConcernEvaluation{
			Concern:        concern.Name,
			Prompt:         concern.Prompt,
			Reviewer:       concern.Reviewer,
			RequiresReview: assessment.RequiresReview,
			Reason:         assessment.Reason,
			Evidence:       slices.Clone(assessment.Evidence),
		})
	}
	result := intent.ConcernAssessment{
		VersionID:       assessmentContext.Version.ID,
		GoverningIntent: assessmentContext.GoverningIntent.ID,
		Evaluations:     evaluations,
	}
	recorded, err := processor.service.RecordConcernAssessment(ctx, item.RepoID, result)
	if errors.Is(err, intent.ErrConcernAssessmentAlreadyRecorded) {
		existing, found, readErr := processor.service.ConcernAssessment(ctx, item.RepoID, item.VersionID)
		if readErr != nil {
			return intent.ConcernAssessment{}, fmt.Errorf("read concurrently recorded assessment: %w", readErr)
		}
		if !found {
			return intent.ConcernAssessment{}, fmt.Errorf("record assessment: %w", err)
		}
		return existing, nil
	}
	if err != nil {
		return intent.ConcernAssessment{}, fmt.Errorf("record assessment: %w", err)
	}
	return recorded, nil
}

func validateAssessment(assessment ConcernResult) error {
	if strings.TrimSpace(assessment.Reason) == "" || len(assessment.Evidence) == 0 {
		return errors.New("assessment requires reason and evidence")
	}
	for _, evidence := range assessment.Evidence {
		if strings.TrimSpace(evidence) == "" {
			return errors.New("assessment evidence must not be empty")
		}
	}
	return nil
}
