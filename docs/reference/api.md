# HTTP API

> **Status:** design. Version `v1`. Request and response shapes beyond the original README are proposals and may change before M1 ships.

- [Conventions](#conventions)
- [Errors](#errors)
- [Endpoints](#endpoints)
- [Resources](#resources)
- [Health](#health)
- [Identity](#identity)
- [Apps and status](#apps-and-status)
- [Creating requests](#creating-requests)
- [Reading requests](#reading-requests)
- [Request actions](#request-actions)
- [Approvals](#approvals)
- [Audit log](#audit-log)
- [Middleware](#middleware)

---

## Conventions

| Topic | Rule |
|---|---|
| Base path | `/v1` |
| Format | JSON, UTF-8. `Content-Type: application/json` required on bodies. |
| Auth | `Authorization: Bearer <jwt>` on everything except `/healthz` and `/readyz`. |
| IDs | UUIDv7 strings (time-ordered, so they sort by creation). The CLI accepts any unique prefix of 4+ characters and resolves it with `GET /v1/requests?id_prefix=`. |
| Times | RFC 3339 in UTC, for example `2026-11-19T10:14:31Z`. |
| Durations | Seconds as numbers (`"duration_s": 48.2`). |
| Field names | `snake_case`. |
| Pagination | Cursor-based: `?limit=50&cursor=<opaque>`. Responses include `next_cursor` (absent on the last page). `limit` max 200. |
| Idempotency | `Idempotency-Key` header on all `POST` endpoints that create or change something. See [request-lifecycle.md](request-lifecycle.md#idempotency). |
| Request ID | Every response carries `X-Request-Id` (generated or echoed from the request). Quote it when reporting problems. |
| Rate limits | Mutating endpoints: 30 per minute per user, burst 10 *(proposed)*. Exceeding returns `429` with `Retry-After`. Limits are per API process, so the effective limit is multiplied by the replica count. |

## Errors

Errors use [RFC 9457 problem details](https://www.rfc-editor.org/rfc/rfc9457) with `Content-Type: application/problem+json`.

```json
{
  "type": "urn:deployctl:problem:active-request-exists",
  "title": "Another request is active for this app and environment",
  "status": 409,
  "detail": "backend/prod already has request 0b8e2c1a in state awaiting_approval",
  "instance": "/v1/deployments",
  "active_request_id": "0b8e2c1a-5d7f-4a1e-9c3b-2f6e8d1a7b40"
}
```

| Status | `type` suffix | When |
|---|---|---|
| 400 | `invalid-body` | Malformed JSON, missing or invalid field. `errors[]` lists fields. |
| 400 | `unknown-app` / `unknown-environment` | Not in config. |
| 401 | `unauthenticated` | Missing, expired or invalid token. |
| 403 | `forbidden` | Authenticated but the policy denies. `missing_action` names the action. |
| 403 | `self-approval` | Deciding your own request's approval. |
| 404 | `not-found` | Resource does not exist (or you cannot see it). |
| 409 | `active-request-exists` | One-active-request rule. Includes `active_request_id`. |
| 409 | `invalid-state` | Retry from a non-failed state, cancel after `git_commit`, decide an approval that is not pending. |
| 409 | `superseded` | Retry of a request older than the newest one for that app and environment. |
| 409 | `idempotency-in-flight` | Same key, first request still processing. `Retry-After: 1`. |
| 422 | `idempotency-key-reused` | Same key with a different body. |
| 429 | `rate-limited` | Too many requests. |
| 503 | `unavailable` | Database unreachable, or a live read from Argo CD or Kubernetes failed. |

Errors never include stack traces or internal hostnames. Server errors (`500`) carry only the `X-Request-Id`.

## Endpoints

| Method | Path | Action required | Description |
|---|---|---|---|
| GET | `/healthz` | none | Liveness |
| GET | `/readyz` | none | Readiness |
| GET | `/v1/me` | any role | Current user and roles |
| GET | `/v1/apps` | `read` | Apps and environments |
| GET | `/v1/apps/{name}` | `read` | Live status per environment |
| POST | `/v1/deployments` | `deploy:nonprod` or `deploy:prod:request` | Request a deploy |
| POST | `/v1/scalings` | same | Request a replica change |
| POST | `/v1/rollbacks` | same | Request a rollback |
| GET | `/v1/requests` | `read` | List requests |
| GET | `/v1/requests/{id}` | `read` | Request with steps |
| GET | `/v1/requests/{id}/events` | `read` | SSE stream of snapshots |
| POST | `/v1/requests/{id}/retry` | `request:manage` | Retry a failed request |
| POST | `/v1/requests/{id}/cancel` | `request:manage` | Cancel before `git_commit` |
| GET | `/v1/approvals` | `read` | List approvals |
| POST | `/v1/approvals/{id}/decision` | `approval:decide` | Approve or reject |
| GET | `/v1/audit` | `audit:read` | Audit log |

## Resources

### Request

```json
{
  "id": "0b8e2c1a-5d7f-4a1e-9c3b-2f6e8d1a7b40",
  "kind": "deploy",
  "app": "backend",
  "environment": "prod",
  "params": { "tag": "e4f5a6b" },
  "status": "running",
  "outcome": null,
  "version": 7,
  "requested_by": { "id": "…", "name": "Linh Tran", "email": "linh@example.com" },
  "created_at": "2026-11-19T10:00:00Z",
  "updated_at": "2026-11-19T10:14:32Z",
  "finished_at": null,
  "from": "9f8e7d6",
  "to": "e4f5a6b",
  "commit_sha": "3c2d1e0f…",
  "warnings": [],
  "approval": { "id": "9c41…", "status": "approved", "decided_by": { "name": "Priya" }, "comment": "Checked the diff" },
  "steps": [
    { "name": "validate",       "status": "succeeded", "attempt": 1, "message": "9f8e7d6 → e4f5a6b", "started_at": "…", "finished_at": "…" },
    { "name": "approval_gate",  "status": "succeeded", "attempt": 1, "message": "approved by Priya" },
    { "name": "git_commit",     "status": "succeeded", "attempt": 1, "message": "pushed 3c2d1e0" },
    { "name": "argocd_refresh", "status": "succeeded", "attempt": 1 },
    { "name": "wait_healthy",   "status": "running",   "attempt": 1, "message": "3/5 pods ready" },
    { "name": "finalize",       "status": "blocked",   "attempt": 0 }
  ]
}
```

- `params` depends on `kind`: `{"tag"}` for deploy, `{"replicas"}` for scale, `{}` for rollback.
- `from` and `to` are filled in once `validate` has run.
- `outcome` is `applied` or `no_change` when `status` is `succeeded`, otherwise `null`.
- Failed steps include `error` (one line) and, for `wait_healthy`, `diagnostics` (events and log excerpts) when the caller may see them.

### App status

```json
{
  "name": "backend",
  "environments": [
    {
      "name": "dev",
      "requires_approval": false,
      "git":   { "tag": "a1b2c3d", "replicas": 3, "commit": "77aa…" },
      "argocd": { "application": "reconcile-dev", "sync": "Synced", "revision": "77aa…", "health": "Healthy" },
      "kube":  { "namespace": "demo-dev", "ready": 3, "desired": 3, "images": ["registry.example.com/reconcile-backend:a1b2c3d"] },
      "active_request_id": null,
      "last_deployment": { "tag": "a1b2c3d", "at": "2026-11-18T16:02:11Z", "by": "Sam" }
    }
  ]
}
```

Status is read live. If Argo CD or Kubernetes is unreachable, that sub-object is replaced by `{"error": "…"}` and the response is still `200`; a dashboard should show partial data rather than nothing.

## Health

- `GET /healthz` → `200 ok` while the process is running.
- `GET /readyz` → `200` if the database is reachable and the config is loaded; `503` otherwise. Argo CD and Kubernetes reachability are **not** part of readiness: if Argo CD is down, the API should still accept requests and serve history.

## Identity

### `GET /v1/me`

```json
{
  "id": "…",
  "subject": "f3c1…",
  "issuer": "https://keycloak.example.com/realms/deployctl",
  "name": "Linh Tran",
  "email": "linh@example.com",
  "groups": ["deployctl-developers"],
  "roles": ["developer", "viewer"],
  "machine": false
}
```

## Apps and status

### `GET /v1/apps`

```json
{ "apps": [ { "name": "backend", "environments": ["dev", "staging", "prod"] }, … ] }
```

### `GET /v1/apps/{name}`

Returns [App status](#app-status). `?env=prod` limits it to one environment.

## Creating requests

All three endpoints share behaviour: authenticate, check idempotency, validate the body, authorise, store, return `202`. See [request-lifecycle.md](request-lifecycle.md#creating-a-request).

### `POST /v1/deployments`

```http
POST /v1/deployments
Authorization: Bearer <jwt>
Idempotency-Key: 6f1c2a3e-8d4b-4e0a-9f17-1c2d3e4f5a6b
Content-Type: application/json

{ "app": "backend", "environment": "dev", "tag": "e4f5a6b", "reason": "Fix rounding in invoice totals" }
```

```http
HTTP/1.1 202 Accepted
Location: /v1/requests/0b8e2c1a-5d7f-4a1e-9c3b-2f6e8d1a7b40

{ "request_id": "0b8e2c1a-5d7f-4a1e-9c3b-2f6e8d1a7b40", "status": "pending" }
```

| Field | Required | Rules |
|---|---|---|
| `app` | yes | Must exist in config |
| `environment` | yes | Must exist for the app |
| `tag` | yes | `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$` |
| `reason` | no | Up to 500 characters. Shown to approvers and stored in the audit payload *(proposed)* |

`?dry_run=true` returns `200` with the plan instead:

```json
{ "dry_run": true, "app": "backend", "environment": "dev", "from": "a1b2c3d", "to": "e4f5a6b",
  "values_file": "charts/reconcile/values-dev.yaml", "key": "backend.image.tag",
  "requires_approval": false, "warnings": [] }
```

### `POST /v1/scalings`

```json
{ "app": "backend", "environment": "dev", "replicas": 5, "reason": "Load test" }
```

`replicas`: integer, `0` to the app's maximum (default 50 *(proposed)*). `0` is allowed and means "stop the app".

### `POST /v1/rollbacks`

```json
{ "app": "backend", "environment": "prod", "reason": "Error rate up after e4f5a6b" }
```

The target tag is resolved by `validate`, not by the caller. Use `dry_run=true` to see it first.

## Reading requests

### `GET /v1/requests`

Filters (all optional, combined with AND): `app`, `environment`, `status` (repeatable), `kind`, `requested_by` (user ID or `me`), `since`, `until`, `id_prefix`. Sorted newest first.

```json
{ "requests": [ { …request without steps… } ], "next_cursor": "eyJ0IjoiMjAyNi0xMS0xOVQx…" }
```

### `GET /v1/requests/{id}`

Returns the full [Request](#request) with steps.

### `GET /v1/requests/{id}/events`

`Accept: text/event-stream`. Sends `event: snapshot` with the full request on connect and on every change, a `: keepalive` comment every 15 seconds, and `event: end` once the request is terminal. See [request-lifecycle.md](request-lifecycle.md#progress-streaming).

```
event: snapshot
id: 7
data: {"id":"0b8e…","status":"running","version":7,"steps":[…]}

: keepalive

event: snapshot
id: 9
data: {"id":"0b8e…","status":"succeeded","outcome":"applied","version":9,"steps":[…]}

event: end
data: {}
```

Clients should treat each snapshot as replacing the previous one. Versions can skip numbers (several changes between two notifications are collapsed into one snapshot).

## Request actions

### `POST /v1/requests/{id}/retry`

No body. `200` with the updated request. Errors: `409 invalid-state` (not `failed`), `409 active-request-exists`, `409 superseded`, `403` (not yours and not admin).

### `POST /v1/requests/{id}/cancel`

Optional body `{ "reason": "…" }`. `200` with the updated request. `409 invalid-state` if `git_commit` has started or the request is already terminal.

## Approvals

### `GET /v1/approvals`

`?status=pending` (default) | `approved` | `rejected` | `cancelled`. With `status=pending`, the list excludes approvals the caller cannot decide (including their own requests), so it is a to-do list.

```json
{
  "approvals": [
    {
      "id": "9c41…",
      "status": "pending",
      "request": { "id": "0b8e…", "kind": "deploy", "app": "backend", "environment": "prod",
                   "from": "9f8e7d6", "to": "e4f5a6b", "reason": "Fix rounding in invoice totals",
                   "warnings": [], "requested_by": { "name": "Linh Tran" } },
      "created_at": "2026-11-19T10:00:02Z"
    }
  ]
}
```

### `POST /v1/approvals/{id}/decision`

```json
{ "decision": "approve", "comment": "Checked the diff, go ahead" }
```

`decision` is `approve` or `reject`. `comment` is optional for approve and **required** for reject *(proposed)*, so the requester knows why. Returns `200` with the approval. Errors: `403 self-approval`, `403 forbidden`, `409 invalid-state`.

## Audit log

### `GET /v1/audit`

Filters: `actor` (user ID), `app`, `environment`, `action` (for example `approval.approved`), `request_id`, `since`, `until`. Newest first, cursor-paginated.

```json
{
  "events": [
    {
      "id": 18233,
      "at": "2026-11-19T10:14:30Z",
      "actor": { "type": "user", "id": "…", "name": "Priya" },
      "action": "approval.approved",
      "resource": { "app": "backend", "environment": "prod", "request_id": "0b8e…" },
      "outcome": "success",
      "client": { "ip": "10.0.4.17", "user_agent": "deployctl/0.3.0" },
      "payload": { "comment": "Checked the diff, go ahead" }
    }
  ],
  "next_cursor": "…"
}
```

## Middleware

Order, outermost first:

1. **Request ID:** read `X-Request-Id` or generate one; put it in the context and response header.
2. **Structured logging (`slog`):** one line per request with method, route pattern (not raw path), status, duration, user ID, request ID. Redacts `Authorization`.
3. **OpenTelemetry tracing:** server span per request; trace context propagated to Postgres, Argo CD and Kubernetes calls.
4. **Panic recovery:** converts panics into `500` with the request ID and logs the stack.
5. **Authentication:** token verification, principal in context. Skipped for `/healthz` and `/readyz`.
6. **Rate limiting:** per-user token bucket on mutating methods.
7. **Handler:** body parsing, authorisation (needs the body), business logic.
