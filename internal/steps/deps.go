package steps

import "context"

// --------------------------- Git ---------------------------

// ValueRef points at one value inside a values file in the config repo.
type ValueRef struct {
	Repo   string // https://github.com/example/config.git
	Branch string // main
	File   string // charts/reconcile/values-prod.yaml
	Key    string // backend.image.tag
}

type ValueKind int

const (
	ValueString ValueKind = iota // image tags: "1.10" must stay a string
	ValueInt                     // replica counts
)

type CommitMessage struct {
	Subject     string // deploy(backend/prod): 9f8e7d6 -> e4f5a6b
	RequestedBy string
	ApprovedBy  string // empty if no approval
	RequestID   string // written as the Deployctl-Request trailer
}

type GitRepo interface {
	// GetValue reads the value at ref on the tip of the branch.
	GetValue(ctx context.Context, ref ValueRef) (string, error)

	// SetValue makes the value at ref equal value, commits and pushes.
	// Returns changed=false (and no commit) if it was already equal.
	// Handles "someone pushed first" internally by re-reading and re-applying.
	SetValue(ctx context.Context, ref ValueRef, value string, kind ValueKind, msg CommitMessage) (sha string, changed bool, err error)

	// FindRequestCommit looks for a recent commit on the branch with the
	// Deployctl-Request trailer for requestID.
	FindRequestCommit(ctx context.Context, repo, branch, requestID string) (sha string, found bool, err error)
}

// --------------------------- Argo CD ---------------------------

type AppState struct {
	SyncStatus    string              // Synced, OutOfSync, Unknown
	SyncRevision  string              // Git revision used for the latest comparison
	Health        string              // Healthy, Progressing, Degraded, Suspended, Missing, Unknown
	HealthMessage string              // e.g. "Deployment exceeded its progress deadline"
	AutoSync      bool                // whether automated sync is enabled
	Operation     *SyncOperationState // pending, active or latest sync; nil if unknown
}

// SyncOperationState describes a sync operation independently of app health.
type SyncOperationState struct {
	Revision string // resolved Git SHA, or requested revision if unresolved (may be empty)
	Phase    string // Pending, Running, Terminating, Succeeded, Failed, Error
	Message  string // operation details or failure reason
}

type ArgoClient interface {
	// Get returns a snapshot of the application. refresh=true asks Argo CD
	// to re-read Git first.
	Get(ctx context.Context, app string, refresh bool) (AppState, error)

	// GetDesiredImages returns desired container images for a Deployment at a Git revision.
	GetDesiredImages(ctx context.Context, app, revision, namespace, selector string) (map[string]string, error)

	// Sync asks Argo CD to apply the given revision when automated sync is disabled.
	Sync(ctx context.Context, app, revision string) error
}

// --------------------------- Kubernetes (read-only) ---------------------------

type Pod struct {
	Name     string
	Ready    bool
	Phase    string // Pending, Running, ...
	Reason   string // CrashLoopBackOff, ImagePullBackOff, ... (empty if none)
	Restarts int32
	Images   []string // full image references of the pod's containers
}

type Event struct {
	Object  string // pod or ReplicaSet name
	Type    string // Normal, Warning
	Reason  string // BackOff, Failed, ...
	Message string
}

type KubeReader interface {
	Pods(ctx context.Context, namespace, selector string) ([]Pod, error)
	Events(ctx context.Context, namespace string, objects []string, limit int) ([]Event, error)
	Logs(ctx context.Context, namespace, pod, container string, previous bool, tailLines int64) (string, error)
	// IsAutoscaled reports whether an HPA targets the workload behind selector.
	// Only needed for scale (M5); can be added later.
	IsAutoscaled(ctx context.Context, namespace, selector string) (bool, error)
}

// --------------------------- Deployment history ---------------------------

// History is read by validate to pick a rollback target. It is backed by
// the deployments table (M1/M5); in M0 only the fake exists.
type History interface {
	// LastGoodTag returns the most recent successfully deployed tag for app
	// and environment that is not equal to exclude (the value Git has now).
	// found is false if there is no such deployment.
	LastGoodTag(ctx context.Context, app, environment, exclude string) (tag string, found bool, err error)
}
