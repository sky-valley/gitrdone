package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sky-valley/gitrdone/internal/reviewidentity"
)

type ConcernEvaluation struct {
	Concern        string
	Prompt         string
	Reviewer       string
	RequiresReview bool
	Reason         string
	Evidence       []string
}

type ConcernAssessment struct {
	VersionID       VersionID
	GoverningIntent RevisionID
	Evaluations     []ConcernEvaluation
}

type ReviewObligation struct {
	VersionID VersionID
	Concern   string
	Reviewer  string
	Reason    string
	Evidence  []string
}

type ConcernAssessmentContext struct {
	Version         Version
	GoverningIntent Revision
}

func (assessment ConcernAssessment) ReviewObligations() []ReviewObligation {
	obligations := make([]ReviewObligation, 0, len(assessment.Evaluations))
	for _, evaluation := range assessment.Evaluations {
		if !evaluation.RequiresReview {
			continue
		}
		obligations = append(obligations, ReviewObligation{
			VersionID: assessment.VersionID,
			Concern:   evaluation.Concern,
			Reviewer:  evaluation.Reviewer,
			Reason:    evaluation.Reason,
			Evidence:  slices.Clone(evaluation.Evidence),
		})
	}
	return obligations
}

func (repository *Repository) ConcernAssessment(ctx context.Context, versionID VersionID) (ConcernAssessment, bool, error) {
	if versionID == "" {
		return ConcernAssessment{}, false, errors.New("assessment version id is required")
	}
	assessment, found, err := repository.concernAssessments.ConcernAssessment(ctx, versionID)
	if err != nil {
		return ConcernAssessment{}, false, fmt.Errorf("read Version assessment: %w", err)
	}
	return assessment, found, nil
}

func (repository *Repository) ConcernAssessmentContext(ctx context.Context, versionID VersionID) (ConcernAssessmentContext, error) {
	if versionID == "" {
		return ConcernAssessmentContext{}, errors.New("assessment version id is required")
	}
	version, found, err := repository.changes.Version(ctx, versionID)
	if err != nil {
		return ConcernAssessmentContext{}, fmt.Errorf("read assessed Version: %w", err)
	}
	if !found {
		return ConcernAssessmentContext{}, ErrVersionNotFound
	}
	latest, found, err := repository.changes.LatestVersion(ctx, version.ChangeID)
	if err != nil {
		return ConcernAssessmentContext{}, fmt.Errorf("read latest assessed Version: %w", err)
	}
	if !found || latest.ID != version.ID {
		return ConcernAssessmentContext{}, ErrVersionAdvanced
	}
	governing, found, err := repository.intents.Revision(ctx, version.BaseIntent)
	if err != nil {
		return ConcernAssessmentContext{}, fmt.Errorf("read governing Intent: %w", err)
	}
	if !found {
		return ConcernAssessmentContext{}, ErrIntentNotFound
	}
	return ConcernAssessmentContext{Version: cloneVersion(version), GoverningIntent: governing}, nil
}

func (repository *Repository) RecordConcernAssessment(ctx context.Context, assessment ConcernAssessment) (ConcernAssessment, error) {
	if err := validateConcernAssessmentShape(assessment); err != nil {
		return ConcernAssessment{}, err
	}
	if err := repository.concernAssessments.RecordConcernAssessment(ctx, assessment); err != nil {
		return ConcernAssessment{}, fmt.Errorf("record Version assessment: %w", err)
	}
	return cloneConcernAssessment(assessment), nil
}

func (repository *Repository) RunnableJudgements(ctx context.Context, query PendingJudgementQuery) (PendingJudgementPage, error) {
	if query.Limit < 1 || query.Limit > 100 {
		return PendingJudgementPage{}, errors.New("runnable assessment page limit must be between 1 and 100")
	}
	versions, more, err := repository.concernAssessments.RunnableJudgements(ctx, query.After, query.Limit)
	if err != nil {
		return PendingJudgementPage{}, fmt.Errorf("read runnable judgements: %w", err)
	}
	page := PendingJudgementPage{Versions: versions}
	if more && len(versions) > 0 {
		page.NextCursor = versions[len(versions)-1].ID
	}
	return page, nil
}

func (ledger *transientLedger) ConcernAssessment(_ context.Context, versionID VersionID) (ConcernAssessment, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	assessment, found := ledger.concernAssessments[versionID]
	return cloneConcernAssessment(assessment), found, nil
}

func (ledger *transientLedger) RunnableJudgements(_ context.Context, after VersionID, limit int) ([]Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	return runnableJudgements(
		ledger.judgementIDs,
		ledger.pendingJudgements,
		ledger.concernAssessments,
		ledger.versions,
		ledger.current.ID,
		after,
		limit,
	)
}

func (ledger *transientLedger) RecordConcernAssessment(_ context.Context, assessment ConcernAssessment) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.concernAssessments[assessment.VersionID]; found {
		if concernAssessmentsEqual(existing, assessment) {
			return nil
		}
		return ErrConcernAssessmentAlreadyRecorded
	}
	if err := validateConcernAssessmentState(
		ledger.revisions,
		ledger.versions,
		ledger.versionIDs,
		ledger.pendingJudgements,
		assessment,
	); err != nil {
		return err
	}
	ledger.concernAssessments[assessment.VersionID] = cloneConcernAssessment(assessment)
	return nil
}

