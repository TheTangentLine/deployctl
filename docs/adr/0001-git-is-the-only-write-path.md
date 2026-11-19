# 0001. Git is the only write path to the cluster

- Status: Accepted
- Date: 2026-11-19

## Context

The clusters are managed by Argo CD from a config repo. Argo CD continuously compares the cluster to Git and, with `selfHeal` enabled, reverts anything that differs. A deploy tool could change the cluster in two ways:

1. Directly, through the Kubernetes API (`kubectl set image`, `kubectl scale`, patching a Deployment).
2. Indirectly, by committing to the config repo and letting Argo CD apply it.

Direct changes are faster and simpler to implement, but under GitOps they are either reverted by `selfHeal` within minutes, or, if `selfHeal` is off, they create drift between what Git says and what runs. Drift is the exact problem GitOps exists to remove.

## Decision

deployctl changes the cluster **only** by committing to the config repo. No deployctl component holds Kubernetes write credentials. Every mutating request kind (deploy, scale, rollback) is a commit. Rollback is a new forward commit, never a `git revert` of history or a force-push.

## Consequences

- Git history is a complete, independent record of every change deployctl made, with requester, approver and request ID in commit trailers.
- Argo CD remains the single actor that applies manifests. Its sync and health status are meaningful because nobody else touches the resources.
- The cluster credentials deployctl holds are read-only, so a compromised deployctl cannot directly break a cluster.
- Every change is slower than a direct API call by one commit, one push and one Argo CD reconciliation (seconds with `argocd_refresh`).
- There is no "emergency" path for scaling out under load through deployctl. Operators with cluster access can still act directly, and Argo CD will revert them unless they also update Git. This is intended: emergency changes should end up in Git anyway.
- deployctl depends on GitHub and Argo CD being available to make any change.

## Alternatives considered

- **Direct Kubernetes writes with `selfHeal` off for managed fields.** Rejected: creates drift, splits the source of truth, and requires deployctl to hold cluster write access.
- **Direct writes followed by a commit to Git.** Rejected: two writes that can disagree, and the cluster write still needs broad credentials.
- **Argo CD parameter overrides** (`argocd app set -p image.tag=...`). Rejected: overrides are stored on the Application object, not in Git, so Git no longer describes what runs.
