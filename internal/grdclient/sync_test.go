package grdclient

import "testing"

func TestValidateResolvedConflictChangeRequiresExactPromotionLineage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*reconciliationConflictResponse, *changeResponse, *changeResponse)
	}{
		{
			name: "resolution base differs from accepted parent intent",
			mutate: func(conflict *reconciliationConflictResponse, _ *changeResponse, _ *changeResponse) {
				conflict.Resolution.BaseIntent = "intent_other"
			},
		},
		{
			name: "resolved version base differs from resolution",
			mutate: func(_ *reconciliationConflictResponse, _ *changeResponse, resolved *changeResponse) {
				resolved.LatestVersion.BaseIntent = "intent_other"
			},
		},
		{
			name: "promotion starts from a different intent",
			mutate: func(_ *reconciliationConflictResponse, _ *changeResponse, resolved *changeResponse) {
				resolved.LatestPromotion.FromIntent = "intent_other"
			},
		},
		{
			name: "promotion names a different version",
			mutate: func(_ *reconciliationConflictResponse, _ *changeResponse, resolved *changeResponse) {
				resolved.LatestPromotion.Version = "version_other"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conflict, parent, resolved := validResolvedConflictChange()
			tt.mutate(&conflict, &parent, &resolved)

			if err := validateResolvedConflictChange(conflict, parent, resolved); err == nil {
				t.Fatal("validate resolved conflict change succeeded for inconsistent lineage")
			}
		})
	}
}

func TestValidateResolvedConflictChangeAllowsJudgementPendingResolution(t *testing.T) {
	conflict, parent, resolved := validResolvedConflictChange()
	resolved.LatestPromotion = nil

	if err := validateResolvedConflictChange(conflict, parent, resolved); err != nil {
		t.Fatalf("validate pending resolution: %v", err)
	}
}

func TestValidateReconciliationConflictAllowsSupersededResolvedAttempt(t *testing.T) {
	conflict, _, _ := validResolvedConflictChange()
	conflict.ID = "conflict_c"
	conflict.State = "superseded"
	conflict.Change.ID = "change_c"
	conflict.Version.ID = "version_c"
	conflict.Version.Change = "change_c"
	conflict.Version.BaseIntent = "intent_a"
	conflict.Version.Producer = "ion"
	conflict.Version.ContentRef.Engine = "git"
	conflict.Version.ContentRef.Revision = "cccccccccccccccccccccccccccccccccccccccc"
	conflict.FromVersion = "version_b"
	conflict.ToVersion = "version_b_prime"
	conflict.ReportedBy = "repository-engine"
	state := continuationState{
		ConflictID:    conflict.ID,
		ParentVersion: conflict.FromVersion,
	}

	if err := validateReconciliationConflict(conflict, state, conflict.ToVersion, "", ""); err != nil {
		t.Fatalf("validate superseded resolved attempt: %v", err)
	}
}

func validResolvedConflictChange() (reconciliationConflictResponse, changeResponse, changeResponse) {
	var conflict reconciliationConflictResponse
	conflict.Change.ID = "change_c"
	conflict.BaseIntent = "intent_b_prime"
	conflict.Resolution = &struct {
		ID          string `json:"id"`
		FromVersion string `json:"fromVersion"`
		ToVersion   string `json:"toVersion"`
		BaseIntent  string `json:"baseIntent"`
		ResolvedBy  string `json:"resolvedBy"`
		Rationale   string `json:"rationale"`
	}{
		ID:          "resolution_c",
		FromVersion: "version_c",
		ToVersion:   "version_c_prime",
		BaseIntent:  "intent_b_prime",
		ResolvedBy:  "judgement-agent",
		Rationale:   "combined competing edits",
	}

	var parent changeResponse
	parent.ID = "change_b"
	parent.LatestPromotion = &struct {
		FromIntent string `json:"fromIntent"`
		ToIntent   string `json:"toIntent"`
		Version    string `json:"version"`
	}{
		FromIntent: "intent_a",
		ToIntent:   "intent_b_prime",
		Version:    "version_b_prime",
	}

	var resolved changeResponse
	resolved.ID = "change_c"
	resolved.LatestVersion.ID = "version_c_prime"
	resolved.LatestVersion.BaseIntent = "intent_b_prime"
	resolved.LatestVersion.ContentRef.Engine = "git"
	resolved.LatestVersion.ContentRef.Revision = "cccccccccccccccccccccccccccccccccccccccc"
	resolved.LatestPromotion = &struct {
		FromIntent string `json:"fromIntent"`
		ToIntent   string `json:"toIntent"`
		Version    string `json:"version"`
	}{
		FromIntent: "intent_b_prime",
		ToIntent:   "intent_c_prime",
		Version:    "version_c_prime",
	}
	return conflict, parent, resolved
}