func validateConcernAssessmentShape(assessment ConcernAssessment) error {
	if assessment.VersionID == "" || assessment.GoverningIntent == "" || len(assessment.Evaluations) == 0 {
		return errors.New("assessment requires Version, governing Intent, and evaluations")
	}
	concerns := make(map[string]struct{}, len(assessment.Evaluations))
	for _, evaluation := range assessment.Evaluations {
		if strings.TrimSpace(evaluation.Concern) == "" ||
			strings.TrimSpace(evaluation.Prompt) == "" ||
			strings.TrimSpace(evaluation.Reviewer) == "" ||
			strings.TrimSpace(evaluation.Reason) == "" ||
			len(evaluation.Evidence) == 0 {
			return errors.New("assessment evaluation requires concern, prompt, reviewer, reason, and evidence")
		}
		if evaluation.Concern != strings.TrimSpace(evaluation.Concern) {
			return errors.New("assessment concern identity must be canonical")
		}
		if strings.ContainsAny(evaluation.Prompt, "\r\n") {
			return errors.New("assessment prompt must be one line")
		}
		if reviewer, valid := reviewidentity.Canonical(evaluation.Reviewer); !valid || reviewer != evaluation.Reviewer {
			return errors.New("assessment reviewer must be a canonical email subject")
		}
		if _, duplicate := concerns[evaluation.Concern]; duplicate {
			return errors.New("assessment concerns must be unique")
		}
		concerns[evaluation.Concern] = struct{}{}
		for _, evidence := range evaluation.Evidence {
			if strings.TrimSpace(evidence) == "" {
				return errors.New("assessment evidence must not be empty")
			}
		}
	}
	return nil
}

func validateConcernAssessmentState(
	revisions map[RevisionID]Revision,
	versions map[VersionID]Version,
	versionIDs map[ChangeID][]VersionID,
	pending map[VersionID]struct{},
	assessment ConcernAssessment,
) error {
	if err := validateConcernAssessmentShape(assessment); err != nil {
		return err
	}
	version, found := versions[assessment.VersionID]
	if !found {
		return ErrVersionNotFound
	}
	ids := versionIDs[version.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != version.ID {
		return ErrVersionAdvanced
	}
	if _, found := pending[version.ID]; !found {
		return ErrVersionPromotionStarted
	}
	if assessment.GoverningIntent != version.BaseIntent {
		return errors.New("assessment governing Intent does not match the Version base")
	}
	if _, found := revisions[assessment.GoverningIntent]; !found {
		return ErrIntentNotFound
	}
	return nil
}

func runnableJudgements(
	ordered []VersionID,
	pending map[VersionID]struct{},
	concernAssessments map[VersionID]ConcernAssessment,
	versions map[VersionID]Version,
	currentIntent RevisionID,
	after VersionID,
	limit int,
) ([]Version, bool, error) {
	start := 0
	if after != "" {
		start = slices.Index(ordered, after)
		if start < 0 {
			return nil, false, ErrVersionNotFound
		}
		start++
	}
	result := make([]Version, 0, limit)
	index := start
	for ; index < len(ordered) && len(result) < limit; index++ {
		id := ordered[index]
		if runnableJudgement(id, pending, concernAssessments, currentIntent) {
			result = append(result, cloneVersion(versions[id]))
		}
	}
	for ; index < len(ordered); index++ {
		if runnableJudgement(ordered[index], pending, concernAssessments, currentIntent) {
			return result, true, nil
		}
	}
	return result, false, nil
}

func runnableJudgement(versionID VersionID, pending map[VersionID]struct{}, concernAssessments map[VersionID]ConcernAssessment, currentIntent RevisionID) bool {
	if _, found := pending[versionID]; !found {
		return false
	}
	assessment, assessed := concernAssessments[versionID]
	return !assessed || assessment.GoverningIntent == currentIntent && len(assessment.ReviewObligations()) == 0
}

func cloneConcernAssessment(assessment ConcernAssessment) ConcernAssessment {
	assessment.Evaluations = slices.Clone(assessment.Evaluations)
	for index := range assessment.Evaluations {
		assessment.Evaluations[index].Evidence = slices.Clone(assessment.Evaluations[index].Evidence)
	}
	return assessment
}

func concernAssessmentsEqual(left, right ConcernAssessment) bool {
	if left.VersionID != right.VersionID || left.GoverningIntent != right.GoverningIntent || len(left.Evaluations) != len(right.Evaluations) {
		return false
	}
	for index := range left.Evaluations {
		l, r := left.Evaluations[index], right.Evaluations[index]
		if l.Concern != r.Concern || l.Prompt != r.Prompt || l.Reviewer != r.Reviewer || l.RequiresReview != r.RequiresReview || l.Reason != r.Reason || !slices.Equal(l.Evidence, r.Evidence) {
			return false
		}
	}
	return true
}
