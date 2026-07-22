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
	Promotion(ctx context.Context, versionID intent.VersionID) (intent.Promoted, bool, error)
	ReadyDependents(ctx context.Context) ([]intent.Proposed, error)
	InspectChange(ctx context.Context, id intent.ChangeID) (intent.ChangeInspection, error)
	Versions(ctx context.Context, query intent.VersionQuery) (intent.VersionPage, error)
}

type NextAction string

const (
	Hold    NextAction = "hold"
	Promote NextAction = "promote"
)

type Triage interface {
	DecideNext(ctx context.Context, proposed intent.Proposed) (NextAction, error)
}

type promoteAllTriage struct{}

func (promoteAllTriage) DecideNext(context.Context, intent.Proposed) (NextAction, error) {
	return Promote, nil
}

type Repositories interface {
	Resolve(ctx context.Context, repoID string) (Repository, error)
	Bootstrap(ctx context.Context, repoID string, content intent.ContentRef) (intent.Revision, error)
}

type Proposal struct {
	IdempotencyKey string
	BaseIntent     intent.RevisionID
	Content        intent.ContentRef
	Producer       string
	Dependencies   []intent.VersionID
}

type Admission struct {
	Proposed  intent.Proposed
	Promotion *intent.Promoted
}

type Service struct {
	repositories Repositories
	triage       Triage
}

func New(repositories Repositories) *Service {
	return NewWithTriage(repositories, promoteAllTriage{})
}

func NewWithTriage(repositories Repositories, triage Triage) *Service {
	if triage == nil {
		triage = promoteAllTriage{}
	}
	return &Service{repositories: repositories, triage: triage}
}

func (service *Service) CurrentIntent(ctx context.Context, repoID string) (intent.Revision, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.Revision{}, err
	}
	return repository.CurrentIntent(), nil
}

func (service *Service) Bootstrap(ctx context.Context, repoID string, content intent.ContentRef) (intent.Revision, error) {
	return service.repositories.Bootstrap(ctx, repoID, content)
}

func (service *Service) Propose(ctx context.Context, repoID string, proposal Proposal) (Admission, error) {
	producer := strings.TrimSpace(proposal.Producer)
	if producer == "" {
		return Admission{}, errors.New("proposal producer is not configured")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return Admission{}, err
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: proposal.IdempotencyKey,
		BaseIntent:     proposal.BaseIntent,
		Content:        proposal.Content,
		Producer:       producer,
		Dependencies:   proposal.Dependencies,
	})
	if err != nil {
		return Admission{}, err
	}
	admission := Admission{Proposed: proposed}
	promoted, err := service.triageAndPromote(ctx, repository, proposed)
	if err != nil {
		return admission, err
	}
	admission.Promotion = promoted
	if promoted != nil {
		if err := service.reconsiderReady(ctx, repository); err != nil {
			return admission, fmt.Errorf("reconsider dependents after promotion: %w", err)
		}
	}
	return admission, nil
}

func (service *Service) InspectChange(ctx context.Context, repoID string, changeID intent.ChangeID) (intent.ChangeInspection, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ChangeInspection{}, err
	}
	return repository.InspectChange(ctx, changeID)
}

func (service *Service) Versions(ctx context.Context, repoID string, query intent.VersionQuery) (intent.VersionPage, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.VersionPage{}, err
	}
	return repository.Versions(ctx, query)
}

func (service *Service) resolve(ctx context.Context, repoID string) (Repository, error) {
	repository, err := service.repositories.Resolve(ctx, repoID)
	if err != nil {
		return nil, err
	}
	if err := service.reconsiderReady(ctx, repository); err != nil {
		return nil, fmt.Errorf("recover ready dependents: %w", err)
	}
	return repository, nil
}

func (service *Service) reconsiderReady(ctx context.Context, repository Repository) error {
	for {
		ready, err := repository.ReadyDependents(ctx)
		if err != nil {
			return err
		}
		advanced := false
		for _, proposed := range ready {
			promoted, err := service.triageAndPromote(ctx, repository, proposed)
			if err != nil {
				return err
			}
			if promoted != nil {
				advanced = true
				break
			}
		}
		if !advanced {
			return nil
		}
	}
}

func (service *Service) triageAndPromote(ctx context.Context, repository Repository, proposed intent.Proposed) (*intent.Promoted, error) {
	promoted, found, err := repository.Promotion(ctx, proposed.Version.ID)
	if err != nil {
		return nil, err
	}
	if found {
		return &promoted, nil
	}
	action, err := service.triage.DecideNext(ctx, proposed)
	if err != nil {
		return nil, fmt.Errorf("choose next judgement action: %w", err)
	}
	if action == Hold {
		return nil, nil
	}
	if action != Promote {
		return nil, fmt.Errorf("unsupported next judgement action %q", action)
	}
	promoted, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	switch {
	case err == nil:
		return &promoted, nil
	case errors.Is(err, intent.ErrIntentAdvanced), errors.Is(err, intent.ErrPromotionPending), errors.Is(err, intent.ErrDependenciesPending):
		return nil, nil
	default:
		return nil, fmt.Errorf("proposal was admitted but immediate promotion did not complete: %w", err)
	}
}
