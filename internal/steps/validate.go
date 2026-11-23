package steps

import (
	"context"
	"errors"
	"fmt"
)

// Validate works out what a request will change, and whether it makes
// sense, without changing anything.
//
//   - from: the value in Git now (tip of the tracked branch)
//   - to:   the tag for deploy, the replica count for scale, or the last
//     successfully deployed tag that differs from "from" for rollback
//
// If from == to there is nothing to do: the outcome is NoChange and the
// request ends as succeeded. An unhealthy or out-of-sync app adds a
// warning, not a failure, because deploying a fix to a broken app must be
// possible.
//
// Errors from the clients are returned as they are, so a retryable Git or
// Argo CD error is retried. Problems with the request itself (no earlier
// deployment to roll back to, autoscaled app) are permanent.
type Validate struct {
	Git     GitRepo
	Argo    ArgoClient
	Kube    KubeReader // needed for scale
	History History    // needed for rollback
}

var _ Step = Validate{}

func (Validate) Name() string { return NameValidate }

func (v Validate) Run(ctx context.Context, in Input) (Result, error) {
	req := in.Request
	t := req.Target

	ref, _, err := valueRef(req)
	if err != nil {
		return Result{}, err
	}

	// A fixed replica count and an HPA fight each other.
	if req.Kind == KindScale {
		if v.Kube == nil {
			return Result{}, errors.New("validate: no Kubernetes reader configured")
		}
		autoscaled, err := v.Kube.IsAutoscaled(ctx, t.Namespace, t.Selector)
		if err != nil {
			return Result{}, err
		}
		if autoscaled {
			return Result{}, fmt.Errorf("%s/%s is autoscaled by an HPA: change the HPA's min and max replicas instead", req.App, req.Environment)
		}
	}

	from, err := v.Git.GetValue(ctx, ref)
	if err != nil {
		return Result{}, err
	}

	to, err := v.target(ctx, req, from)
	if err != nil {
		return Result{}, err
	}

	out := in.Outputs
	out.From, out.To = from, to

	if from == to {
		return Result{Outcome: NoChange, Outputs: out, Message: "already " + to}, nil
	}

	state, err := v.Argo.Get(ctx, t.ArgoApp, false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Result{}, fmt.Errorf("Argo CD application %s: %w", t.ArgoApp, err)
		}
		return Result{}, err
	}
	out.Warnings = append(out.Warnings, warnings(state)...)

	return Result{Outcome: Succeeded, Outputs: out, Message: from + " → " + to}, nil
}

// target returns the value the request will set.
func (v Validate) target(ctx context.Context, req Request, from string) (string, error) {
	if req.Kind != KindRollback {
		return requestedValue(req)
	}
	if v.History == nil {
		return "", errors.New("validate: no deployment history configured")
	}
	tag, found, err := v.History.LastGoodTag(ctx, req.App, req.Environment, from)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("no earlier successful deployment of %s/%s to roll back to", req.App, req.Environment)
	}
	return tag, nil
}

// warnings explains anything about the app's current state an approver or
// requester should know before going ahead.
func warnings(s AppState) []string {
	var w []string
	switch s.Health {
	case "Degraded", "Missing":
		msg := "app is " + s.Health + " before this change"
		if s.HealthMessage != "" {
			msg += ": " + s.HealthMessage
		}
		w = append(w, msg)
	}
	if s.SyncStatus == "OutOfSync" {
		w = append(w, "app is OutOfSync with Git before this change (someone may have changed the cluster by hand)")
	}
	return w
}
