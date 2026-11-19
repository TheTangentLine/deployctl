# 0007. One active request per app and environment

- Status: Accepted
- Date: 2026-11-19

## Context

Two requests for the same app and environment at the same time would edit the same key in the same values file, and both would then watch the same rollout. Outcomes depend on timing: the second push may win silently, `wait_healthy` for the first may observe the second's pods, and the audit trail becomes confusing ("which request broke prod?").

## Decision

Allow at most **one active request** (`pending`, `awaiting_approval` or `running`) per `(app, environment)`. Enforce it with a partial unique index:

```sql
CREATE UNIQUE INDEX requests_one_active
    ON requests (app, environment)
    WHERE status IN ('pending', 'awaiting_approval', 'running');
```

A second request returns `409 Conflict` with the active request's ID. `failed` is **not** active, so a failed request does not block the app; retrying it must re-acquire the slot.

## Consequences

- Correct by construction: the database rejects the second insert, so there is no check-then-insert race in application code.
- "What is happening to backend/prod?" is always at most one row.
- A production request waiting for approval blocks other changes to that environment, including an urgent fix. The requester (or an admin) must cancel it first. This is a deliberate trade: queuing behind a pending approval would be surprising, and parallel requests would be worse.
- Different apps, or the same app in different environments, proceed in parallel.
- Retrying an old failed request after newer ones exist could revert newer changes, so retry checks for a newer request and returns `409 superseded`.

## Alternatives considered

- **Queue requests per app/env and run them in order.** More convenient, but a queued request's `from` value is unknown until it runs, approvals would be given for changes whose context may change, and a stuck head blocks everything behind it. Rejected for v1.
- **Last writer wins.** Rejected: unsafe and unauditable.
- **Application-level lock (advisory lock).** Works, but disappears on connection loss and is invisible in the data. The index is simpler and permanent.
