# Operations

> **Status:** design. How to run deployctl, what to watch, how it fails, and what to do about it. Metric names, alert thresholds and runbooks are proposals.

- [Deploying deployctl](#deploying-deployctl)
- [Upgrades](#upgrades)
- [Observability](#observability)
- [Alerts](#alerts)
- [Failure modes](#failure-modes)
- [Runbooks](#runbooks)
- [Credential rotation](#credential-rotation)
- [Backups and recovery](#backups-and-recovery)

---

## Deploying deployctl

deployctl runs in the cluster and is deployed by Argo CD from the config repo, like any other app. Its own changes go through pull requests, **not** through deployctl, so a broken deployctl can always be fixed.

| Workload | Replicas | Resources (start) | Notes |
|---|---|---|---|
| `deployctl-api` | 2 | 100m CPU, 128Mi | Behind an Ingress with TLS. PodDisruptionBudget `minAvailable: 1`. |
| `deployctl-worker` | 2 | 200m CPU, 256Mi + Git cache volume | No Service needed. PDB `minAvailable: 1`. |
| `deployctl-migrate` | Job | 50m CPU, 64Mi | Argo CD `PreSync` hook. |
| Postgres | managed | — | Postgres 15+. A managed service with point-in-time recovery is strongly preferred. |

Probes:

- API: liveness `GET /healthz`, readiness `GET /readyz`.
- Worker: liveness via a small HTTP server on `:8081/healthz` *(proposed)* that fails if the claim loop has not ticked in 60 seconds.

Graceful shutdown: on `SIGTERM` the worker stops claiming, lets running steps finish for up to 25 seconds (with `terminationGracePeriodSeconds: 30`), then exits. Steps that did not finish are picked up by another worker when their lease expires. The API stops accepting new connections, sends `event: end` with `{"reason":"shutdown"}` to SSE streams (the CLI reconnects), and exits.

## Upgrades

1. Merge the version bump to the config repo.
2. Argo CD runs the `PreSync` migration Job, then rolls API and worker pods.
3. During the rollout, old and new workers run together. Therefore:
   - migrations must be backward compatible (see [data-model.md](data-model.md#migrations)),
   - step names and `output` keys must be readable by both versions; a new step must be introduced in one release and used in the next.
4. Watch `deployctl_step_failures_total` and queue age for 15 minutes.

## Observability

### Logs

JSON via `slog`, one object per line. Every line from request handling or step execution carries:

| Field | Example |
|---|---|
| `request_id` (HTTP) | `01JD…` |
| `deploy_request_id` | `0b8e2c1a-…` |
| `step` | `git_commit` |
| `attempt` | `2` |
| `app`, `environment` | `backend`, `prod` |
| `user_id` | `…` |
| `trace_id` | `4bf9…` |

Grep for one deploy end to end: `deploy_request_id=0b8e2c1a`.

### Metrics

Prometheus on `:9090/metrics` *(proposed names)*:

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `deployctl_requests_total` | counter | `kind`, `environment`, `status` | Requests reaching a terminal state |
| `deployctl_request_duration_seconds` | histogram | `kind`, `environment` | Created to terminal, excluding approval wait |
| `deployctl_approval_wait_seconds` | histogram | `environment` | `awaiting_approval` to decision |
| `deployctl_step_duration_seconds` | histogram | `step`, `outcome` | Per step run |
| `deployctl_step_failures_total` | counter | `step`, `class` (`retryable`, `permanent`) | Step errors |
| `deployctl_queue_ready_steps` | gauge | — | Steps in `ready` with `run_after <= now()` |
| `deployctl_queue_oldest_ready_seconds` | gauge | — | Age of the oldest claimable step |
| `deployctl_lease_expirations_total` | counter | — | Steps reclaimed after a lease expired (a worker died or stalled) |
| `deployctl_git_push_retries_total` | counter | — | Non-fast-forward retries |
| `deployctl_sse_streams` | gauge | — | Open SSE connections |
| `deployctl_http_requests_total` | counter | `route`, `code` | API traffic |
| `deployctl_auth_failures_total` | counter | `reason` | Token verification failures |

App names are **not** metric labels, to keep cardinality bounded; use logs or the API for per-app questions. (Environment is fine: there are a handful.)

### Traces

OpenTelemetry. One trace per HTTP request, and one trace per step run, linked to the creating HTTP request's trace by a span link stored in `requests` *(proposed)*. Outbound calls to Postgres, GitHub, Argo CD and Kubernetes are child spans.

### Dashboards

A single dashboard is enough to start:

1. Requests per hour by status, split prod/non-prod.
2. p50 and p95 request duration by kind.
3. Queue: ready steps and oldest ready age.
4. Step failures by step and class.
5. Pending approvals and approval wait p95.
6. Lease expirations (should be near zero outside deploys of deployctl itself).

## Alerts

| Alert | Condition | Why |
|---|---|---|
| Queue stuck | `deployctl_queue_oldest_ready_seconds > 120` for 5m | No worker is claiming. Workers down, DB issue, or a bug. |
| Prod request failed | `increase(deployctl_requests_total{environment="prod",status="failed"}[5m]) > 0` | Someone's prod deploy needs attention. Route to the team channel, not a pager. |
| Lease churn | `rate(deployctl_lease_expirations_total[15m]) > 0.1` | Workers crashing or stalling repeatedly. |
| Auth failures spike | `rate(deployctl_auth_failures_total[5m]) > 1` | Misconfigured issuer, key rotation, or an attack. |
| Git push failures | `increase(deployctl_step_failures_total{step="git_commit",class="permanent"}[1h]) > 0` | Usually expired App key or branch protection change. |
| API down | `/readyz` failing on all pods for 2m | Standard. |

## Failure modes

| What breaks | What happens | What to do |
|---|---|---|
| Worker crashes mid-step | Lease expires within 30s; another worker reclaims and re-runs the step. Idempotency and the commit trailer prevent a duplicate commit. | Nothing, unless it repeats. Check `lease_expirations`. |
| All workers down | Requests are accepted and stay `pending`/`running`; nothing progresses. | Restore workers. Work resumes where it stopped. |
| Git push rejected (non-fast-forward) | `git_commit` re-fetches, re-applies the edit and pushes again, up to 5 times, then retries with backoff. | Nothing, unless `git_push_retries` is high (contention: consider sharding the config repo). |
| Branch protection blocks the push | `git_commit` fails permanently with the `GH006` message. | Add the GitHub App to the bypass list, or wait for pull request mode. |
| GitHub App key invalid or App uninstalled | `git_commit` fails permanently: "cannot mint installation token". | Fix the key or installation, then `deployctl retry`. |
| Argo CD unreachable | `validate`, `argocd_refresh` and `wait_healthy` retry with backoff, then fail. `status` shows an error for Argo CD fields. | Restore Argo CD, then retry. |
| Argo CD token expired | `403` from Argo CD: permanent failure. | Rotate token (below), then retry. |
| Rollout never becomes healthy | `wait_healthy` times out, records events and logs, request fails. The new tag stays in Git. | Read diagnostics. Fix forward with a new deploy, or `deployctl rollback`. |
| Image tag does not exist | Pods go to `ImagePullBackOff`; `wait_healthy` fails with that event. | Deploy the right tag or roll back. |
| Identity provider down | Existing tokens keep working until expiry (JWKS cached). New logins fail. | Wait or fix IdP. Requests already accepted continue. |
| Database unavailable | API returns `503`; workers stop claiming. Nothing is half-applied, because intent is stored before side effects. | Restore DB. Work resumes. |
| Two people deploy the same app/env | Second gets `409` with the first request's ID. | Wait, or cancel the first if it has not reached `git_commit`. |
| Someone edits the cluster by hand | Argo CD `selfHeal` reverts it. | Expected. Make changes through deployctl. |
| Someone pushes to the config repo by hand | Argo CD applies it; deployctl's next `validate` sees the new value as `from`. History in `deployments` does not include it. | Prevent with branch protection. |
| CI also commits to the config repo | Fine if it touches other keys. If it touches deployctl-managed keys, they fight. | Make CI call deployctl instead; make CI builds ignore config repo paths. |
| Config repo force-pushed | The trailer search may not find a commit that was rewritten away; a retried `git_commit` re-applies the change. Rollback history in the DB still holds. | Do not force-push the tracked branch. |

## Runbooks

### A request is stuck in `running`

1. `deployctl request <id>`: which step, which attempt, what message?
2. If the step is `wait_healthy`: it is waiting by design until `rolloutTimeout`. Check `deployctl status <app>` and the Argo CD UI.
3. If another step has been `running` for minutes: check `lease_expires_at`. If it keeps being extended, a worker is alive but blocked (check its logs by `deploy_request_id`). If it is in the past, no worker is reclaiming: check worker pods and the queue alert.
4. As a last resort an admin can mark the step failed so the request can be retried *(proposed: `deployctl admin fail-step <request> <step>`)*. Never edit rows by hand while workers are running.

### A request is stuck in `awaiting_approval`

Not a fault. `deployctl approvals` as an approver shows it. If nobody with `approver` exists apart from the requester, fix group membership in the identity provider.

### Worker cannot push

1. Check the error in `deployctl request <id>`.
2. `GH006 Protected branch update failed`: branch protection changed. Restore the App's bypass.
3. `401`/`Bad credentials` when minting a token: App private key rotated or App uninstalled. See [credential rotation](#credential-rotation).
4. After fixing, `deployctl retry <id>`. The trailer search guarantees no double commit.

### Argo CD says Synced, but deployctl keeps waiting

`wait_healthy` requires Argo CD's synced revision to contain the change: it is the request's commit, or a later commit whose rendered Deployment still has the new image tag. The message `waiting for Argo CD to pick up <sha>` means neither is true yet. Common causes:

- Argo CD tracks a different branch than `git.branch` in the config, so it never sees the commit. Compare `spec.source.targetRevision` with the config.
- A later commit changed the same key again (someone edited the values file by hand). The request's change is no longer what Git says, and the step times out. Check `git log` on the values file.
- The label selector or `imageTagKey` points at a different Deployment or container than the one the tag actually changes. See [configuration](configuration.md#field-reference).

### deployctl and Git disagree after a manual edit

Git wins; it is the desired state. deployctl reads Git fresh in every `validate`. The only thing that can be stale is `deployments` (rollback history), which only records deployctl's own deploys.

## Credential rotation

| Credential | How | Downtime |
|---|---|---|
| GitHub App private key | Generate a new key in App settings; update the Secret; restart workers; delete the old key. | None |
| Argo CD tokens | `argocd account generate-token --account deployctl-worker`; update Secret; restart; delete old token with `argocd account delete-token`. | None |
| Database passwords | Create new password (or use IAM auth); update Secret; restart; revoke old. | None with rolling restart |
| OIDC signing keys | Done in the identity provider. deployctl refetches JWKS on unknown `kid`. | None |

## Backups and recovery

- Postgres: daily snapshots and point-in-time recovery from the managed service.
- **What is lost if the database is lost:** request history, approvals, audit log, rollback history. **What is not lost:** the desired state of every app, which is in Git, and the running state, which is in the cluster. deployctl is never the only record of what is deployed.
- After a restore to an earlier point, requests that were `running` at the snapshot time will resume. Their steps are idempotent, so `git_commit` will find its trailer if the push happened after the snapshot, and `wait_healthy` will observe the current state.
