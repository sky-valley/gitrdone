package judgement

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
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
	Purpose         string
	Priorities      string
	ChangeEvidence  string
}

type ConcernResult struct {
	RequiresReview bool
	Reason         string
	Evidence       []string
	Provenance     intent.EvaluatorProvenance
}

type ConcernEvaluator interface {
	Evaluate(ctx context.Context, request ConcernRequest) (ConcernResult, error)
}

type ConcernService interface {
	ConcernAssessment(ctx context.Context, repoID string, versionID intent.VersionID) (intent.ConcernAssessment, bool, error)
	ConcernAssessmentContext(ctx context.Context, repoID string, versionID intent.VersionID) (intent.ConcernAssessmentContext, error)
	RecordConcernAssessment(ctx context.Context, repoID string, assessment intent.ConcernAssessment) (intent.ConcernAssessment, error)
	UnresolvedReviewObligations(ctx context.Context, repoID string, versionID intent.VersionID) ([]intent.ReviewObligation, error)
	Promote(ctx context.Context, repoID string, request intent.PromoteRequest) (intent.Promoted, error)
}

type ConcernProcessor struct {
	service   ConcernService
	evaluator ConcernEvaluator
	inputs    AssessmentInputSource
}

func NewConcernProcessor(service ConcernService, evaluator ConcernEvaluator, inputs AssessmentInputSource) (*ConcernProcessor, error) {
	if service == nil || evaluator == nil || inputs == nil {
		return nil, errors.New("assessment processor requires service, evaluator, and repository inputs")
	}
	return &ConcernProcessor{service: service, evaluator: evaluator, inputs: inputs}, nil
}

func normalizeConcerns(concerns []Concern) ([]Concern, error) {
	if len(concerns) == 0 {
		return nil, errors.New("assessment inputs require at least one concern")
	}
	normalized := make([]Concern, len(concerns))
	names := make(map[string]struct{}, len(concerns))
	for index, concern := range concerns {
		var err error
		concern, err = normalizeConcern(concern)
		if err != nil {
			return nil, err
		}
		if _, duplicate := names[concern.Name]; duplicate {
			return nil, errors.New("assessment concern names must be unique")
		}
		names[concern.Name] = struct{}{}
		normalized[index] = concern
	}
	return normalized, nil
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
	obligations, err := processor.service.UnresolvedReviewObligations(ctx, item.RepoID, recorded.VersionID)
	if err != nil {
		return fmt.Errorf("read unresolved review obligations: %w", err)
	}
	if len(obligations) > 0 {
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
	inputs, err := processor.inputs.Load(ctx, item.RepoID, assessmentContext)
	if err != nil {
		return intent.ConcernAssessment{}, fmt.Errorf("load repository assessment inputs: %w", err)
	}
	if strings.TrimSpace(inputs.Purpose) == "" || strings.TrimSpace(inputs.Priorities) == "" || strings.TrimSpace(inputs.ChangeEvidence) == "" {
		return intent.ConcernAssessment{}, errors.New("repository assessment inputs require purpose, priorities, and change evidence")
	}
	concerns, err := normalizeConcerns(inputs.Concerns)
	if err != nil {
		return intent.ConcernAssessment{}, fmt.Errorf("load repository assessment inputs: %w", err)
	}
	evaluations := make([]intent.ConcernEvaluation, 0, len(concerns))
	for _, concern := range concerns {
		version := assessmentContext.Version
		version.Dependencies = slices.Clone(version.Dependencies)
		assessment, err := processor.evaluator.Evaluate(ctx, ConcernRequest{
			RepoID:          item.RepoID,
			Version:         version,
			GoverningIntent: assessmentContext.GoverningIntent,
			Concern:         concern,
			Purpose:         inputs.Purpose,
			Priorities:      inputs.Priorities,
			ChangeEvidence:  inputs.ChangeEvidence,
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
			Provenance:     assessment.Provenance,
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
	if strings.TrimSpace(assessment.Reason) == "" || len(assessment.Evidence) == 0 ||
		strings.TrimSpace(assessment.Provenance.Evaluator) == "" || strings.TrimSpace(assessment.Provenance.ContractRevision) == "" {
		return errors.New("assessment requires reason, evidence, and evaluator provenance")
	}
	if assessment.Provenance.Evaluator != strings.TrimSpace(assessment.Provenance.Evaluator) ||
		assessment.Provenance.ContractRevision != strings.TrimSpace(assessment.Provenance.ContractRevision) ||
		strings.ContainsAny(assessment.Provenance.Evaluator+assessment.Provenance.ContractRevision, "\r\n") {
		return errors.New("assessment evaluator provenance must be canonical one-line text")
	}
	for _, evidence := range assessment.Evidence {
		if strings.TrimSpace(evidence) == "" {
			return errors.New("assessment evidence must not be empty")
		}
	}
	return nil
}
