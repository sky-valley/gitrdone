package intentapi

import (
	"testing"

	"github.com/sky-valley/gitrdone/internal/intent"
)

func TestMapReconciliationConflictDerivesSupersededState(t *testing.T) {
	response := mapReconciliationConflict(intent.ReconciliationConflictInspection{
		ReconciliationConflict: intent.ReconciliationConflict{
			ID:         "conflict_c",
			BaseIntent: "intent_old",
		},
		Superseded: true,
	})
	if response.State != "superseded" {
		t.Fatalf("state = %q, want superseded", response.State)
	}
}
