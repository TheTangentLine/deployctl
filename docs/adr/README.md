# Architecture Decision Records

An ADR records one significant decision: the situation, what was decided, and what it costs. They are short on purpose. When a decision changes, a new ADR **supersedes** the old one; old ADRs are never edited except to update their status.

| # | Decision | Status |
|---|---|---|
| [0001](0001-git-is-the-only-write-path.md) | Git is the only write path to the cluster | Accepted |
| [0002](0002-persist-intent-before-side-effects.md) | Persist intent before side effects | Accepted |
| [0003](0003-postgres-as-the-job-queue.md) | Postgres as the job queue | Accepted |
| [0004](0004-one-binary-three-modes.md) | One binary, separate processes | Accepted |
| [0005](0005-identity-from-oidc-roles-from-groups.md) | Identity from OIDC, roles from group claims | Accepted |
| [0006](0006-github-app-for-git-writes.md) | A GitHub App for Git writes | Accepted |
| [0007](0007-one-active-request-per-app-and-environment.md) | One active request per app and environment | Accepted |
| [0008](0008-idempotency-at-api-and-step-level.md) | Idempotency at the API and at each step | Accepted |
| [0009](0009-no-automatic-rollback.md) | No automatic rollback | Accepted |
| [0010](0010-sse-snapshots-for-progress.md) | Server-Sent Events with full snapshots for progress | Proposed |
| [0011](0011-edit-yaml-with-the-node-api.md) | Edit YAML with the node API | Accepted |
| [0012](0012-direct-push-before-pull-request-mode.md) | Push directly first, pull request mode later | Proposed |

**Accepted** means the decision is part of the design in the README and reference docs. **Proposed** means it fills a gap the original design left open and needs review.

## Template

Copy this for a new ADR. Number sequentially; never reuse a number.

```markdown
# NNNN. Title in the imperative or as a statement

- Status: Proposed | Accepted | Superseded by [NNNN](NNNN-....md)
- Date: YYYY-MM-DD

## Context

What situation forces a decision? What constraints apply? Keep it factual.

## Decision

What we will do, stated plainly.

## Consequences

What becomes easier, what becomes harder, what we now have to do.

## Alternatives considered

Each option with the reason it was not chosen.
```
