package judgementpi

import (
	"context"
	"os"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/judgement"
)

func TestLiveAnthropicCoveConcernAssessment(t *testing.T) {
	if os.Getenv("GITRDONE_LIVE_ANTHROPIC") == "" {
		t.Skip("set GITRDONE_LIVE_ANTHROPIC to exercise the real Anthropic evaluator")
	}
	evaluator, err := NewEvaluator(os.Getenv("ANTHROPIC_API_KEY"), DefaultModelID)
	if err != nil {
		t.Fatal(err)
	}
	base := judgement.ConcernRequest{
		RepoID: "repo_cove",
		Version: intent.Version{
			ID:       "version_candidate",
			ChangeID: "change_candidate",
		},
		GoverningIntent: intent.Revision{ID: "intent_current"},
		Purpose:         "A calm workplace chat application for team channels, direct messages, and presence, deliberately designed without notification-driven urgency.",
		Priorities:      "Architecture, data-model, and infrastructure changes require review from noam@skyvalley.ac.",
		Concern: judgement.Concern{
			Name:     "architecture-data-infrastructure",
			Prompt:   "Does this change alter architecture, data models, or infrastructure requirements such as databases, environment variables, services, or deployment topology?",
			Reviewer: "noam@skyvalley.ac",
		},
	}

	architecture := base
	architecture.ChangeEvidence = "Changed paths:\n- go.mod\n- internal/store/postgres.go\n- .env.example\n\nPatch:\n+ require github.com/jackc/pgx/v5\n+ DATABASE_URL=\n+ func OpenPostgres(databaseURL string)"
	result, err := evaluator.Evaluate(context.Background(), architecture)
	if err != nil {
		t.Fatalf("evaluate architecture change: %v", err)
	}
	if !result.RequiresReview {
		t.Fatalf("architecture change was cleared: %#v", result)
	}

	typo := base
	typo.Version.ID = "version_typo"
	typo.ChangeEvidence = "Changed paths:\n- README.md\n\nPatch:\n- A calm workpalce chat application.\n+ A calm workplace chat application."
	result, err = evaluator.Evaluate(context.Background(), typo)
	if err != nil {
		t.Fatalf("evaluate typo change: %v", err)
	}
	if result.RequiresReview {
		t.Fatalf("README typo was escalated as architecture: %#v", result)
	}
}
