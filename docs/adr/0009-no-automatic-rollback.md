# 0009. No automatic rollback

- Status: Accepted
- Date: 2026-11-19

## Context

When `wait_healthy` fails, the new tag is in Git and Argo CD has (at least partly) applied it. deployctl could immediately commit the previous tag back. Many deploy tools do.

But "not healthy within five minutes" has several causes:

- the new code is broken (rollback helps),
- the cluster is short of capacity, or a node is draining (rollback does not help, and churns more pods),
- a dependency (database, another service) is down (rollback does not help),
- the timeout is too short for this app (rollback undoes a good deploy),
- the previous version is also broken by the same external cause.

An automatic rollback also creates a second change nobody asked for, in production possibly without approval, at the moment people are trying to understand what is happening.

## Decision

deployctl **never rolls back automatically**. A failed `wait_healthy` fails the request, records diagnostics (events, log excerpts, Argo CD health message), and tells the user how to roll back. A rollback is a separate, explicit request, recorded and (in production) approved like any other change.

## Consequences

- Every change to production has a human decision behind it.
- Failure output must be good enough to decide quickly; this is why `wait_healthy` captures diagnostics and the CLI prints the exact rollback command.
- Mean time to recovery depends on someone noticing. Alerts on failed prod requests ([operations.md](../reference/operations.md#alerts)) are therefore part of the design, not an add-on.
- In production, a rollback needs an approver. For a real incident, approvers must be reachable. This is a known cost of the two-person rule.

## Alternatives considered

- **Automatic rollback on failure.** Rejected for the reasons above.
- **Automatic rollback in non-prod only.** Tempting, but it hides broken builds in dev, which is where you want to see them.
- **Progressive delivery (Argo Rollouts with analysis).** The right tool for automated, metric-driven rollback, and compatible with deployctl: deployctl changes the tag, Argo Rollouts decides how to roll it out. Out of scope for deployctl itself.
- **Expedited rollback without approval.** Could be added later as an explicit, audited break-glass flow for admins. Not in v1.
