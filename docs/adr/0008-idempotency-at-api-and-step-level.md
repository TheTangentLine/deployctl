# 0008. Idempotency at the API and at each step

- Status: Accepted
- Date: 2026-11-19

## Context

There are two places where "the same thing happens twice" can go wrong:

1. **Client → API.** The CLI sends `POST /v1/deployments`, the network drops before the response arrives, the CLI (or a CI job) retries. Without protection, two requests are created. (The one-active rule would turn the second into a `409`, which is confusing for a retry.)
2. **Worker → external systems.** A worker pushes a commit, then crashes before writing "git_commit succeeded" to the database. Another worker reclaims the step. Without protection, it pushes a second commit.

Window 2 cannot be closed with database transactions, because the push is outside the database.

## Decision

**API level.** Mutating endpoints accept an `Idempotency-Key` header. The server stores `(user_id, key)` with a SHA-256 of the body and the response, for 24 hours. Same key and body → stored response. Same key, different body → `422`. Same key while the first is in flight → `409` with `Retry-After`.

**Step level.** Every step checks the current state before acting and does nothing if the target state is already reached. `git_commit` additionally:

- writes a trailer `Deployctl-Request: <request_id>` into each commit,
- on every run, first searches recent history on the branch for that trailer, and if found, records that commit and succeeds without pushing.

`finalize` relies on a unique constraint on `deployments.request_id`.

## Consequences

- Clients can retry blindly. The CLI generates a key per invocation; CI can derive one from the pipeline run ID.
- The worker can be killed at any instant without risking duplicate commits.
- Git history becomes self-describing: any commit can be traced back to a request.
- The trailer search depends on history not being rewritten. A force-push that removes the commit makes the retry re-apply the change, which is the correct outcome anyway.
- Searching history costs a `git log` over the last 200 commits per `git_commit` run, which is cheap with a cached clone.

## Alternatives considered

- **Two-phase write (record "about to push", then push, then record "pushed").** Does not help: after a crash between push and the second record, the worker still cannot tell whether the push happened without looking at Git, which is what the trailer search does.
- **Compare the value only.** Sufficient in most cases, but cannot distinguish "we already did this" from "someone else set the same value"; the trailer gives an exact answer and the commit SHA for `wait_healthy`.
- **No API idempotency, rely on the 409.** Rejected: a retry would get an error pointing at its own request.
