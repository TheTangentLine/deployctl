# 0003. Postgres as the job queue

- Status: Accepted
- Date: 2026-11-19

## Context

Given [0002](0002-persist-intent-before-side-effects.md), the worker needs a queue of steps to run. The expected load is tens to hundreds of requests per day, each with five or six steps. Requirements:

- a step is never run by two workers at the same time,
- a step held by a crashed worker is picked up again,
- creating a request and enqueuing its first step is atomic with the audit event,
- the state of the queue is inspectable with SQL.

## Decision

Use the `steps` table in Postgres as the queue.

- Workers claim with `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1` inside a short `UPDATE` statement, setting `claimed_by`, `attempt` and `lease_expires_at`.
- The row lock is released immediately; ownership during execution is the **lease** (30 seconds, extended by a heartbeat every 10 seconds).
- Steps whose lease has expired are claimable again.
- Every completion is **fenced**: `WHERE claimed_by = $me AND attempt = $n`, so a stale worker cannot overwrite a newer attempt.
- Retry backoff uses a `run_after` column instead of sleeping.
- `LISTEN/NOTIFY` (`step_ready`) wakes idle workers; a 2-second poll is the fallback.

## Consequences

- One stateful dependency instead of two. Enqueueing is part of the same transaction as the business write, so there is no dual-write problem (no "row committed but message not sent").
- The queue is visible and debuggable with ordinary SQL.
- Throughput is far beyond what is needed; `SKIP LOCKED` is used in production queues handling thousands of jobs per second.
- We own lease and retry logic (a few hundred lines) and must test it carefully. The store tests in [development.md](../reference/development.md#testing) exist for this.
- Fencing protects database state, not external side effects; steps must still be idempotent.
- If load grew by orders of magnitude, a dedicated queue might be needed. Not expected for this tool.

## Alternatives considered

- **Holding the row lock for the whole step.** Simpler (no leases), but a 5-minute `wait_healthy` would keep a transaction open for 5 minutes per step, which ties up connections and blocks vacuum. Rejected.
- **Redis, RabbitMQ, NATS, SQS.** Rejected: extra infrastructure, and the transactional coupling with request creation would need an outbox pattern anyway.
- **A library such as River (Go, Postgres-backed).** A reasonable choice that implements the same idea. Worth evaluating during M1; this ADR's decision is about Postgres being the queue, not about hand-rolling it.
