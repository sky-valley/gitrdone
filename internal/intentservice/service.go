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
	Amend(ctx context.Context, request intent.AmendRequest) (intent.Amended, error)
	Promote(ctx context.Context, request intent.PromoteRequest) (intent.Promoted, error)
	Promotion(ctx context.Context, versionID intent.VersionID) (intent.Promoted, bool, error)
	ReadyDependents(ctx context.Context) ([]intent.Proposed, error)
	RecordReconciliationConflict(ctx context.Context, request intent.ReconciliationConflictRequest) (intent.ReconciliationConflict, error)
	ReconciliationConflict(ctx context.Context, id intent.ConflictID) (intent.ReconciliationConflict, bool, error)
	ReconciliationConflicts(ctx context.Context, query intent.ReconciliationConflictQuery) (intent.ReconciliationConflictPage, error)
	InspectChange(ctx context.Context, id intent.ChangeID) (intent.ChangeInspection, error)
	Versions(ctx context.Context, query intent.VersionQuery) (intent.VersionPage, error)
}

type PromotionDecision string

const (
	DeferPromotion PromotionDecision = "defer"
	PromoteNow     PromotionDecision = "promote"
)

type JudgementSubject struct {
	Change  intent.Change
	Version intent.Version
}

type PromotionDecider interface {
	DecidePromotion(ctx context.Context, subject JudgementSubject) (PromotionDecision, error)
}

type promoteAllDecider struct{}

func (promoteAllDecider) DecidePromotion(context.Context, JudgementSubject) (PromotionDecision, error) {
	return PromoteNow, nil
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

type AmendmentRequest struct {
	IdempotencyKey  string
	ChangeID        intent.ChangeID
	ExpectedVersion intent.VersionID
	Content         intent.ContentRef
	Producer        string
	Rationale       string
}

type AmendmentReceipt struct {
	Amended   intent.Amended
	Promotion *intent.Promoted
}

type ReconciliationConflictRequest struct {
	IdempotencyKey    string
	FromVersion       intent.VersionID
	ToVersion         intent.VersionID
	DescendantVersion intent.VersionID
	ReportedBy        string
	AffectedPaths     []string
}

type Service struct {
	repositories Repositories
	decider      PromotionDecider
}

func New(repositories Repositories) *Service {
	return NewWithPromotionDecider(repositories, promoteAllDecider{})
}

func NewWithPromotionDecider(repositories Repositories, decider PromotionDecider) *Service {
	if decider == nil {
		decider = promoteAllDecider{}
	}
	return &Service{repositories: repositories, decider: decider}
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
	promoted, err := service.decideAndPromote(ctx, repository, JudgementSubject{Change: proposed.Change, Version: proposed.Version})
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

func (service *Service) Amend(ctx context.Context, repoID string, amendment AmendmentRequest) (AmendmentReceipt, error) {
	producer := strings.TrimSpace(amendment.Producer)
	rationale := strings.TrimSpace(amendment.Rationale)
	if producer == "" || rationale == "" {
		return AmendmentReceipt{}, errors.New("amendment producer and rationale are required")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return AmendmentReceipt{}, err
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  amendment.IdempotencyKey,
		ChangeID:        amendment.ChangeID,
		ExpectedVersion: amendment.ExpectedVersion,
		Content:         amendment.Content,
		Producer:        producer,
		Rationale:       rationale,
	})
	if err != nil {
		return AmendmentReceipt{}, err
	}
	receipt := AmendmentReceipt{Amended: amended}
	promoted, err := service.decideAndPromote(ctx, repository, JudgementSubject{
		Change:  amended.Change,
		Version: amended.Version,
	})
	if err != nil {
		return receipt, err
	}
	receipt.Promotion = promoted
	if promoted != nil {
		if err := service.reconsiderReady(ctx, repository); err != nil {
			return receipt, fmt.Errorf("reconsider dependents after amendment promotion: %w", err)
		}
	}
	return receipt, nil
}

func (service *Service) InspectChange(ctx context.Context, repoID string, changeID intent.ChangeID) (intent.ChangeInspection, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ChangeInspection{}, err
	}
	return repository.InspectChange(ctx, changeID)
}

func (service *Service) RecordReconciliationConflict(ctx context.Context, repoID string, request ReconciliationConflictRequest) (intent.ReconciliationConflict, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciliationConflict{}, err
	}
	return repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    request.IdempotencyKey,
		FromVersion:       request.FromVersion,
		ToVersion:         request.ToVersion,
		DescendantVersion: request.DescendantVersion,
		ReportedBy:        strings.TrimSpace(request.ReportedBy),
		AffectedPaths:     request.AffectedPaths,
	})
}

func (service *Service) ReconciliationConflict(ctx context.Context, repoID string, conflictID intent.ConflictID) (intent.ReconciliationConflict, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciliationConflict{}, err
	}
	conflict, found, err := repository.ReconciliationConflict(ctx, conflictID)
	if err != nil {
		return intent.ReconciliationConflict{}, err
	}
	if !found {
		return intent.ReconciliationConflict{}, intent.ErrReconciliationConflictNotFound
	}
	return conflict, nil
}

func (service *Service) ReconciliationConflicts(ctx context.Context, repoID string, query intent.ReconciliationConflictQuery) (intent.ReconciliationConflictPage, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciliationConflictPage{}, err
	}
	return repository.ReconciliationConflicts(ctx, query)
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
			promoted, err := service.decideAndPromote(ctx, repository, JudgementSubject{Change: proposed.Change, Version: proposed.Version})
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

func (service *Service) decideAndPromote(ctx context.Context, repository Repository, subject JudgementSubject) (*intent.Promoted, error) {
	promoted, found, err := repository.Promotion(ctx, subject.Version.ID)
	if err != nil {
		return nil, err
	}
	if found {
		return &promoted, nil
	}
	decision, err := service.decider.DecidePromotion(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("decide immediate promotion: %w", err)
	}
	if decision == DeferPromotion {
		return nil, nil
	}
	if decision != PromoteNow {
		return nil, fmt.Errorf("unsupported promotion decision %q", decision)
	}
	promoted, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      subject.Version.ID,
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
