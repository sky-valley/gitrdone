package requestauth_test

import (
	"net/http/httptest"
	"testing"

	"github.com/sky-valley/gitrdone/internal/requestauth"
)

func TestAuthenticatedSubjectComesFromDerivedRequestContext(t *testing.T) {
	request := httptest.NewRequest("GET", "https://git.example.com", nil)
	authenticated := requestauth.WithSubject(request, "  noam@company.example  ")

	if got := requestauth.Subject(authenticated); got != "noam@company.example" {
		t.Fatalf("subject = %q, want authenticated reviewer", got)
	}
	if got := requestauth.Subject(request); got != "" {
		t.Fatalf("original request subject = %q, want empty", got)
	}
}
