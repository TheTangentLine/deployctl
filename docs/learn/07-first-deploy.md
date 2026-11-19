# 7. The first deploy, step by step

*Tuesday, 11:00.*

Linh is ready. CI built `reconcile-backend:e4f5a6b` yesterday. Linh is logged in as a developer. Dev needs no approval.

```bash
$ deployctl deploy backend --env dev --tag e4f5a6b
✔ Request 0b8e created
✔ validate        a1b2c3d → e4f5a6b
✔ git_commit      pushed 3c2d1e0
✔ argocd_refresh
⏳ wait_healthy    2/3 pods ready
✔ Deployed in 48s
```

Forty-eight seconds. "Now," Sam says, "let's slow those 48 seconds down and look at every single thing that happened. If you understand this chapter, you understand deployctl."

## The shape of it

deployctl is three pieces: the **API**, the **worker**, and a **Postgres** database. They never talk to each other directly. The API writes to the database; the worker reads from it.

```mermaid
sequenceDiagram
    actor Linh
    participant CLI
    participant API
    participant DB as Postgres
    participant W as Worker
    participant Git as Config repo
    participant Argo as Argo CD
    participant K8s as Kubernetes

    Linh->>CLI: deploy backend --env dev --tag e4f5a6b
    CLI->>API: POST /v1/deployments
    API->>DB: save request + steps + audit (one transaction)
    API-->>CLI: 202 Accepted, request 0b8e
    CLI->>API: follow progress (SSE)
    W->>DB: claim "validate"
    W->>Git: read current tag
    W->>DB: validate done
    W->>DB: claim "git_commit"
    W->>Git: edit, commit, push
    W->>DB: git_commit done
    W->>Argo: refresh
    loop every 5 s
        W->>Argo: synced to our commit? healthy?
        W->>K8s: pods ready on new tag?
    end
    W->>DB: finalize: record deployment, succeeded
    API-->>CLI: progress updates along the way
```

## Second 0: the CLI sends a request

The CLI turns the command into an HTTP request:

```http
POST /v1/deployments HTTP/1.1
Host: deployctl.example.com
Authorization: Bearer eyJhbGciOiJSUzI1NiIs...
Idempotency-Key: 6f1c2a3e-8d4b-4e0a-9f17-1c2d3e4f5a6b
Content-Type: application/json

{ "app": "backend", "environment": "dev", "tag": "e4f5a6b" }
```

Two headers matter:

- `Authorization` carries Linh's access token from chapter 5.
- `Idempotency-Key` is a random ID the CLI generated for this one command. If the network drops and the CLI sends the same request again, the API recognises the key and returns the original answer instead of creating a second request. (Wednesday, this saves Linh.)

## Second 0: the API checks and records

The API runs through a fixed list, all in a few milliseconds:

1. **Who is this?** Verify the token's signature, issuer, audience and expiry. Linh, `developer`.
2. **Seen this key before?** No.
3. **Does the request make sense?** App `backend` exists in the config, it has an environment `dev`, and `e4f5a6b` is a valid tag format.
4. **Is Linh allowed?** `dev` has `requiresApproval: false`, so the action is `deploy:nonprod`. Developers may. Yes.
5. **Write it down.** In **one database transaction**, insert:
   - a row in `requests`: kind `deploy`, app `backend`, environment `dev`, tag `e4f5a6b`, status `pending`, requested by Linh, plus a snapshot of exactly which repo, file and key to change,
   - five rows in `steps`: `validate` (ready), then `git_commit`, `argocd_refresh`, `wait_healthy`, `finalize` (all blocked),
   - a row in `audit_events`: Linh created a request.
6. **Answer:** `202 Accepted`, request ID `0b8e2c1a-...`.

> **Aside: what's a transaction?** A database transaction groups several changes so they all happen or none do. If the server crashed between inserting the request and inserting the audit event, the transaction would be rolled back and *neither* would exist. You never get a request without its audit event, or steps without their request.

Notice what the API did **not** do: talk to Git, Argo CD or the cluster. It only wrote down the **intent**. The status code is `202 Accepted`, which in HTTP means "I've accepted this and will process it", not "it's done".

### Why not just do it right away?

Linh asks, "Why the indirection? The API could push to Git itself."

Sam gives three reasons.

1. **Credentials.** The API is the part exposed to the network. If it could push to Git, an attacker who broke into it could too. (Chapter 6.)
2. **Crashes.** A deploy takes up to minutes and touches three outside systems. If the process doing it crashes halfway, what state are we in? Because every step is written down *before* and *after* it runs, whoever picks the work up next knows exactly where it stopped. This principle is called **durable intent first, side effects second**.
3. **Waiting.** Production changes might wait hours for an approval. A request that's a row in a database can wait forever at no cost. An open HTTP connection can't.

### One at a time

