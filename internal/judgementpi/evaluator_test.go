package judgementpi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/judgement"
	"github.com/sky-valley/pi/ai"
)

func TestEvaluatorUsesPiAnthropicTransportAndParsesStrictConcernResult(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("x-api-key"); got != "test-anthropic-key" {
			t.Errorf("x-api-key = %q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		if err := json.Unmarshal(body, &requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(writer, anthropicResponse(`{"requiresReview":true,"reason":"adds a database requirement","evidence":["internal/store.ts","DATABASE_URL"]}`))
	}))
	defer server.Close()

	evaluator := &Evaluator{
		apiKey: "test-anthropic-key",
		model: &ai.Model{
			ID: "claude-test", Name: "Claude Test", Api: ai.APIAnthropicMessages, Provider: "anthropic",
			BaseURL: server.URL, Input: []string{"text"}, MaxTokens: 4096,
		},
		httpClient: server.Client(),
	}
	result, err := evaluator.Evaluate(context.Background(), judgement.ConcernRequest{
		RepoID:          "repo_cove",
		Version:         intent.Version{ID: "version_b", Content: intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}},
		GoverningIntent: intent.Revision{ID: "intent_a", Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}},
		Concern: judgement.Concern{
			Name:     "architecture-data-infrastructure",
			Reviewer: "noam@skyvalley.ac",
			Prompt:   "Does this change alter architecture, data models, or infrastructure requirements?",
		},
		Purpose:        "calm workplace chat",
		Priorities:     "review architecture changes",
		ChangeEvidence: "M internal/store.ts\n+DATABASE_URL",
	})
	if err != nil {
		t.Fatalf("evaluate concern: %v", err)
	}
	if !result.RequiresReview || result.Reason != "adds a database requirement" || len(result.Evidence) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Provenance != (intent.EvaluatorProvenance{
		Evaluator:        "pi+anthropic://claude-test",
		ContractRevision: "gitrdone.concern-assessment/v1",
	}) {
		t.Fatalf("provenance = %#v", result.Provenance)
	}
	encoded, _ := json.Marshal(requestBody)
	wire := string(encoded)
	for _, want := range []string{"calm workplace chat", "review architecture changes", "DATABASE_URL", "architecture-data-infrastructure"} {
		if !strings.Contains(wire, want) {
			t.Fatalf("Anthropic request missing %q: %s", want, wire)
		}
	}
	if strings.Contains(wire, "test-anthropic-key") {
		t.Fatal("Anthropic API key leaked into model payload")
	}
}

func TestEvaluatorRejectsNonJSONModelOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(writer, anthropicResponse("probably fine"))
	}))
	defer server.Close()
	evaluator := &Evaluator{
		apiKey:     "test-key",
		model:      &ai.Model{ID: "claude-test", Name: "Claude Test", Api: ai.APIAnthropicMessages, Provider: "anthropic", BaseURL: server.URL, Input: []string{"text"}, MaxTokens: 4096},
		httpClient: server.Client(),
	}
	_, err := evaluator.Evaluate(context.Background(), judgement.ConcernRequest{
		Concern:        judgement.Concern{Name: "architecture", Prompt: "Does architecture change?", Reviewer: "noam@skyvalley.ac"},
		Purpose:        "purpose",
		Priorities:     "priorities",
		ChangeEvidence: "diff",
	})
	if err == nil {
		t.Fatal("non-JSON Anthropic output was accepted")
	}
}

func TestNewEvaluatorRequiresPublishedAnthropicModelAndKey(t *testing.T) {
	if _, err := NewEvaluator("", "claude-sonnet-5"); err == nil {
		t.Fatal("empty Anthropic key was accepted")
	}
	if _, err := NewEvaluator("test-key", "not-a-model"); err == nil {
		t.Fatal("unknown Anthropic model was accepted")
	}
	evaluator, err := NewEvaluator("test-key", "claude-sonnet-5")
	if err != nil {
		t.Fatalf("new Claude Sonnet 5 evaluator: %v", err)
	}
	if evaluator.model.Provider != "anthropic" || evaluator.model.ID != "claude-sonnet-5" {
		t.Fatalf("model = %#v", evaluator.model)
	}
}

func anthropicResponse(text string) string {
	return "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + mustJSON(text) + "}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":20}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
}

func mustJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
