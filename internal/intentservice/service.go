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
	ReconcileDependent(ctx context.Context, request intent.ReconcileDependentRequest) (intent.ReconciledDependent, error)
	RebaseHeldVersion(ctx context.Context, request intent.RebaseHeldVersionRequest) (intent.RebasedHeldVersion, error)
	Promote(ctx context.Context, request intent.PromoteRequest) (intent.Promoted, error)
	Promotion(ctx context.Context, versionID intent.VersionID) (intent.Promoted, bool, error)
	ReadyDependents(ctx context.Context) ([]intent.Proposed, error)
	RecordReconciliationConflict(ctx context.Context, request intent.ReconciliationConflictRequest) (intent.ReconciliationConflictInspection, error)
	ResolveReconciliationConflict(ctx context.Context, request intent.ResolveReconciliationConflictRequest) (intent.ResolvedReconciliationConflict, error)
	ReconciliationConflict(ctx context.Context, id intent.ConflictID) (intent.ReconciliationConflictInspection, bool, error)
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

type DependentReconciliationRequest struct {
	IdempotencyKey     string
	ExpectedVersion    intent.VersionID
	ReplacedDependency intent.VersionID
	AcceptedVersion    intent.VersionID
	ExpectedIntent     intent.RevisionID
	Content            intent.ContentRef
	Producer           string
	Rationale          string
}

type DependentReconciliationReceipt struct {
	Reconciled intent.ReconciledDependent
	Promotion  *intent.Promoted
}

type HeldVersionRebaseRequest struct {
	IdempotencyKey  string
	ExpectedVersion intent.VersionID
	ExpectedIntent  intent.RevisionID
	Content         intent.ContentRef
	Producer        string
	Rationale       string
}

type HeldVersionRebaseReceipt struct {
	Rebased   intent.RebasedHeldVersion
	Promotion *intent.Promoted
}

type ReconciliationConflictRequest struct {
	IdempotencyKey    string
	FromVersion       intent.VersionID
	ToVersion         intent.VersionID
	DescendantVersion intent.VersionID
	ExpectedIntent    intent.RevisionID
	ReportedBy        string
	AffectedPaths     []string
}

type ReconciliationResolutionRequest struct {
	IdempotencyKey  string
	ConflictID      intent.ConflictID
	ExpectedVersion intent.VersionID
	ExpectedIntent  intent.RevisionID
	Content         intent.ContentRef
	Producer        string
	ResolvedBy      string
	Rationale       string
}