There is one more thing in that insert. The database has a rule, a **partial unique index**, that allows at most one *active* request per app and environment. If Linh's teammate had a `backend`/`dev` request already running, the insert would have failed and Linh would have got `409 Conflict` with the other request's ID. That is the fix for the Friday merge-conflict disaster: two changes to the same thing simply cannot be in flight at once. The database enforces it, so there's no race where two API servers both check "is anything running?" at the same moment, both see "no", and both go ahead.

## Second 0.1: the CLI starts listening

The CLI immediately opens a second connection:

```http
GET /v1/requests/0b8e2c1a-.../events
Accept: text/event-stream
```

This is a **Server-Sent Events** (SSE) stream: an HTTP response that never quite ends, where the server writes a new message whenever something changes. Each message is a complete snapshot of the request:

```
event: snapshot
id: 3
data: {"status":"running","steps":[{"name":"validate","status":"succeeded","message":"a1b2c3d → e4f5a6b"},{"name":"git_commit","status":"running"}, ...]}
```

The CLI redraws its checklist from each snapshot. If the connection drops, the CLI reconnects and gets the latest snapshot. Nothing is lost, because each message contains everything.

How does the API know when something changed, if it never talks to the worker? The database tells it. Every time the worker updates a request, it also sends a Postgres notification (`NOTIFY request_updated`). The API is listening (`LISTEN request_updated`), re-reads the request, and pushes a snapshot down the stream.

## Second 0.2: a worker claims the first step

Two worker processes are running, each with a few goroutines, each in a loop: "is there a step ready to run? If so, take it."

"Take it" has to be careful. Two workers asking at the same moment must not both get the same step. The claim query uses a Postgres feature called `FOR UPDATE SKIP LOCKED`:

```sql
SELECT id FROM steps
WHERE status = 'ready' AND run_after <= now()
ORDER BY run_after
FOR UPDATE SKIP LOCKED
LIMIT 1;
```

Think of a kitchen with order tickets on a rail. `FOR UPDATE` means "I'm grabbing this ticket". `SKIP LOCKED` means "if someone else's hand is already on a ticket, skip it and grab the next one, don't wait for them." Two cooks never cook the same order, and neither stands around waiting.

Worker A gets `validate`. It marks the step `running`, records itself as the owner, and sets a **lease**: "I own this for 30 seconds". While it works, it renews the lease every 10 seconds. (On Wednesday, a worker will die mid-step, and the lease is what saves the day.)

## Second 0.3: `validate`

`validate` looks before anything is touched:

- **Git:** fetch the config repo, read `backend.image.tag` in `charts/reconcile/values-dev.yaml`. It says `a1b2c3d`. So the change is `a1b2c3d → e4f5a6b`.
- If it had already said `e4f5a6b`, the request would end here, successfully, with "already deployed". Not an error, just nothing to do.
- **Argo CD:** `reconcile-dev` is `Synced` and `Healthy`. Good. If it had been `Degraded`, deployctl would show a warning but carry on, because deploying a fix to something broken has to be possible.

Worker A then does one more transaction: mark `validate` succeeded, mark `git_commit` ready, move the request from `pending` to `running`, and notify. The CLI prints:

```
✔ validate        a1b2c3d → e4f5a6b
```

## Second 1: `git_commit`

Worker B happens to grab `git_commit`. It is the only step that changes anything outside deployctl, so it is the most careful.

**First, has this already been done?** The worker searches the recent history of `main` for a commit with this request's ID in it. It finds none. (Why look? Because if a previous attempt pushed and then crashed before it could record that, we must not push twice. More on Wednesday.)

**Then, edit exactly one line.** The worker parses the values file as a YAML *tree*, walks to `backend` → `image` → `tag`, and changes only that value. Comments and formatting stay as they were, so the diff is clean:

```diff
 backend:
   image:
     repository: registry.example.com/reconcile-backend
-    tag: a1b2c3d          # managed by deployctl
+    tag: e4f5a6b          # managed by deployctl
   replicaCount: 3         # managed by deployctl
```

**Commit, with a message that explains itself:**

```
deploy(backend/dev): a1b2c3d -> e4f5a6b

Requested-by: linh@example.com

Deployctl-Request: 0b8e2c1a-5d7f-4a1e-9c3b-2f6e8d1a7b40
```

The lines at the bottom are Git **trailers**: key-value lines at the end of a commit message. They make Git history a second audit trail, and the `Deployctl-Request` trailer is how a retry finds its own commit.

**Push**, using the GitHub App's one-hour token. GitHub accepts it. The new commit is `3c2d1e0`. The worker records that SHA in the step's output, marks `git_commit` succeeded and `argocd_refresh` ready.

```
✔ git_commit      pushed 3c2d1e0
```

From this moment the change is real. It is in Git, so Argo CD will apply it whether or not deployctl does anything else. That's why a request can only be cancelled *before* this step.

## Second 1.2: `argocd_refresh`

Argo CD checks the repo every three minutes. Waiting up to three minutes is a waste, so the worker asks Argo CD to look now:

