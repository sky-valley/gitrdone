package judgement

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/reviewidentity"
)

const (
	purposePath    = ".gitrdone/purpose.md"
	prioritiesPath = ".gitrdone/priorities.md"
)

type RepositoryContent interface {
	ReadText(ctx context.Context, repoID string, content intent.ContentRef, path string) (string, error)
	Compare(ctx context.Context, repoID string, base, candidate intent.ContentRef) (string, error)
}

type AssessmentInput struct {
	Purpose        string
	Priorities     string
	ChangeEvidence string
	Concerns       []Concern
}

type AssessmentInputSource interface {
	Load(ctx context.Context, repoID string, assessment intent.ConcernAssessmentContext) (AssessmentInput, error)
}

type RepositoryAssessmentInputSource struct {
	content RepositoryContent
}

func NewRepositoryAssessmentInputSource(content RepositoryContent) (*RepositoryAssessmentInputSource, error) {
	if content == nil {
		return nil, errors.New("repository assessment input source requires content")
	}
	return &RepositoryAssessmentInputSource{content: content}, nil
}

func (source *RepositoryAssessmentInputSource) Load(ctx context.Context, repoID string, assessment intent.ConcernAssessmentContext) (AssessmentInput, error) {
	governing := assessment.GoverningIntent.Content
	purpose, err := source.content.ReadText(ctx, repoID, governing, purposePath)
	if err != nil {
		return AssessmentInput{}, fmt.Errorf("read governing repository purpose: %w", err)
	}
	if strings.TrimSpace(purpose) == "" {
		return AssessmentInput{}, errors.New("governing repository purpose is empty")
	}
	priorities, err := source.content.ReadText(ctx, repoID, governing, prioritiesPath)
	if err != nil {
		return AssessmentInput{}, fmt.Errorf("read governing repository priorities: %w", err)
	}
	concerns, err := parseConcerns(priorities)
	if err != nil {
		return AssessmentInput{}, fmt.Errorf("parse governing repository priorities: %w", err)
	}
	evidence, err := source.content.Compare(ctx, repoID, governing, assessment.Version.Content)
	if err != nil {
		return AssessmentInput{}, fmt.Errorf("compare candidate to governing content: %w", err)
	}
	if strings.TrimSpace(evidence) == "" {
		return AssessmentInput{}, errors.New("candidate comparison produced no evidence")
	}
	return AssessmentInput{
		Purpose:        purpose,
		Priorities:     priorities,
		ChangeEvidence: evidence,
		Concerns:       concerns,
	}, nil
}

func parseConcerns(priorities string) ([]Concern, error) {
	if strings.TrimSpace(priorities) == "" {
		return nil, errors.New("repository priorities are empty")
	}
	var concerns []Concern
	var current *Concern
	finish := func() error {
		if current == nil {
			return nil
		}
		normalized, err := normalizeConcern(*current)
		if err != nil {
			return err
		}
		concerns = append(concerns, normalized)
		current = nil
		return nil
	}
	for _, rawLine := range strings.Split(priorities, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "## ") {
			if err := finish(); err != nil {
				return nil, err
			}
			current = &Concern{Name: strings.TrimSpace(strings.TrimPrefix(line, "## "))}
			continue
		}
		if current == nil || line == "" {
			continue
		}
		if value, found := strings.CutPrefix(line, "Reviewer:"); found {
			if current.Reviewer != "" {
				return nil, fmt.Errorf("concern %q repeats Reviewer", current.Name)
			}
			current.Reviewer = strings.TrimSpace(value)
			continue
		}
		if value, found := strings.CutPrefix(line, "Prompt:"); found {
			if current.Prompt != "" {
				return nil, fmt.Errorf("concern %q repeats Prompt", current.Name)
			}
			current.Prompt = strings.TrimSpace(value)
		}
	}
	if err := finish(); err != nil {
		return nil, err
	}
	if len(concerns) == 0 {
		return nil, errors.New("repository priorities define no concerns")
	}
	seen := make(map[string]struct{}, len(concerns))
	for _, concern := range concerns {
		if _, duplicate := seen[concern.Name]; duplicate {
			return nil, fmt.Errorf("concern %q is repeated", concern.Name)
		}
		seen[concern.Name] = struct{}{}
	}
	return concerns, nil
}

func normalizeConcern(concern Concern) (Concern, error) {
	concern.Name = strings.TrimSpace(concern.Name)
	concern.Prompt = strings.TrimSpace(concern.Prompt)
	reviewer, validReviewer := reviewidentity.Canonical(concern.Reviewer)
	concern.Reviewer = reviewer
	if concern.Name == "" || concern.Prompt == "" || concern.Reviewer == "" {
		return Concern{}, errors.New("assessment concern requires name, one-line prompt, and reviewer")
	}
	if !validReviewer {
		return Concern{}, errors.New("assessment concern reviewer must be a canonical email subject")
	}
	if strings.ContainsAny(concern.Prompt, "\r\n") {
		return Concern{}, errors.New("assessment concern prompt must be one line")
	}
	return concern, nil
}
