package steps

import (
	"context"
	"errors"
	"fmt"
)

// Finalize closes a request whose change is live and healthy. It runs
// only after wait_healthy succeeded.
//
// Steps never touch the database, so Finalize decides what to record and
// the worker records it, in the one transaction that completes the step
// and the request (outcome applied) and writes the audit event. For deploy
// and rollback it sets Outputs.Deployment: the row for the deployments
// table, which is where rollback looks for its target. Because it runs
// after wait_healthy, only tags that were proven healthy get there. Scale
// records no deployment; a replica count is not a rollback target.
//
// Run has no side effects and returns the same result every time, and the
// worker ignores a second insert for the same request, so a re-run after a
// crash records the deployment once.
type Finalize struct{}

var _ Step = Finalize{}

func (Finalize) Name() string { return NameFinalize }

func (Finalize) Run(ctx context.Context, in Input) (Result, error) {
	req := in.Request
	out := in.Outputs

	if out.CommitSHA == "" || out.To == "" {
		return Result{}, errors.New("finalize: no commit or target value; git_commit must run first")
	}

	var msg string
	switch req.Kind {
	case KindDeploy, KindRollback:
		out.Deployment = &Deployment{
			App:         req.App,
			Environment: req.Environment,
			Tag:         out.To,
			CommitSHA:   out.CommitSHA,
			RequestID:   req.ID,
		}
		verb := "deployed"
		if req.Kind == KindRollback {
			verb = "rolled back to"
		}
		msg = fmt.Sprintf("%s %s at %s", verb, out.To, shortSHA(out.CommitSHA))
	case KindScale:
		out.Deployment = nil
		msg = "scaled to " + out.To
	default:
		return Result{}, fmt.Errorf("finalize: unknown request kind %q", req.Kind)
	}
	return Result{Outcome: Succeeded, Outputs: out, Message: msg}, nil
}
