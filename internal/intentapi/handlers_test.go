package intentapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
	"github.com/sky-valley/gitrdone/internal/intentapi"
	"github.com/sky-valley/gitrdone/internal/intentservice"
)

func TestNativeIntentAPIAdmitsAndImmediatelyPromotesAProposal(t *testing.T) {
	repository, projection := newRepository(t)
	initial := repository.CurrentIntent()
	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))

	requestBody := []byte(`{
		"baseIntent":"` + string(initial.ID) + `",
		"contentRef":{"engine":"git","revision":"bbbbbbbb"}
	}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/proposals", bytes.NewReader(requestBody))
	request.SetPathValue("repoID", "repo_123")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "request-1")
	recorder := httptest.NewRecorder()

	handlers.AdmitProposal.ServeHTTP(recorder, intentapi.WithAuthenticatedProducer(request, "control-api"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var receipt struct {
		Change struct {
			ID string `json:"id"`
		} `json:"change"`
		Version struct {
			ID         string `json:"id"`
			ChangeID   string `json:"change"`
			BaseIntent string `json:"baseIntent"`
			Producer   string `json:"producer"`
			ContentRef struct {
				Engine   string `json:"engine"`
				Revision string `json:"revision"`
			} `json:"contentRef"`
		} `json:"version"`
		State     string `json:"state"`
		Promotion *struct {
			ID         string `json:"id"`
			FromIntent string `json:"fromIntent"`
			ToIntent   string `json:"toIntent"`
			Version    string `json:"version"`
		} `json:"promotion"`
	}
	decodeResponse(t, recorder, &receipt)
	if receipt.Change.ID == "" || receipt.Version.ID == "" {
		t.Fatalf("receipt identities are empty: %#v", receipt)
	}
	if receipt.Version.ChangeID != receipt.Change.ID {
		t.Fatalf("version change = %q, want %q", receipt.Version.ChangeID, receipt.Change.ID)
	}
	if receipt.Version.BaseIntent != string(initial.ID) {
		t.Fatalf("base intent = %q, want %q", receipt.Version.BaseIntent, initial.ID)
	}
	if receipt.Version.Producer != "control-api" {
		t.Fatalf("producer = %q, want control-api", receipt.Version.Producer)
	}
	if receipt.Version.ContentRef.Engine != "git" || receipt.Version.ContentRef.Revision != "bbbbbbbb" {
		t.Fatalf("content ref = %#v, want git:bbbbbbbb", receipt.Version.ContentRef)
	}
	if receipt.State != "admitted" {
		t.Fatalf("state = %q, want admitted", receipt.State)
	}
	if receipt.Promotion == nil || receipt.Promotion.ID == "" {
		t.Fatalf("promotion = %#v, want completed promotion", receipt.Promotion)
	}
	if receipt.Promotion.Version != receipt.Version.ID || receipt.Promotion.FromIntent != string(initial.ID) {
		t.Fatalf("promotion = %#v, want version %q from %q", receipt.Promotion, receipt.Version.ID, initial.ID)
	}
	if got := repository.CurrentIntent(); string(got.ID) != receipt.Promotion.ToIntent || got.Content.Revision != "bbbbbbbb" {
		t.Fatalf("current intent = %#v, want promoted receipt", got)
	}
	if len(projection.advances) != 1 {
		t.Fatalf("projection advances = %d, want 1", len(projection.advances))
	}

	retry := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/proposals", bytes.NewReader(requestBody))
	retry.SetPathValue("repoID", "repo_123")
	retry.Header.Set("Content-Type", "application/json")
	retry.Header.Set("Idempotency-Key", "request-1")
	retryRecorder := httptest.NewRecorder()
	handlers.AdmitProposal.ServeHTTP(retryRecorder, intentapi.WithAuthenticatedProducer(retry, "control-api"))
	if retryRecorder.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200: %s", retryRecorder.Code, retryRecorder.Body.String())
	}
	var retried map[string]any
	decodeResponse(t, retryRecorder, &retried)
	var first map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first receipt: %v", err)
	}
	if !mapsEqual(first, retried) {
		t.Fatalf("retry receipt = %#v, want %#v", retried, first)
	}
	if len(projection.advances) != 1 {
		t.Fatalf("projection advances after retry = %d, want 1", len(projection.advances))
	}
}

func TestNativeIntentAPIKeepsAdmissionSuccessSeparateFromPromotion(t *testing.T) {
	repository, _ := newRepository(t)
	stale := repository.CurrentIntent()
	first, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "advance-first",
		BaseIntent:     stale.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "setup",
	})
	if err != nil {
		t.Fatalf("propose setup change: %v", err)
	}
	if _, err := repository.Promote(context.Background(), intent.PromoteRequest{VersionID: first.Version.ID, ExpectedIntent: stale.ID}); err != nil {
		t.Fatalf("promote setup change: %v", err)
	}

	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))
	request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/proposals", bytes.NewBufferString(`{
		"baseIntent":"`+string(stale.ID)+`",
		"contentRef":{"engine":"git","revision":"cccccccc"}
	}`))
	request.SetPathValue("repoID", "repo_123")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "stale-proposal")
	recorder := httptest.NewRecorder()

	handlers.AdmitProposal.ServeHTTP(recorder, intentapi.WithAuthenticatedProducer(request, "control-api"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var receipt struct {
		State     string          `json:"state"`
		Promotion json.RawMessage `json:"promotion"`
	}
	decodeResponse(t, recorder, &receipt)
	if receipt.State != "admitted" {
		t.Fatalf("state = %q, want admitted", receipt.State)
	}
	if len(receipt.Promotion) != 0 && !bytes.Equal(receipt.Promotion, []byte("null")) {
		t.Fatalf("promotion = %s, want absent or null", receipt.Promotion)
	}
}

func TestNativeIntentAPIReturnsDurableAdmissionWhenPromotionDecisionFails(t *testing.T) {
	repository, _ := newRepository(t)
	baseIntent := repository.CurrentIntent()
	handlers := intentapi.NewHandlers(intentservice.NewWithPromotionDecider(staticResolver{repository: repository}, failingDecider{}))
	request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/proposals", bytes.NewBufferString(`{
		"baseIntent":"`+string(baseIntent.ID)+`",
		"contentRef":{"engine":"git","revision":"bbbbbbbb"}
	}`))
	request.SetPathValue("repoID", "repo_123")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "decision-failure")
	recorder := httptest.NewRecorder()

	handlers.AdmitProposal.ServeHTTP(recorder, intentapi.WithAuthenticatedProducer(request, "ion"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want admitted 200: %s", recorder.Code, recorder.Body.String())
	}
	var receipt struct {
		State   string `json:"state"`
		Version struct {
			ID string `json:"id"`
		} `json:"version"`
		Promotion json.RawMessage `json:"promotion"`
	}
	decodeResponse(t, recorder, &receipt)
	if receipt.State != "admitted" || receipt.Version.ID == "" {
		t.Fatalf("receipt = %#v, want durable admission", receipt)
	}
	if len(receipt.Promotion) != 0 && !bytes.Equal(receipt.Promotion, []byte("null")) {
		t.Fatalf("promotion = %s, want absent or null", receipt.Promotion)
	}
	if got := repository.CurrentIntent(); got != baseIntent {
		t.Fatalf("current intent = %#v, want unchanged %#v", got, baseIntent)
	}
}

func TestNativeIntentAPIAdmitsAnExplicitVersionDependency(t *testing.T) {
	repository, _ := newRepository(t)
	baseIntent := repository.CurrentIntent()
	parent, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "parent",
		BaseIntent:     baseIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "ion",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))
	request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/proposals", bytes.NewBufferString(`{
		"baseIntent":"`+string(baseIntent.ID)+`",
		"contentRef":{"engine":"git","revision":"cccccccc"},
		"dependencies":["`+string(parent.Version.ID)+`"]
	}`))
	request.SetPathValue("repoID", "repo_123")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "dependent")
	recorder := httptest.NewRecorder()

	handlers.AdmitProposal.ServeHTTP(recorder, intentapi.WithAuthenticatedProducer(request, "ion"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var receipt struct {
		Version struct {
			Dependencies []string `json:"dependencies"`
		} `json:"version"`
		Promotion json.RawMessage `json:"promotion"`
	}
	decodeResponse(t, recorder, &receipt)
	if len(receipt.Version.Dependencies) != 1 || receipt.Version.Dependencies[0] != string(parent.Version.ID) {
		t.Fatalf("dependencies = %q, want [%q]", receipt.Version.Dependencies, parent.Version.ID)
	}
	if len(receipt.Promotion) != 0 && !bytes.Equal(receipt.Promotion, []byte("null")) {
		t.Fatalf("promotion = %s, want absent or null", receipt.Promotion)
	}
}

func TestNativeIntentAPIReadsIntentChangeAndBoundedVersions(t *testing.T) {
	repository, _ := newRepository(t)
	initial := repository.CurrentIntent()
	proposed, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "inspect",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "control-api",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))

	intentRequest := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/intent", nil)
	intentRequest.SetPathValue("repoID", "repo_123")
	intentRecorder := httptest.NewRecorder()
	handlers.CurrentIntent.ServeHTTP(intentRecorder, intentRequest)
	if intentRecorder.Code != http.StatusOK {
		t.Fatalf("intent status = %d, want 200: %s", intentRecorder.Code, intentRecorder.Body.String())
	}
	var current struct {
		ID         string `json:"id"`
		ContentRef struct {
			Engine   string `json:"engine"`
			Revision string `json:"revision"`
		} `json:"contentRef"`
	}
	decodeResponse(t, intentRecorder, &current)
	if current.ID != string(initial.ID) || current.ContentRef.Engine != "git" || current.ContentRef.Revision != "aaaaaaaa" {
		t.Fatalf("current intent = %#v, want %#v", current, initial)
	}

	changeRequest := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/changes/"+string(proposed.Change.ID), nil)
	changeRequest.SetPathValue("repoID", "repo_123")
	changeRequest.SetPathValue("changeID", string(proposed.Change.ID))
	changeRecorder := httptest.NewRecorder()
	handlers.GetChange.ServeHTTP(changeRecorder, changeRequest)
	if changeRecorder.Code != http.StatusOK {
		t.Fatalf("change status = %d, want 200: %s", changeRecorder.Code, changeRecorder.Body.String())
	}
	var change struct {
		ID            string `json:"id"`
		LatestVersion struct {
			ID string `json:"id"`
		} `json:"latestVersion"`
	}
	decodeResponse(t, changeRecorder, &change)
	if change.ID != string(proposed.Change.ID) {
		t.Fatalf("change id = %q, want %q", change.ID, proposed.Change.ID)
	}
	if change.LatestVersion.ID != string(proposed.Version.ID) {
		t.Fatalf("latest version id = %q, want %q", change.LatestVersion.ID, proposed.Version.ID)
	}

	versionsRequest := httptest.NewRequest(http.MethodGet, "/v1/repos/repo_123/changes/"+string(proposed.Change.ID)+"/versions?limit=1", nil)
	versionsRequest.SetPathValue("repoID", "repo_123")
	versionsRequest.SetPathValue("changeID", string(proposed.Change.ID))
	versionsRecorder := httptest.NewRecorder()
	handlers.ListVersions.ServeHTTP(versionsRecorder, versionsRequest)
	if versionsRecorder.Code != http.StatusOK {
		t.Fatalf("versions status = %d, want 200: %s", versionsRecorder.Code, versionsRecorder.Body.String())
	}
	var page struct {
		Versions []struct {
			ID string `json:"id"`
		} `json:"versions"`
		NextCursor string `json:"nextCursor"`
	}
	decodeResponse(t, versionsRecorder, &page)
	if len(page.Versions) != 1 || page.Versions[0].ID != string(proposed.Version.ID) {
		t.Fatalf("versions = %#v, want version %q", page.Versions, proposed.Version.ID)
	}
	if page.NextCursor != "" {
		t.Fatalf("next cursor = %q, want empty", page.NextCursor)
	}
}

func TestNativeIntentAPIRejectsSpoofedProducerAndUnsafeRetries(t *testing.T) {
	repository, _ := newRepository(t)
	baseIntent := repository.CurrentIntent().ID
	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))

	tests := []struct {
		name   string
		header string
		body   string
		status int
	}{
		{
			name:   "missing idempotency key",
			body:   `{"baseIntent":"` + string(baseIntent) + `","contentRef":{"engine":"git","revision":"bbbbbbbb"}}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "producer cannot be supplied by caller",
			header: "spoof-attempt",
			body:   `{"baseIntent":"` + string(baseIntent) + `","contentRef":{"engine":"git","revision":"bbbbbbbb"},"producer":"ion"}`,
			status: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/proposals", bytes.NewBufferString(test.body))
			request.SetPathValue("repoID", "repo_123")
			request.Header.Set("Content-Type", "application/json")
			if test.header != "" {
				request.Header.Set("Idempotency-Key", test.header)
			}
			recorder := httptest.NewRecorder()
			handlers.AdmitProposal.ServeHTTP(recorder, intentapi.WithAuthenticatedProducer(request, "control-api"))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}

	propose := func(revision string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/proposals", bytes.NewBufferString(`{"baseIntent":"`+string(baseIntent)+`","contentRef":{"engine":"git","revision":"`+revision+`"}}`))
		request.SetPathValue("repoID", "repo_123")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "same-key")
		recorder := httptest.NewRecorder()
		handlers.AdmitProposal.ServeHTTP(recorder, intentapi.WithAuthenticatedProducer(request, "control-api"))
		return recorder
	}
	if first := propose("bbbbbbbb"); first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200: %s", first.Code, first.Body.String())
	}
	if conflict := propose("cccccccc"); conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409: %s", conflict.Code, conflict.Body.String())
	}
}

