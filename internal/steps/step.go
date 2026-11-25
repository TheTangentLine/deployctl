package steps

import (
	"context"
	"time"
)

// Step is one unit of work inside a request: validate, git_commit,
// argocd_refresh, wait_healthy, ...
//
// Run must be idempotent: if the worker crashes and Run is called again
// with the same Input, it must reach the same outcome without repeating
// side effects (for example, without pushing a second commit).
//
// Run must not touch the database. It receives everything it needs in
// Input and returns everything it learned in Result; the worker stores it.
//
// When Run fails, it may still return a Result whose Outputs and Message
// explain the failure (wait_healthy puts its diagnostics there). The worker
// stores those Outputs with the failed step; the Outcome is ignored.
type Step interface {
	Name() string
	Run(ctx context.Context, in Input) (Result, error)
}

// Step names, as stored in the steps table.
const (
	NameValidate     = "validate"
	NameApprovalGate = "approval_gate"
	NameGitCommit    = "git_commit"
	NameArgoRefresh  = "argocd_refresh"
	NameWaitHealthy  = "wait_healthy"
	NameFinalize     = "finalize"
)

// Kind is what the request asks for.
type Kind string

const (
	KindDeploy   Kind = "deploy"
	KindScale    Kind = "scale"
	KindRollback Kind = "rollback"
)

// Target is the resolved configuration for one app and environment,
// copied into the request when it is created so a config reload cannot
// redirect a request in flight.
type Target struct {
	Repo             string // https://github.com/example/config.git
	Branch           string // main
	ValuesFile       string // charts/reconcile/values-prod.yaml
	ImageTagKey      string // backend.image.tag
	ReplicasKey      string // backend.replicaCount (empty: scale not supported)
	ArgoApp          string // reconcile-prod
	Namespace        string // demo-prod
	Selector         string // app=reconcile-backend
	RequiresApproval bool
}

// Request is the part of a stored request that steps need.
type Request struct {
	ID          string
	Kind        Kind
	App         string
	Environment string
	Tag         string // deploy only
	Replicas    int    // scale only
	RequestedBy string // email or display name, for commit trailers
	ApprovedBy  string // empty until approved, and for non-prod
	Target      Target
}

// Outputs accumulates what steps learn, in order. Each step receives the
// outputs of the steps before it and returns an updated copy.
type Outputs struct {
	From      string   // value in Git before the change (set by validate)
	To        string   // value the request will set (set by validate)
	Warnings  []string // shown to the user and the approver (set by validate)
	CommitSHA string   // commit that carries the change (set by git_commit)

	Diagnostics *Diagnostics // set by wait_healthy on failure
}

// Diagnostics is what wait_healthy captures when a rollout fails, so the
// user can see why without cluster access.
type Diagnostics struct {
	HealthMessage string
	Events        []Event
	Logs          map[string]string // pod/container -> last log lines
}

// Input is everything a step gets for one run.
type Input struct {
	Request Request
	Outputs Outputs

	// Attempt is 1 on the first run of this step and increases on every
	// retry or reclaim after a crash.
	Attempt int

	// FirstStartedAt is when this step first started, across attempts.
	// wait_healthy measures its timeout from here so a crash does not
	// reset the clock.
	FirstStartedAt time.Time

	// Progress, if set, reports a one-line status while the step runs,
	// for example "2/3 pods ready". Use in.Report, which is nil-safe.
	Progress func(message string)
}

// Report sends a progress message if a Progress callback is set.
func (in Input) Report(message string) {
	if in.Progress != nil {
		in.Progress(message)
	}
}

// Outcome is how a step finished, when it did not return an error.
type Outcome string

const (
	// Succeeded: the step did its work. Move on to the next step.
	Succeeded Outcome = "succeeded"
	// Skipped: the work was already done (for example, the value was
	// already set). Move on to the next step.
	Skipped Outcome = "skipped"
	// NoChange: there is nothing to do at all. The request ends as
	// succeeded with outcome no_change. Only validate returns this.
	NoChange Outcome = "no_change"
	// Waiting: park the request until an outside decision. Only
	// approval_gate returns this.
	Waiting Outcome = "waiting"
)

// Result is what a step returns. On success Outcome says how it finished;
// on failure (non-nil error) only Outputs and Message are meaningful.
type Result struct {
	Outcome Outcome
	Outputs Outputs // in.Outputs with this step's additions
	Message string  // one line for the CLI, e.g. "pushed 3c2d1e0"
}
