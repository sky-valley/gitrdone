package intentfs

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/reviewidentity"
)

func (ledger *Ledger) ConcernAssessment(ctx context.Context, versionID intent.VersionID) (intent.ConcernAssessment, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.ConcernAssessment{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.ConcernAssessment{}, false, errors.New("journal is closed")
	}
	assessment, found := ledger.state.concernAssessments[versionID]
	return cloneConcernAssessment(assessment), found, nil
}

func (ledger *Ledger) RunnableJudgements(ctx context.Context, after intent.VersionID, limit int) ([]intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	start := 0
	if after != "" {
		start = slices.Index(ledger.state.judgementIDs, after)
		if start < 0 {
			return nil, false, intent.ErrVersionNotFound
		}
		start++
	}
	versions := make([]intent.Version, 0, limit)
	index := start
	for ; index < len(ledger.state.judgementIDs) && len(versions) < limit; index++ {
		id := ledger.state.judgementIDs[index]
		if runnableJudgement(ledger.state, id) {
			versions = append(versions, cloneVersion(ledger.state.versions[id]))
		}
	}
	for ; index < len(ledger.state.judgementIDs); index++ {
		if runnableJudgement(ledger.state, ledger.state.judgementIDs[index]) {
			return versions, true, nil
		}
	}
	return versions, false, nil
}

func (ledger *Ledger) RecordConcernAssessment(ctx context.Context, assessment intent.ConcernAssessment) error {
	copy := cloneConcernAssessment(assessment)
	return ledger.append(ctx, journalRecord{
		Format:            journalFormat,
		Kind:              concernAssessmentRecorded,
		ConcernAssessment: &copy,
	})
}

func validateConcernAssessment(state *journalState, record journalRecord) error {
	if record.ConcernAssessment == nil {
		return errors.New("invalid assessment record")
	}
	assessment := *record.ConcernAssessment
	if existing, found := state.concernAssessments[assessment.VersionID]; found {
		if sameConcernAssessment(existing, assessment) {
			return nil
		}
		return intent.ErrConcernAssessmentAlreadyRecorded
	}
	if assessment.VersionID == "" || assessment.GoverningIntent == "" || len(assessment.Evaluations) == 0 {
		return errors.New("assessment requires Version, governing Intent, and evaluations")
	}
	version, found := state.versions[assessment.VersionID]
	if !found {
		return intent.ErrVersionNotFound
	}
	ids := state.versionIDs[version.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != version.ID {
		return intent.ErrVersionAdvanced
	}
	if _, found := state.pendingJudgements[version.ID]; !found {
		return intent.ErrVersionPromotionStarted
	}
	if assessment.GoverningIntent != version.BaseIntent {
		return errors.New("assessment governing Intent does not match the Version base")
	}
	if _, found := state.revisions[assessment.GoverningIntent]; !found {
		return intent.ErrIntentNotFound
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

func runnableJudgement(state journalState, versionID intent.VersionID) bool {
	if _, found := state.pendingJudgements[versionID]; !found {
		return false
	}
	assessment, assessed := state.concernAssessments[versionID]
	return !assessed || assessment.GoverningIntent == state.current.ID && len(assessment.ReviewObligations()) == 0
}

func sameConcernAssessment(left, right intent.ConcernAssessment) bool {
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

func cloneConcernAssessment(assessment intent.ConcernAssessment) intent.ConcernAssessment {
	assessment.Evaluations = slices.Clone(assessment.Evaluations)
	for index := range assessment.Evaluations {
		assessment.Evaluations[index].Evidence = slices.Clone(assessment.Evaluations[index].Evidence)
	}
	return assessment
}
