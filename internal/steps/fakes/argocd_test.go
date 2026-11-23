package fakes

import (
	"context"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

func TestArgoGetOperationSnapshotsAreIndependent(t *testing.T) {
	states := []steps.AppState{
		{},
		{Operation: &steps.SyncOperationState{Revision: "new-commit", Phase: "Pending"}},
		{Operation: &steps.SyncOperationState{Revision: "new-commit", Phase: "Running"}},
		{Operation: &steps.SyncOperationState{Revision: "new-commit", Phase: "Succeeded"}},
		{Operation: &steps.SyncOperationState{Revision: "older-commit", Phase: "Failed", Message: "sync failed"}},
	}
	argo := &Argo{States: map[string][]steps.AppState{"app": states}}

	// Read the last snapshot twice, as health polling does after the script ends.
	for i := 0; i <= len(states); i++ {
		want := states[min(i, len(states)-1)].Operation
		got, err := argo.Get(context.Background(), "app", false)
		if err != nil {
			t.Fatal(err)
		}
		if want == nil {
			if got.Operation != nil {
				t.Fatalf("snapshot %d: unexpected operation %+v", i, got.Operation)
			}
			continue
		}
		if got.Operation == nil || *got.Operation != *want {
			t.Fatalf("snapshot %d: got operation %+v, want %+v", i, got.Operation, want)
		}
		original := *want
		*got.Operation = steps.SyncOperationState{Revision: "changed", Phase: "Error", Message: "changed"}
		if *want != original {
			t.Fatalf("snapshot %d: caller changed the operation fixture", i)
		}
	}
}
