package steps

import (
	"context"
	"errors"
	"fmt"
)

// ApprovalGate holds a request that needs approval until a person decides.
// It runs only for environments with requiresApproval, between validate
// and git_commit.
//
// It does not create the approval itself, because steps never touch the
// database. It returns Waiting, and the worker then, in one transaction,
// creates the pending approval, marks the step waiting and the request
// awaiting_approval, and releases the step, so no worker is held while a
// person decides. The API's decision endpoint completes the step (approve)
// or fails it (reject); Run is not called for the decision.
//
// Run has no side effects, so a crash between Run and the worker storing
// its result is harmless: the next attempt returns the same answer. If the
// request already has an approver, the gate is open and Run succeeds.
type ApprovalGate struct{}

var _ Step = ApprovalGate{}

func (ApprovalGate) Name() string { return NameApprovalGate }

func (ApprovalGate) Run(ctx context.Context, in Input) (Result, error) {
	req := in.Request
	out := in.Outputs

	if out.To == "" {
		return Result{}, errors.New("approval_gate: no target value; validate must run first")
	}
	if !req.Target.RequiresApproval {
		return Result{}, fmt.Errorf("approval_gate: %s/%s does not require approval, so this step should not be part of the request", req.App, req.Environment)
	}
	if req.ApprovedBy != "" {
		return Result{Outcome: Succeeded, Outputs: out, Message: "approved by " + req.ApprovedBy}, nil
	}

	msg := "waiting for approval: " + out.From + " → " + out.To
	switch n := len(out.Warnings); n {
	case 0:
	case 1:
		msg += " (1 warning)"
	default:
		msg += fmt.Sprintf(" (%d warnings)", n)
	}
	return Result{Outcome: Waiting, Outputs: out, Message: msg}, nil
}
