# 0010. Server-Sent Events with full snapshots for progress

- Status: Proposed
- Date: 2026-11-19

## Context

The CLI shows live step progress. Progress is produced by the worker, but the CLI talks only to the API, and the API and worker do not talk to each other ([0004](0004-one-binary-three-modes.md)). We need:

- a server-to-client stream over plain HTTPS that passes through ingress and corporate proxies,
- reconnection without missing the final state,
- no new infrastructure.

## Decision

- **Transport:** Server-Sent Events (`text/event-stream`) on `GET /v1/requests/{id}/events`.
- **Payload:** each event is a **full snapshot** of the request and its steps, with `id` set to the request's `version` counter. No deltas, no event log.
- **Worker → API signalling:** the worker issues `NOTIFY request_updated, '<id>'` in the same transaction as each state change. Each API process holds one `LISTEN` connection and wakes the matching streams, which re-read the snapshot. A 5-second re-read is a safety net for missed notifications.
- **End of stream:** `event: end` after the terminal snapshot. Keepalive comments every 15 seconds.

## Consequences

- Reconnection is trivial and lossless: the client reconnects and gets the current snapshot. `Last-Event-ID` needs no server-side history.
- Snapshots are small (six steps), so sending the whole thing is cheap.
- Intermediate states can be collapsed (version jumps from 7 to 9). Clients must not assume every transition is delivered. Anything that needs every transition uses the audit log.
- `NOTIFY` payloads are delivered only on commit, so clients never see rolled-back states.
- One `LISTEN` connection per API process is needed outside the normal pool.

## Alternatives considered

- **WebSockets.** Bidirectional, which is not needed; more proxy and auth friction. Rejected.
- **Client polling `GET /v1/requests/{id}`.** Simple and robust, and the CLI may fall back to it. Rejected as the primary mechanism only because it adds latency or load.
- **Delta events with a replay log (`request_events` table).** Needed only if clients must see every transition. Rejected for now as extra schema and retention for no current consumer.
