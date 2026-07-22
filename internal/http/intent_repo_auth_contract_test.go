package httpapi_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
)

func TestRepositoryPrincipalCanReadIntentAndProposeWithoutPromotionAuthority(t *testing.T) {
	world := newJourneyWorld(t)
	ion := world.cloneWorkspace("ion")
	base := world.currentIntent()
	proposedRevision := ion.commitFile("proposal.txt", "from Ion\n", "propose as Ion")
	requireGitSuccess(t, "publish Ion proposal", "-C", ion.path, "push", "origin", "HEAD:refs/candidates/auth-contract")

	res, body := request(t, world.server.handler, http.MethodGet, "/v1/repos/"+world.server.repo.ID+"/intent", repoBasicAuthorization(ion.token), "", "")
	requireStatus(t, res, body, http.StatusOK)

	proposalBody := fmt.Sprintf(`{"baseIntent":%q,"contentRef":{"engine":"git","revision":%q}}`, base.ID, proposedRevision)
	res, body = requestWithHeaders(t, world.server.handler, http.MethodPost, "/v1/repos/"+world.server.repo.ID+"/proposals", map[string]string{
		"Authorization":   repoBasicAuthorization(ion.token),
		"Content-Type":    "application/json",
		"Idempotency-Key": "repo-principal-proposal",
	}, proposalBody)
	requireStatus(t, res, body, http.StatusOK)
	var receipt struct {
		Version struct {
			Producer string `json:"producer"`
		} `json:"version"`
		Promotion *struct {
			ID string `json:"id"`
		} `json:"promotion"`
	}
	decodeJSON(t, res, body, &receipt)
	if receipt.Version.Producer != "ion" {
		t.Fatalf("proposal producer = %q, want ion", receipt.Version.Producer)
	}
	if receipt.Promotion == nil || receipt.Promotion.ID == "" {
		t.Fatal("approve-all judgement did not promote Ion's proposal")
	}

	readToken := createRepoTokenFixture(t, world.server.handler, world.server.repo.ID, "read", "reader")
	res, body = requestWithHeaders(t, world.server.handler, http.MethodPost, "/v1/repos/"+world.server.repo.ID+"/proposals", map[string]string{
		"Authorization":   repoBasicAuthorization(readToken.Token),
		"Content-Type":    "application/json",
		"Idempotency-Key": "read-only-proposal",
	}, proposalBody)
	requireStatus(t, res, body, http.StatusForbidden)
}

func repoBasicAuthorization(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}
