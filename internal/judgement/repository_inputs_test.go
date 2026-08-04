package judgement_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/judgement"
)

const covePurpose = "A calm workplace chat application for team channels, direct messages, and presence, deliberately designed without notification-driven urgency."

const covePriorities = `# Priorities

## architecture-data-infrastructure
Reviewer: noam@skyvalley.ac
Prompt: Does this change alter architecture, data models, or infrastructure requirements such as databases, environment variables, services, or deployment topology?

## design-user-experience
Reviewer: ion@skyvalley.ac
Prompt: Does this change alter the design system or user experience?

## copy-commercial-impact
Reviewer: iris@skyvalley.ac
Prompt: Does this change alter copywriting or create commercial impact?

## prompts-models
Reviewer: jules@skyvalley.ac
Prompt: Does this change alter prompts, model selection, or how LLMs are used?
`

func TestRepositoryAssessmentInputSourceUsesGoverningIntentForPolicyAndCandidateOnlyForEvidence(t *testing.T) {
	accepted := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	candidate := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	content := &recordingRepositoryContent{
		files: map[string]string{
			"aaaaaaaa:.gitrdone/purpose.md":    covePurpose,
			"aaaaaaaa:.gitrdone/priorities.md": covePriorities,
		},
		difference: "M\tinternal/store/postgres.go\n\n@@ -1 +1 @@\n-memory\n+postgres\n",
	}
	source, err := judgement.NewRepositoryAssessmentInputSource(content)
	if err != nil {
		t.Fatalf("new repository assessment input source: %v", err)
	}

	got, err := source.Load(context.Background(), "repo_cove", intent.ConcernAssessmentContext{
		Version:         intent.Version{Content: candidate},
		GoverningIntent: intent.Revision{Content: accepted},
	})
	if err != nil {
		t.Fatalf("load repository assessment input: %v", err)
	}
	if got.Purpose != covePurpose || got.Priorities != covePriorities || got.ChangeEvidence != content.difference {
		t.Fatalf("assessment input = %#v", got)
	}
	wantConcerns := []judgement.Concern{
		{Name: "architecture-data-infrastructure", Reviewer: "noam@skyvalley.ac", Prompt: "Does this change alter architecture, data models, or infrastructure requirements such as databases, environment variables, services, or deployment topology?"},
		{Name: "design-user-experience", Reviewer: "ion@skyvalley.ac", Prompt: "Does this change alter the design system or user experience?"},
		{Name: "copy-commercial-impact", Reviewer: "iris@skyvalley.ac", Prompt: "Does this change alter copywriting or create commercial impact?"},
		{Name: "prompts-models", Reviewer: "jules@skyvalley.ac", Prompt: "Does this change alter prompts, model selection, or how LLMs are used?"},
	}
	if !reflect.DeepEqual(got.Concerns, wantConcerns) {
		t.Fatalf("concerns = %#v, want %#v", got.Concerns, wantConcerns)
	}
	if !reflect.DeepEqual(content.reads, []contentRead{
		{repoID: "repo_cove", content: accepted, path: ".gitrdone/purpose.md"},
		{repoID: "repo_cove", content: accepted, path: ".gitrdone/priorities.md"},
	}) {
		t.Fatalf("governing reads = %#v", content.reads)
	}
	if content.base != accepted || content.candidate != candidate {
		t.Fatalf("comparison = %#v to %#v, want %#v to %#v", content.base, content.candidate, accepted, candidate)
	}
}

func TestRepositoryAssessmentInputSourceRejectsMalformedAcceptedPrioritiesBeforeComparing(t *testing.T) {
	content := &recordingRepositoryContent{files: map[string]string{
		"aaaaaaaa:.gitrdone/purpose.md":    covePurpose,
		"aaaaaaaa:.gitrdone/priorities.md": "## architecture\nReviewer: noam@skyvalley.ac\n",
	}}
	source, err := judgement.NewRepositoryAssessmentInputSource(content)
	if err != nil {
		t.Fatalf("new repository assessment input source: %v", err)
	}
	_, err = source.Load(context.Background(), "repo_cove", intent.ConcernAssessmentContext{
		Version:         intent.Version{Content: intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}},
		GoverningIntent: intent.Revision{Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}},
	})
	if err == nil {
		t.Fatal("load malformed priorities succeeded")
	}
	if content.compares != 0 {
		t.Fatalf("content comparisons = %d, want none before valid governing guidance", content.compares)
	}
}

type contentRead struct {
	repoID  string
	content intent.ContentRef
	path    string
}

type recordingRepositoryContent struct {
	files      map[string]string
	reads      []contentRead
	difference string
	base       intent.ContentRef
	candidate  intent.ContentRef
	compares   int
}

func (content *recordingRepositoryContent) ReadText(_ context.Context, repoID string, ref intent.ContentRef, path string) (string, error) {
	content.reads = append(content.reads, contentRead{repoID: repoID, content: ref, path: path})
	value, found := content.files[ref.Revision+":"+path]
	if !found {
		return "", errors.New("file not found")
	}
	return value, nil
}

func (content *recordingRepositoryContent) Compare(_ context.Context, _ string, base, candidate intent.ContentRef) (string, error) {
	content.compares++
	content.base = base
	content.candidate = candidate
	return content.difference, nil
}
