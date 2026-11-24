package steps_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
	"github.com/TheTangentLine/deployctl/internal/steps/fakes"
)

// Full 40-character SHAs, as git_commit records them.
const (
	oldSHA   = "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432"
	ourSHA   = "3c2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d"
	laterSHA = "77aa88bb99cc00dd11ee22ff33aa44bb55cc66dd"
)

// pushed is the input after git_commit: From, To and CommitSHA set.
func pushed(req steps.Request, from, to string) steps.Input {
	return steps.Input{Request: req, Attempt: 1, Outputs: steps.Outputs{From: from, To: to, CommitSHA: ourSHA}}
}

func argoWith(states ...steps.AppState) *fakes.Argo {
	return &fakes.Argo{States: map[string][]steps.AppState{devTarget.ArgoApp: states}, Err: map[string]error{}}
}

func TestArgoRefresh(t *testing.T) {
	in := pushed(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b")
	tests := []struct {
		name        string
		state       steps.AppState
		wantOutcome steps.Outcome
		wantSyncs   []string
	}{
		{
			name:        "automated sync: refresh only",
			state:       steps.AppState{AutoSync: true},
			wantOutcome: steps.Succeeded,
		},
		{
			name:        "manual sync: requests a sync of our commit",
			state:       steps.AppState{AutoSync: false},
			wantOutcome: steps.Succeeded,
			wantSyncs:   []string{"reconcile-dev@" + ourSHA},
		},
		{
			name:        "manual sync of another revision in progress: still requests ours",
			state:       steps.AppState{Operation: &steps.SyncOperationState{Revision: oldSHA, Phase: "Succeeded"}},
			wantOutcome: steps.Succeeded,
			wantSyncs:   []string{"reconcile-dev@" + ourSHA},
		},
	}
	for _, phase := range []string{"Pending", "Running", "Succeeded"} {
		tests = append(tests, struct {
			name        string
			state       steps.AppState
			wantOutcome steps.Outcome
			wantSyncs   []string
		}{
			name:        "manual sync of our commit already " + phase + ": no second sync",
			state:       steps.AppState{Operation: &steps.SyncOperationState{Revision: ourSHA, Phase: phase}},
			wantOutcome: steps.Skipped,
		})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argo := argoWith(tt.state)
			res, err := steps.ArgoRefresh{Argo: argo}.Run(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %s, want %s (%s)", res.Outcome, tt.wantOutcome, res.Message)
			}
			if argo.Refreshes[devTarget.ArgoApp] != 1 {
				t.Fatalf("refreshes = %d, want 1", argo.Refreshes[devTarget.ArgoApp])
			}
			if len(argo.Syncs) != len(tt.wantSyncs) || (len(tt.wantSyncs) > 0 && argo.Syncs[0] != tt.wantSyncs[0]) {
				t.Fatalf("syncs = %v, want %v", argo.Syncs, tt.wantSyncs)
			}
			if res.Outputs.CommitSHA != ourSHA {
				t.Fatal("outputs not passed through")
			}
		})
	}
}

func TestArgoRefresh_Errors(t *testing.T) {
	in := pushed(deployReq("e4f5a6b"), "a1b2c3d", "e4f5a6b")

	t.Run("app missing", func(t *testing.T) {
		argo := &fakes.Argo{States: map[string][]steps.AppState{}}
		_, err := steps.ArgoRefresh{Argo: argo}.Run(context.Background(), in)
		if !errors.Is(err, steps.ErrNotFound) {
			t.Fatalf("got %v, want not found", err)
		}
	})
	t.Run("Argo CD down is retried", func(t *testing.T) {
		argo := argoWith(steps.AppState{AutoSync: true})
		argo.Err["Get"] = steps.Retryable(io.EOF)
		_, err := steps.ArgoRefresh{Argo: argo}.Run(context.Background(), in)
		if !steps.IsRetryable(err) {
			t.Fatalf("got %v, want retryable", err)
		}
	})
	t.Run("sync busy is retried", func(t *testing.T) {
		argo := argoWith(steps.AppState{})
		argo.Err["Sync"] = steps.Retryable(errors.New("another operation is already in progress"))
		_, err := steps.ArgoRefresh{Argo: argo}.Run(context.Background(), in)
		if !steps.IsRetryable(err) {
			t.Fatalf("got %v, want retryable", err)
		}
	})
	t.Run("no commit", func(t *testing.T) {
		in := in
		in.Outputs.CommitSHA = ""
		_, err := steps.ArgoRefresh{Argo: argoWith(steps.AppState{AutoSync: true})}.Run(context.Background(), in)
		if err == nil {
			t.Fatal("expected an error")
		}
	})
}
