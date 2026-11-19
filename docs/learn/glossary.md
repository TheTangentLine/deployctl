# Glossary

Terms used across the docs, with the chapter of the [story track](README.md) where each is introduced.

| Term | Meaning | Ch. |
|---|---|---|
| **202 Accepted** | HTTP status meaning "received and will be processed", not "done". The API returns it for every mutating request. | 7 |
| **Access token** | Short-lived JWT sent with every API call to prove identity. | 5 |
| **ADR** | Architecture Decision Record: a short document recording one design decision, its context and its trade-offs. | — |
| **Approval / approval gate** | The step that pauses a production request until someone with the approver role, who is not the requester, decides. | 9 |
| **Argo CD** | A GitOps agent running in the cluster that makes Kubernetes match a Git repository. | 4 |
| **Argo CD Application** | An Argo CD object that maps a path in a repo (at a branch) to a namespace in a cluster. | 4 |
| **Audit event / audit log** | An append-only record of who did what, when, to which resource, and the outcome. | 6 |
| **Authentication (authn)** | Proving who you are. | 5 |
| **Authorization (authz)** | Deciding whether an identity may perform an action. | 5 |
| **Blast radius** | How much damage follows if one component is compromised. | 6 |
| **Chart (Helm)** | A folder of Kubernetes manifest templates plus default values. | 4 |
| **Claim (JWT)** | One field in a JWT payload, such as `sub` or `groups`. | 5 |
| **Claim (queue)** | A worker taking ownership of a step to run it. | 7 |
| **Cluster** | A set of machines (nodes) managed together by Kubernetes. | 3 |
| **Config repo** | The Git repository holding the desired state of every app and environment. | 4 |
| **Container** | A process running with its own isolated filesystem and resource limits, started from an image. | 2 |
| **Controller** | A program that runs a reconciliation loop for one kind of Kubernetes object. | 3 |
| **CrashLoopBackOff** | Pod status: the container keeps exiting and Kubernetes waits longer between restarts. | 3 |
| **Deployment (Kubernetes)** | An object that keeps a number of identical pods running and performs rolling updates. | 3 |
| **Deployment (deployctl)** | A row in the `deployments` table recording a successful deploy or rollback, used to pick rollback targets. | 8 |
| **Desired state** | What should exist, as described in Git or a Kubernetes object. | 3 |
| **Digest** | A SHA-256 hash identifying an image's exact contents. | 2 |
| **Drift** | A difference between Git and the cluster that Git did not cause. | 4 |
| **Durable intent** | Writing down what will be done before doing it, so a crash can resume. | 7 |
| **Environment** | One tier of an app, such as `dev` or `prod`. | 1 |
| **Events (Kubernetes)** | Short messages from Kubernetes components about an object, such as "Back-off restarting failed container". | 3 |
| **Exponential backoff** | Waiting longer after each failed retry (2s, 4s, 8s...). | 8 |
| **Fencing** | Making each write conditional on still owning the work (`claimed_by`, `attempt`), so a stale worker cannot overwrite newer results. | 8 |
| **Four-eyes principle** | No single person can make a sensitive change alone. Same as separation of duties. | 6 |
| **GitHub App** | A non-human GitHub identity, installed on specific repos with specific permissions, issuing one-hour tokens. | 6 |
| **GitOps** | Running systems from declarative desired state in Git, pulled and continuously reconciled by an agent. | 4 |
| **Groups claim** | A JWT claim listing the user's groups; deployctl maps groups to roles. | 5 |
| **Health (Argo CD)** | Whether an Application's resources are working: `Healthy`, `Progressing`, `Degraded`, `Missing`. | 4 |
| **Heartbeat** | A periodic lease renewal by the worker running a step. | 7 |
| **Helm** | A templating and packaging tool for Kubernetes manifests. | 4 |
| **HPA** | HorizontalPodAutoscaler: changes a Deployment's replica count automatically. | 3 |
| **Idempotency key** | A client-chosen ID that makes retrying the same request return the original result. | 8 |
| **Idempotent** | Doing it twice has the same effect as doing it once. | 8 |
| **Identity provider (IdP)** | The central login system (Keycloak, Okta, Entra ID) that authenticates users and issues tokens. | 5 |
| **Image** | A packaged, read-only filesystem plus start command, from which containers run. | 2 |
| **ImagePullBackOff** | Pod status: the node cannot download the image (often a wrong tag). | 3 |
| **JWKS** | JSON Web Key Set: the identity provider's published public keys, used to verify token signatures. | 5 |
| **JWT** | JSON Web Token: a signed, Base64URL-encoded set of claims. | 5 |
| **kubeconfig** | A file with a cluster address and credentials for `kubectl`. | 1 |
| **Label / label selector** | Key-value tags on Kubernetes objects, and queries over them (`app=reconcile-backend`). | 3 |
| **Lease** | Time-limited ownership of a running step; if not renewed, another worker may take over. | 7 |
| **Least privilege** | Every person and program gets only the access its job needs. | 6 |
| **Liveness probe** | A check that, when failing, makes Kubernetes restart the container. | 3 |
| **Namespace** | A named partition of a Kubernetes cluster. | 3 |
| **Node** | A machine in a Kubernetes cluster that runs pods. | 3 |
| **Non-fast-forward** | A Git push rejected because the branch moved on since you fetched it. | 8 |
| **NOTIFY / LISTEN** | Postgres features for sending and receiving lightweight notifications; the worker notifies, the API listens. | 7 |
| **OIDC** | OpenID Connect: a standard for delegating login to an identity provider, built on OAuth 2.0. | 5 |
| **Partial unique index** | A database uniqueness rule that applies only to rows matching a condition; used for "one active request per app and environment". | 7 |
| **PKCE** | Proof Key for Code Exchange: lets a public client (like a CLI) prove it started the login flow. | 5 |
| **Pod** | The smallest unit Kubernetes runs: one or more containers sharing a network address. | 3 |
| **Readiness probe** | A check that decides whether a pod receives traffic and counts as available. | 3 |
| **Reconciliation loop** | Repeatedly comparing desired and actual state and acting to close the gap. | 3 |
| **Refresh (Argo CD)** | Asking Argo CD to re-read Git now rather than at its next poll. | 4 |
| **Refresh token** | Longer-lived token used only to obtain new access tokens. | 5 |
| **Registry** | A server that stores and serves container images. | 2 |
| **Replica** | One copy of a pod managed by a Deployment. | 3 |
| **ReplicaSet** | Keeps a number of pods of one template version running; managed by a Deployment. | 3 |
| **Request (deployctl)** | A recorded "do this": a deploy, scale or rollback for one app and environment. | 7 |
| **Retry** | Resuming a failed request from its failed step. | 8 |
| **Retryable / permanent error** | Errors that are likely temporary (retried automatically) versus ones that will not fix themselves (fail the request). | 8 |
| **Role** | A named set of permissions (`viewer`, `developer`, `approver`, `admin`). | 6 |
| **RBAC** | Role-based access control: permissions attached to roles, roles assigned to people. | 6 |
| **Rollback** | A new request and commit that sets the tag back to the last good one. | 8 |
| **Rolling update** | Replacing pods a few at a time so there is no downtime. | 3 |
| **Self-heal (Argo CD)** | Automatically reverting drift in the cluster to match Git. | 4 |
| **Separation of duties** | Splitting a sensitive action between two people. | 6 |
| **SKIP LOCKED** | Postgres option that makes concurrent claimers skip rows another transaction has locked. | 7 |
| **SSE** | Server-Sent Events: a long-lived HTTP response over which the server pushes messages. | 7 |
| **Step** | One unit of work in a request: `validate`, `approval_gate`, `git_commit`, `argocd_refresh`, `wait_healthy`, `finalize`. | 7 |
| **`sub` (subject)** | The identity provider's stable, unique user ID. | 5 |
| **Sync / sync status** | Argo CD applying Git to the cluster; whether they match (`Synced`, `OutOfSync`). | 4 |
| **Sync revision** | The Git commit Argo CD last synced. | 4 |
| **Tag** | A human-readable, movable label for an image version. | 2 |
| **Threat model** | A structured list of what could go wrong security-wise, its impact and its controls. | 6 |
| **Trailer** | A `Key: value` line at the end of a Git commit message. | 7 |
| **Transaction** | A group of database changes that all happen or none do. | 7 |
| **Values file** | A YAML file of per-environment settings fed to a Helm chart. | 4 |
| **Worker** | The deployctl process that claims and runs steps; the only part that can push to Git. | 7 |
