package intentservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
)

var ErrRepositoryNotFound = errors.New("repository not found")
var ErrRepositoryAlreadyInitialized = errors.New("repository intent is already initialized")

type Repository interface {
	CurrentIntent() intent.Revision
	Propose(ctx context.Context, proposal intent.Proposal) (intent.Proposed, error)
	Promote(ctx context.Context, request intent.PromoteRequest) (intent.Promoted, error)
	InspectChange(ctx context.Context, id intent.ChangeID) (intent.ChangeInspection, error)
	Versions(ctx context.Context, query intent.VersionQuery) (intent.VersionPage, error)
}

type Repositories interface {
	Resolve(ctx context.Context, repoID string) (Repository, error)
	Bootstrap(ctx context.Context, repoID string, content intent.ContentRef) (intent.Revision, error)
}

type Proposal struct {
	IdempotencyKey string
	BaseIntent     intent.RevisionID
	Content        intent.ContentRef
}

type Admission struct {
	Proposed  intent.Proposed
	Promotion *intent.Promoted
}

type Service struct {
	repositories Repositories
	producer     string
}

func New(repositories Repositories, producer string) *Service {
	return &Service{repositories: repositories, producer: strings.TrimSpace(producer)}
}

func (service *Service) CurrentIntent(ctx context.Context, repoID string) (intent.Revision, error) {
	repository, err := service.repositories.Resolve(ctx, repoID)
	if err != nil {
		return intent.Revision{}, err
	}
	return repository.CurrentIntent(), nil
}

func (service *Service) Bootstrap(ctx context.Context, repoID string, content intent.ContentRef) (intent.Revision, error) {
	return service.repositories.Bootstrap(ctx, repoID, content)
}

func (service *Service) Propose(ctx context.Context, repoID string, proposal Proposal) (Admission, error) {
	if service.producer == "" {
		return Admission{}, errors.New("proposal producer is not configured")
	}
	repository, err := service.repositories.Resolve(ctx, repoID)
	if err != nil {
		return Admission{}, err
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: proposal.IdempotencyKey,
		BaseIntent:     proposal.BaseIntent,
		Content:        proposal.Content,
		Producer:       service.producer,
	})
	if err != nil {
		return Admission{}, err
	}
	admission := Admission{Proposed: proposed}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: proposed.Version.BaseIntent,
	})
	switch {
	case err == nil:
		admission.Promotion = &promoted
		return admission, nil
	case errors.Is(err, intent.ErrIntentAdvanced), errors.Is(err, intent.ErrPromotionPending):
		return admission, nil
	default:
		return Admission{}, fmt.Errorf("proposal was admitted but immediate promotion did not complete: %w", err)
	}
}

func (service *Service) InspectChange(ctx context.Context, repoID string, changeID intent.ChangeID) (intent.ChangeInspection, error) {
	repository, err := service.repositories.Resolve(ctx, repoID)
	if err != nil {
		return intent.ChangeInspection{}, err
	}
	return repository.InspectChange(ctx, changeID)
}

func (service *Service) Versions(ctx context.Context, repoID string, query intent.VersionQuery) (intent.VersionPage, error) {
	repository, err := service.repositories.Resolve(ctx, repoID)
	if err != nil {
		return intent.VersionPage{}, err
	}
	return repository.Versions(ctx, query)
}