type ReconciliationResolutionReceipt struct {
	Resolved  intent.ResolvedReconciliationConflict
	Promotion *intent.Promoted
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

func (service *Service) ReconcileDependent(
	ctx context.Context,
	repoID string,
	request DependentReconciliationRequest,
) (DependentReconciliationReceipt, error) {
	producer := strings.TrimSpace(request.Producer)
	rationale := strings.TrimSpace(request.Rationale)
	if producer == "" || rationale == "" {
		return DependentReconciliationReceipt{}, errors.New("dependent reconciliation producer and rationale are required")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return DependentReconciliationReceipt{}, err
	}
	reconciled, err := repository.ReconcileDependent(ctx, intent.ReconcileDependentRequest{
		IdempotencyKey:     request.IdempotencyKey,
		ExpectedVersion:    request.ExpectedVersion,
		ReplacedDependency: request.ReplacedDependency,
		AcceptedVersion:    request.AcceptedVersion,
		ExpectedIntent:     request.ExpectedIntent,
		Content:            request.Content,
		Producer:           producer,
		Rationale:          rationale,
	})
	if err != nil {
		return DependentReconciliationReceipt{}, err
	}
	receipt := DependentReconciliationReceipt{Reconciled: reconciled}
	promoted, err := service.decideAndPromote(ctx, repository, JudgementSubject{
		Change:  reconciled.Change,
		Version: reconciled.Version,
	})
	if err != nil {
		return receipt, err
	}
	receipt.Promotion = promoted
	if promoted != nil {
		if err := service.reconsiderReady(ctx, repository); err != nil {
			return receipt, fmt.Errorf("reconsider dependents after dependent reconciliation promotion: %w", err)
		}
	}
	return receipt, nil
}

func (service *Service) RebaseHeldVersion(
	ctx context.Context,
	repoID string,
	request HeldVersionRebaseRequest,
) (HeldVersionRebaseReceipt, error) {
	producer := strings.TrimSpace(request.Producer)
	rationale := strings.TrimSpace(request.Rationale)
	if producer == "" || rationale == "" {
		return HeldVersionRebaseReceipt{}, errors.New("held version rebase producer and rationale are required")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return HeldVersionRebaseReceipt{}, err
	}
	rebased, err := repository.RebaseHeldVersion(ctx, intent.RebaseHeldVersionRequest{
		IdempotencyKey:  request.IdempotencyKey,
		ExpectedVersion: request.ExpectedVersion,
		ExpectedIntent:  request.ExpectedIntent,
		Content:         request.Content,
		Producer:        producer,
		Rationale:       rationale,
	})
	if err != nil {
		return HeldVersionRebaseReceipt{}, err
	}
	receipt := HeldVersionRebaseReceipt{Rebased: rebased}
	promoted, err := service.decideAndPromote(ctx, repository, JudgementSubject{
		Change:  rebased.Change,
		Version: rebased.Version,
	})
	if err != nil {
		return receipt, err
	}
	receipt.Promotion = promoted
	if promoted != nil {
		if err := service.reconsiderReady(ctx, repository); err != nil {
			return receipt, fmt.Errorf("reconsider dependents after held version rebase promotion: %w", err)
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

func (service *Service) RecordReconciliationConflict(ctx context.Context, repoID string, request ReconciliationConflictRequest) (intent.ReconciliationConflictInspection, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciliationConflictInspection{}, err
	}
	expectedIntent := request.ExpectedIntent
	if expectedIntent == "" {
		promoted, found, err := repository.Promotion(ctx, request.ToVersion)
		if err != nil {
			return intent.ReconciliationConflictInspection{}, err
		}
		if !found {
			return intent.ReconciliationConflictInspection{}, intent.ErrVersionNotPromoted
		}
		expectedIntent = promoted.Intent.ID
	}
	return repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    request.IdempotencyKey,
		FromVersion:       request.FromVersion,
		ToVersion:         request.ToVersion,
		DescendantVersion: request.DescendantVersion,
		ExpectedIntent:    expectedIntent,
		ReportedBy:        strings.TrimSpace(request.ReportedBy),
		AffectedPaths:     request.AffectedPaths,
	})
}

func (service *Service) ResolveReconciliationConflict(ctx context.Context, repoID string, request ReconciliationResolutionRequest) (ReconciliationResolutionReceipt, error) {
	producer := strings.TrimSpace(request.Producer)
	resolvedBy := strings.TrimSpace(request.ResolvedBy)
	rationale := strings.TrimSpace(request.Rationale)
	if producer == "" || resolvedBy == "" || rationale == "" {
		return ReconciliationResolutionReceipt{}, errors.New("resolution producer, actor, and rationale are required")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return ReconciliationResolutionReceipt{}, err
	}
	resolved, err := repository.ResolveReconciliationConflict(ctx, intent.ResolveReconciliationConflictRequest{
		IdempotencyKey:  request.IdempotencyKey,
		ConflictID:      request.ConflictID,
		ExpectedVersion: request.ExpectedVersion,
		ExpectedIntent:  request.ExpectedIntent,
		Content:         request.Content,
		Producer:        producer,
		ResolvedBy:      resolvedBy,
		Rationale:       rationale,
	})
	if err != nil {
		return ReconciliationResolutionReceipt{}, err
	}
	receipt := ReconciliationResolutionReceipt{Resolved: resolved}
	promoted, err := service.decideAndPromote(ctx, repository, JudgementSubject{
		Change:  resolved.Change,
		Version: resolved.Version,
	})
	if err != nil {
		return receipt, err
	}
	receipt.Promotion = promoted
	if promoted != nil {
		if err := service.reconsiderReady(ctx, repository); err != nil {
			return receipt, fmt.Errorf("reconsider dependents after reconciliation resolution promotion: %w", err)
		}
	}
	return receipt, nil
}

func (service *Service) ReconciliationConflict(ctx context.Context, repoID string, conflictID intent.ConflictID) (intent.ReconciliationConflictInspection, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciliationConflictInspection{}, err
	}
	conflict, found, err := repository.ReconciliationConflict(ctx, conflictID)
	if err != nil {
		return intent.ReconciliationConflictInspection{}, err
	}
	if !found {
		return intent.ReconciliationConflictInspection{}, intent.ErrReconciliationConflictNotFound
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