```http
GET /api/v1/applications/reconcile-dev?refresh=normal
Authorization: Bearer <worker's Argo CD token>
```

Argo CD fetches `main`, sees `3c2d1e0`, renders the Helm chart with the new values, compares, finds the Deployment's image differs: `OutOfSync`. Because the Application has automated sync, Argo CD applies the new Deployment to the cluster. Kubernetes takes it from there.

```
✔ argocd_refresh
```

## Seconds 2 to 48: `wait_healthy`

Now the worker watches. Every 5 seconds, until success or a 5-minute timeout, it checks five things:

1. Argo CD says `Synced`...
2. ...**at revision `3c2d1e0`** (or something newer).
3. Argo CD says `Healthy`.
4. Every pod matching `app=reconcile-backend` in `demo-dev` is ready.
5. Every one of those pods runs image tag `e4f5a6b`.

Check 2 is the subtle one. In the first second after the push, before Argo CD has even noticed the new commit, it reports `Synced` and `Healthy`, about the **old** commit. A naive tool would declare victory right there, before anything happened. Checking the revision rules that out. Check 5 rules out the same mistake from the Kubernetes side.

Meanwhile, in the cluster, the rolling update from chapter 3 is happening:

```
$ kubectl get pods -n demo-dev -l app=reconcile-backend    (at ~second 20)
NAME                                  READY   STATUS              AGE
reconcile-backend-6b8f9c7d4-4kzpq     1/1     Running             2d     ← old
reconcile-backend-6b8f9c7d4-9xw2m     1/1     Running             2d     ← old
reconcile-backend-7f5d6c8b9-k2m4p     1/1     Running             15s    ← new, ready
reconcile-backend-7f5d6c8b9-r8n3v     0/1     ContainerCreating   3s     ← new, starting
```

Each poll, the worker updates the step's message, which flows to the CLI:

```
⏳ wait_healthy    2/3 pods ready
```

At second 48, all three pods are new and ready, Argo CD is `Synced` at `3c2d1e0` and `Healthy`.

## Second 48: `finalize`

One last transaction:

- add a row to `deployments`: `backend`, `dev`, `e4f5a6b`, commit `3c2d1e0`, by Linh. This history is what `rollback` will use.
- mark the request `succeeded`,
- write the audit event,
- notify.

The API sends the final snapshot and closes the stream. The CLI prints the last line and exits with code `0`.

```
✔ Deployed in 48s
```

## Looking at it afterwards

```bash
$ deployctl status backend
APP       ENV    SYNC     HEALTH    IMAGE TAG   REPLICAS
backend   dev    Synced   Healthy   e4f5a6b     3/3
backend   prod   Synced   Healthy   9f8e7d6     5/5

$ git log --oneline -1 origin/main      # in the config repo
3c2d1e0 deploy(backend/dev): a1b2c3d -> e4f5a6b
```

Linh opens the dev UI and checks an invoice that used to be off by a cent. It's right.

"Notice what you never needed," Sam says. "No kubeconfig. No GitHub write access. No Argo CD login. And there's a record, in two places, of exactly what you did."

## The request's life, in states

Everything above, as the request saw it:

```mermaid
stateDiagram-v2
    [*] --> pending: API stores it
    pending --> running: validate ok (dev, no approval)
    running --> succeeded: finalize
    succeeded --> [*]
```

Tomorrow you will meet the other paths: `failed`, `cancelled`, and, on Thursday, `awaiting_approval` and `rejected`.

---

## Check yourself

<details>
<summary>1. What does the API do with Git, Argo CD and Kubernetes when it receives a deploy request?</summary>

Nothing. It authenticates, authorises, validates the body against its config, and writes the request, steps and audit event to Postgres in one transaction. Everything that touches the outside world happens later, in the worker.
</details>

<details>
<summary>2. Two workers run the claim query at the same instant. How many get the <code>validate</code> step?</summary>

One. `FOR UPDATE SKIP LOCKED` lets the first lock the row; the second skips it and either finds another ready step or none.
</details>

<details>
<summary>3. Why does <code>wait_healthy</code> check Argo CD's sync <em>revision</em>, not just "Synced and Healthy"?</summary>

Right after the push, Argo CD has not yet seen the new commit and still reports `Synced` and `Healthy` for the old one. Checking that the synced revision is our commit (or newer) ensures we are looking at the result of our change.
</details>

<details>
<summary>4. After which step can the request no longer be cancelled, and why?</summary>

After `git_commit` starts. Once the commit is pushed, the change exists in Git and Argo CD will apply it no matter what deployctl does. Undoing it requires a new change, which is a rollback.
</details>

<details>
<summary>5. What are the two places that record that Linh deployed <code>e4f5a6b</code> to dev?</summary>

deployctl's database (the request, the `deployments` row and the audit events) and the config repo's Git history (the commit with `Requested-by` and `Deployctl-Request` trailers).
</details>

Next: [8. When things break](08-when-things-break.md)
