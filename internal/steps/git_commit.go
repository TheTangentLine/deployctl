package steps

import (
	"context"
	"errors"
	"fmt"
)

// GitCommit makes Git say the new value. It is the only step that changes
// anything outside deployctl, and it must be safe to run again after a
// crash at any point:
//
//  1. If a commit with this request's trailer is already on the branch (a
//     previous attempt pushed, then crashed before recording it), record
//     that commit and stop. Nothing is pushed twice.
//  2. Otherwise set the value. If it is already set, the outcome is
//     Skipped and the commit is the current branch tip.
//  3. Otherwise SetValue commits and pushes, handling "someone pushed
//     first" itself.
//
// It relies on validate having stored From and To in the outputs.
type GitCommit struct {
	Git GitRepo
}

var _ Step = GitCommit{}

func (GitCommit) Name() string { return NameGitCommit }

func (g GitCommit) Run(ctx context.Context, in Input) (Result, error) {
	req := in.Request
	t := req.Target
	out := in.Outputs

	if out.To == "" {
		return Result{}, errors.New("git_commit: no target value; validate must run first")
	}
	// Defence in depth: the approval gate should make this impossible.
	if t.RequiresApproval && req.ApprovedBy == "" {
		return Result{}, fmt.Errorf("refusing to change %s/%s: it requires approval and the request is not approved", req.App, req.Environment)
	}

	ref, kind, err := valueRef(req)
	if err != nil {
		return Result{}, err
	}

	// 1. Did an earlier attempt already push?
	sha, found, err := g.Git.FindRequestCommit(ctx, t.Repo, t.Branch, req.ID)
	if err != nil {
		return Result{}, err
	}
	if found {
		out.CommitSHA = sha
		return Result{Outcome: Succeeded, Outputs: out, Message: "found earlier push " + shortSHA(sha)}, nil
	}

	// 2 and 3. Set the value.
	msg := CommitMessage{
		Subject:     fmt.Sprintf("%s(%s/%s): %s -> %s", req.Kind, req.App, req.Environment, out.From, out.To),
		RequestedBy: req.RequestedBy,
		ApprovedBy:  req.ApprovedBy,
		RequestID:   req.ID,
	}
	sha, changed, err := g.Git.SetValue(ctx, ref, out.To, kind, msg)
	if err != nil {
		return Result{}, err
	}
	out.CommitSHA = sha
	if !changed {
		return Result{Outcome: Skipped, Outputs: out, Message: "already " + out.To + " at " + shortSHA(sha)}, nil
	}
	return Result{Outcome: Succeeded, Outputs: out, Message: "pushed " + shortSHA(sha)}, nil
}