func TestNativeIntentAPIReturnsUnprocessableForContentTheEngineCannotAdmit(t *testing.T) {
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewRepository(initial, rejectingAdmission{err: intent.ErrContentNotAdmissible}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	handlers := intentapi.NewHandlers(intentservice.New(staticResolver{repository: repository}))
	request := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_123/proposals", bytes.NewBufferString(`{
		"baseIntent":"`+string(repository.CurrentIntent().ID)+`",
		"contentRef":{"engine":"jj","revision":"not-in-this-engine"}
	}`))
	request.SetPathValue("repoID", "repo_123")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "wrong-engine")
	recorder := httptest.NewRecorder()

	handlers.AdmitProposal.ServeHTTP(recorder, intentapi.WithAuthenticatedProducer(request, "control-api"))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", recorder.Code, recorder.Body.String())
	}
}

func newRepository(t *testing.T) (*intent.Repository, *recordingProjection) {
	t.Helper()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initial}
	repository, err := intent.NewRepository(initial, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return repository, projection
}

type staticResolver struct {
	repository *intent.Repository
}

func (resolver staticResolver) Resolve(context.Context, string) (intentservice.Repository, error) {
	return resolver.repository, nil
}

func (resolver staticResolver) Bootstrap(_ context.Context, _ string, content intent.ContentRef) (intent.Revision, error) {
	current := resolver.repository.CurrentIntent()
	if current.Content != content {
		return intent.Revision{}, intentservice.ErrRepositoryAlreadyInitialized
	}
	return current, nil
}

type acceptingAdmission struct{}

func (acceptingAdmission) Admit(context.Context, intent.VersionID, intent.ContentRef) error {
	return nil
}

type rejectingAdmission struct {
	err error
}

type failingDecider struct{}

func (failingDecider) DecidePromotion(context.Context, intentservice.JudgementSubject) (intentservice.PromotionDecision, error) {
	return "", errors.New("promotion decision unavailable")
}

func (admission rejectingAdmission) Admit(context.Context, intent.VersionID, intent.ContentRef) error {
	return admission.err
}

type recordingProjection struct {
	current  intent.ContentRef
	advances []intent.ContentRef
}

func (projection *recordingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *recordingProjection) Advance(_ context.Context, _ intent.ContentRef, next intent.ContentRef) error {
	projection.current = next
	projection.advances = append(projection.advances, next)
	return nil
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v: %s", err, recorder.Body.String())
	}
}

func mapsEqual(left, right map[string]any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
