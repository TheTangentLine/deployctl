# 0002. Persist intent before side effects

- Status: Accepted
- Date: 2026-11-19

## Context

A deploy involves several external systems: GitHub (push), Argo CD (refresh, then minutes of reconciliation), Kubernetes (pods becoming ready). Any of them can be slow or fail, and the process doing the work can crash at any point.

If the API handled a deploy synchronously (push, wait for health, respond), then:

- the HTTP request would last minutes and break on any network blip between CLI and API,
- a crash after the push but before recording it would leave a change in Git that deployctl does not know about,
- there would be no place to pause for an approval that may take hours.

## Decision

The API does only fast, local work: authenticate, authorise, validate the body, and **in one database transaction** insert the request, its steps and an audit event. It then returns `202 Accepted` with the request ID.

All side effects happen later, in the worker, driven by rows in Postgres. Each step's start and result are written to the database before the next step begins.

## Consequences

- Nothing is ever "half-applied" from deployctl's point of view: either the intent is recorded and will be carried out (or fail visibly), or it was never accepted.
- A worker crash resumes from the last recorded step. Combined with idempotent steps ([0008](0008-idempotency-at-api-and-step-level.md)), re-running is safe.
- Approvals are just a step that waits; no process is blocked while a human decides.
- The CLI needs a second channel for progress, which is the SSE stream ([0010](0010-sse-snapshots-for-progress.md)).
- Errors after acceptance are reported through the request's status, not the HTTP response. Clients must follow the request to know the outcome.
- The API and worker can be scaled and deployed independently.

## Alternatives considered

- **Synchronous API.** Rejected for the reasons in the context.
- **Fire-and-forget goroutine in the API.** Rejected: lost on crash, no durability, and the API would need write credentials.
- **Workflow engine (Temporal, Argo Workflows).** Gives durable execution and retries out of the box. Rejected for now: a large operational dependency for six steps. The step model is deliberately similar, so migrating later is possible.
