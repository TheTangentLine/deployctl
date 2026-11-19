# Roadmap

Build in this order so there is something working at every stage. Each milestone lists what "done" means, so progress is checkable.

## M0: Steps and clients

Git, Argo CD and Kubernetes clients, the step implementations and the YAML editor, tested with fakes. No server yet.

Done when:

- [ ] `internal/git` can clone, read a value, edit it with the node API, commit with trailers, push, and find a commit by trailer, against a local bare repo.
- [ ] YAML golden tests pass, including comments, indentation and numeric-looking tags.
- [ ] `internal/argocd` reads sync status, revision and health, and refreshes.
- [ ] `internal/kube` lists pods by selector, reads events and log tails.
- [ ] Every step has table tests with fakes, including a re-run after "crash".

## M1: API and worker

Postgres schema, request and step storage, worker claiming, the deploy pipeline end to end, in dev auth mode.

Done when:

- [ ] `deployctl migrate` applies the schema in [data-model.md](data-model.md).
- [ ] `POST /v1/deployments` stores a request and returns `202`; the second one for the same app/env returns `409`.
- [ ] The worker claims with `SKIP LOCKED`, heartbeats, and a killed worker's step is reclaimed.
- [ ] A dev deploy runs through to `succeeded` against kind + Argo CD.
- [ ] `GET /v1/requests/{id}/events` streams snapshots.

## M2: Auth

OIDC verification, role mapping and the central policy.

Done when:

- [ ] The API verifies tokens from the Keycloak in `docker-compose.yaml`.
- [ ] Groups map to roles; `/v1/me` shows them.
- [ ] The policy table test covers every role × action × environment type.
- [ ] Dev auth refuses non-loopback binds.

## M3: Approvals and audit

Production gate, the no-self-approval rule, the audit log and its endpoint.

Done when:

- [ ] Prod requests stop at `awaiting_approval`; approve and reject work.
- [ ] Self-approval is refused by the policy and by the database constraint.
- [ ] Every state change writes an audit event in the same transaction.
- [ ] `deployctl_app` cannot modify `audit_events` (store test).

## M4: CLI client

`login`, SSE progress, and all commands, with `status` reading live state.

Done when:

- [ ] `deployctl login` completes PKCE with a loopback redirect and stores tokens in the keychain.
- [ ] `deploy` renders live progress and diagnostics on failure; exit codes match [cli.md](cli.md#output-and-exit-codes).
- [ ] `status` shows Git, Argo CD and pod state side by side.

## M5: Scale and rollback

The remaining request kinds and the deployment history.

Done when:

- [ ] `scale` edits `replicasKey`, refuses when an HPA targets the workload, and waits for the right ready count.
- [ ] `finalize` records `deployments`; `rollback` picks the last good tag that differs from Git.
- [ ] A failed deploy followed by a rollback ends with the previous tag running.

## M6: Hardening

Idempotency keys, rate limiting, metrics, integration tests in CI.

Done when:

- [ ] Idempotency keys behave as specified, including in-flight and mismatched-body cases.
- [ ] Per-user rate limiting on mutating endpoints.
- [ ] Metrics from [operations.md](operations.md#metrics) exported; dashboard and alerts defined.
- [ ] End-to-end tests run on kind in CI.

## Later

- **Pull request mode** for protected branches: `git_commit` opens a PR and `wait_merged` waits for it. Approval moves to code review for teams that prefer it.
- **Tag-exists check** against the registry in `validate`.
- **Approval expiry**, configurable per environment.
- **Per-app permissions** (team-scoped developers and approvers).
- **Notifications:** Slack or webhook on approval requested, request failed, request succeeded in prod.
- **Kustomize support:** edit `images:` in `kustomization.yaml` instead of Helm values.
- **Deploy by digest** rather than tag.
- **A small web UI** on the same API.
