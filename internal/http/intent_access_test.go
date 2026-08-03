package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sky-valley/gitrdone/internal/requestauth"
)

func TestRepositoryAccessAuthCarriesReviewerAndControlSubjectsWithoutImpersonation(t *testing.T) {
	authorizer := &recordingRepoAccessAuthorizer{
		grant: repoAccessGrant{RepoID: "00000000-0000-4000-8000-000000000000", Subject: "noam@company.example"},
	}
	var subjects []string
	handler := repositoryAccessAuth("admin-token", authorizer, repoCapabilityReview, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		subjects = append(subjects, requestauth.Subject(request))
	}))

	reviewer := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_00000000-0000-4000-8000-000000000000/reviews", nil)
	reviewer.SetPathValue("repoID", "repo_00000000-0000-4000-8000-000000000000")
	reviewer.Header.Set("Authorization", "Bearer reviewer-token")
	handler.ServeHTTP(httptest.NewRecorder(), reviewer)
	if authorizer.input.Capability != repoCapabilityReview {
		t.Fatalf("authorized capability = %q, want review", authorizer.input.Capability)
	}

	control := httptest.NewRequest(http.MethodPost, "/v1/repos/repo_00000000-0000-4000-8000-000000000000/reviews", nil)
	control.SetPathValue("repoID", "repo_00000000-0000-4000-8000-000000000000")
	control.Header.Set("Authorization", "Bearer admin-token")
	handler.ServeHTTP(httptest.NewRecorder(), control)

	if len(subjects) != 2 || subjects[0] != "noam@company.example" || subjects[1] != "control-api" {
		t.Fatalf("authenticated subjects = %#v, want reviewer then distinct control-api", subjects)
	}
	if authorizer.calls != 1 {
		t.Fatalf("repo authorizer calls = %d, want reviewer token only", authorizer.calls)
	}
}

type recordingRepoAccessAuthorizer struct {
	input authorizeRepoAccessInput
	grant repoAccessGrant
	calls int
}

func (authorizer *recordingRepoAccessAuthorizer) AuthorizeRepoAccess(_ context.Context, input authorizeRepoAccessInput) (repoAccessGrant, error) {
	authorizer.calls++
	authorizer.input = input
	return authorizer.grant, nil
}
