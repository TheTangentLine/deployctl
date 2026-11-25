package steps

import (
	"context"
	"errors"
	"fmt"
)

// ArgoRefresh asks Argo CD to look at Git now, instead of at its next poll
// (every 3 minutes by default).
//
// With automated sync (the expected setup), Argo CD then applies the new
// commit by itself. Without it, the step also requests a sync of the commit
// git_commit pushed, unless a sync of that commit is already pending,
// running or done, so a re-run after a crash does not queue a second one.
//
// Nothing here is a side effect that can go wrong if repeated: a refresh is
// a read, and the sync is skipped when already requested.
type ArgoRefresh struct {
	Argo ArgoClient
}

var _ Step = ArgoRefresh{}

func (ArgoRefresh) Name() string { return NameArgoRefresh }

func (a ArgoRefresh) Run(ctx context.Context, in Input) (Result, error) {
	app := in.Request.Target.ArgoApp
	sha := in.Outputs.CommitSHA
	if sha == "" {
		return Result{}, errors.New("argocd_refresh: no commit; git_commit must run first")
	}

	state, err := a.Argo.Get(ctx, app, true)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Result{}, fmt.Errorf("Argo CD application %s: %w", app, err)
		}
		return Result{}, err
	}
	if state.AutoSync {
		return Result{Outcome: Succeeded, Outputs: in.Outputs, Message: "refreshed " + app}, nil
	}

	if op := state.Operation; op != nil && op.Revision == sha {
		switch op.Phase {
		case "Pending", "Running", "Succeeded":
			return Result{Outcome: Skipped, Outputs: in.Outputs,
				Message: "sync of " + shortSHA(sha) + " already " + op.Phase}, nil
		}
	}
	if err := a.Argo.Sync(ctx, app, sha); err != nil {
		return Result{}, err
	}
	return Result{Outcome: Succeeded, Outputs: in.Outputs, Message: "sync of " + shortSHA(sha) + " requested"}, nil
}
