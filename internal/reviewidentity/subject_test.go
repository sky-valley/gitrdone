package reviewidentity_test

import (
	"testing"

	"github.com/sky-valley/gitrdone/internal/reviewidentity"
)

func TestCanonicalSubjectMatchesDurableReviewAuthority(t *testing.T) {
	got, valid := reviewidentity.Canonical(" Noam+GitRDone@Company.Example ")
	if !valid || got != "noam+gitrdone@company.example" {
		t.Fatalf("canonical subject = %q, %t; want noam+gitrdone@company.example, true", got, valid)
	}
	for _, invalid := range []string{"", "reviewer-job", "Noam <noam@company.example>"} {
		if got, valid := reviewidentity.Canonical(invalid); valid || got != "" {
			t.Fatalf("canonical invalid subject %q = %q, %t; want empty, false", invalid, got, valid)
		}
	}
}
