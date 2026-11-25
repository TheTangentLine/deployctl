# Configuration

> **Status:** design. Fields from the original README are the baseline; fields marked *(proposed)* are additions.

The server modes (`api`, `worker`, `migrate`) read one YAML file, given by `--config` (default `deployctl.config.yaml`). **No secrets go in it.** Secrets come from environment variables or mounted files.

- [Full example](#full-example)
- [Field reference](#field-reference)
- [Validation](#validation)
- [Environment variables](#environment-variables)
- [Mapping to the config repo](#mapping-to-the-config-repo)
- [Argo CD setup](#argo-cd-setup)
- [Reloading](#reloading)

---

## Full example

```yaml
# deployctl.config.yaml
server:
  listen: ":8080"
  publicURL: https://deployctl.example.com     # (proposed) used in links printed by the CLI

auth:
  mode: oidc                                   # oidc | dev
  issuer: https://keycloak.example.com/realms/deployctl
  audience: deployctl
  cliClientID: deployctl-cli                   # (proposed) public client used by `deployctl login`
  groupsClaim: groups
  roleMapping:
    viewer:    [deployctl-viewers]
    developer: [deployctl-developers]
    approver:  [deployctl-approvers]
    admin:     [deployctl-admins]
  machineGroups: [deployctl-ci]                # (proposed) principals in these groups are machines

worker:
  concurrency: 4                               # (proposed) steps in parallel per worker process
  rolloutTimeout: 5m
  pollInterval: 5s
  gitCacheDir: /var/cache/deployctl/git        # (proposed)

apps:
  backend:
    maxReplicas: 20                            # (proposed) upper bound for `scale`
    git:
      repo: https://github.com/example/reconcile.git
      branch: main
      imageTagKey: backend.image.tag           # dotted path inside the values file
      replicasKey: backend.replicaCount
    environments:
      dev:
        valuesFile: charts/reconcile/values-dev.yaml
        requiresApproval: false
        argocd:
          application: reconcile-dev
        kube:
          namespace: demo-dev
          labelSelector: app=reconcile-backend
      prod:
        valuesFile: charts/reconcile/values-prod.yaml
        requiresApproval: true
        argocd:
          application: reconcile-prod
        kube:
          namespace: demo-prod
          labelSelector: app=reconcile-backend

  frontend:
    git:
      repo: https://github.com/example/reconcile.git
      branch: main
      imageTagKey: frontend.image.tag
      replicasKey: frontend.replicaCount
    environments:
      dev:
        valuesFile: charts/reconcile/values-dev.yaml
        requiresApproval: false
        argocd: { application: reconcile-dev }
        kube:   { namespace: demo-dev, labelSelector: app=reconcile-frontend }
```

Note that `backend` and `frontend` share one values file and one Argo CD application per environment. That is fine: they edit different keys. It does mean `wait_healthy` for `backend` waits on the whole `reconcile-dev` application's health, so a broken `frontend` can fail a `backend` deploy. If that matters, give each app its own Argo CD application.

## Field reference

### `server`

| Field | Default | Description |
|---|---|---|
| `listen` | `:8080` | Address the API binds. |
| `publicURL` | — | External URL, used in links and in `/v1/auth-config`. *(proposed)* |

### `auth`

| Field | Default | Description |
|---|---|---|
| `mode` | `oidc` | `dev` enables fixed-token auth; loopback only. |
| `issuer` | required | OIDC issuer URL. Must match the `iss` claim exactly (watch trailing slashes). |
| `audience` | required | Expected `aud` claim. Configure the identity provider to add it to access tokens. |
| `cliClientID` | — | Public OIDC client used by the CLI. *(proposed)* |
| `groupsClaim` | `groups` | Claim holding group names. Keycloak: add a "Group Membership" mapper with "Full group path" off. |
| `roleMapping.<role>` | required | Identity provider groups that grant the role. Roles: `viewer`, `developer`, `approver`, `admin`. |
| `machineGroups` | `[]` | Groups that mark a principal as a machine. *(proposed)* |

### `worker`

| Field | Default | Description |
|---|---|---|
| `concurrency` | `4` | Steps run in parallel per process. *(proposed)* |
| `rolloutTimeout` | `5m` | How long `wait_healthy` waits before failing. Measured from the step's first start. |
| `pollInterval` | `5s` | Interval between health polls. |
| `gitCacheDir` | temp dir | Where the worker keeps clones. Use a volume to avoid re-cloning on restart. *(proposed)* |

### `apps.<name>`

App names: lowercase letters, digits and `-`, up to 63 characters.

| Field | Description |
|---|---|
| `maxReplicas` | Upper bound for `scale` (default 50). *(proposed)* |
| `git.repo` | HTTPS URL of the config repo. The GitHub App must be installed on it. |
| `git.branch` | Branch Argo CD tracks. |
| `git.imageTagKey` | Dotted path to the image tag inside the values file. |
| `git.replicasKey` | Dotted path to the replica count. Omit to disable `scale` for the app. |
| `environments.<env>.valuesFile` | Path to that environment's values file, relative to the repo root. |
| `environments.<env>.requiresApproval` | Whether requests need an approver. This, not the name, is what makes an environment "prod". |
| `environments.<env>.argocd.application` | Argo CD `Application` name. |
| `environments.<env>.argocd.project` | Argo CD project (default `default`). Used for RBAC object names. *(proposed)* |
| `environments.<env>.kube.namespace` | Namespace where the pods run. |
| `environments.<env>.kube.labelSelector` | Selector that matches exactly this app's pods. |

Dotted paths: `backend.image.tag` means:

```yaml
backend:
  image:
    tag: a1b2c3d
```

Keys containing dots are not supported. Sequences (lists) are not supported in paths.

## Validation

At startup the service validates the config and **refuses to start** on any error. It reports all errors at once, not just the first.

- Required fields are present; durations parse; `pollInterval < rolloutTimeout`.
- Every role in `roleMapping` has at least one group; unknown role names are rejected.
- App and environment names match the naming rules.
- Every app has `imageTagKey`; every environment has `valuesFile`, `argocd.application`, `kube.namespace` and `kube.labelSelector`.
- `labelSelector` parses as a Kubernetes label selector.
- `auth.mode: dev` is rejected unless `server.listen` is a loopback address.
- *(proposed, worker only)* `deployctl worker --check-config` additionally checks that each values file and key exists on the branch and each Argo CD application is readable. This is a command, not a startup check, so a temporarily unreachable GitHub does not stop the worker from starting.

## Environment variables

| Variable | Used by | Purpose |
|---|---|---|
| `DEPLOYCTL_DATABASE_URL` | api, worker, migrate | Postgres connection string. `migrate` uses the `deployctl_migrate` role; the others use `deployctl_app`. |
| `DEPLOYCTL_GITHUB_APP_ID` | worker | GitHub App ID. |
| `DEPLOYCTL_GITHUB_APP_INSTALLATION_ID` | worker | Installation ID for the config repo. |
| `DEPLOYCTL_GITHUB_APP_PRIVATE_KEY_PATH` | worker | Path to the App's private key (PEM). |
| `DEPLOYCTL_ARGOCD_SERVER` | api, worker | Argo CD API address, for example `argocd-server.gitops.svc:443`. |
| `DEPLOYCTL_ARGOCD_TOKEN` | api, worker | Argo CD token. Use **different** accounts for api (`get`) and worker (`get`, `sync`). |
| `KUBECONFIG` | api, worker | Kubeconfig path. If unset, in-cluster service account credentials are used. |
| `DEPLOYCTL_ENV` | all | `production` disables `auth.mode: dev` unconditionally. *(proposed)* |
| `DEPLOYCTL_LOG_LEVEL` | all | `debug`, `info` (default), `warn`, `error`. *(proposed)* |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | api, worker | Standard OpenTelemetry exporter settings. |

The kube client loads the explicit `KUBECONFIG` path (or OS-separated path list)
and returns an error for missing, malformed or unusable files. Without a value it
uses in-cluster credentials; it does not search `~/.kube/config`. API/worker wiring
is planned for M1; the M0 client exposes `kube.LoadConfig` and `kube.NewForConfig`.

`deployctl api` refuses to start if any `DEPLOYCTL_GITHUB_APP_*` variable is set, to keep write credentials out of the API process. *(proposed)*

## Mapping to the config repo

For the example above, the config repo looks like this:

```
reconcile/
└── charts/
    └── reconcile/
        ├── Chart.yaml
        ├── templates/
        │   ├── backend-deployment.yaml
        │   └── frontend-deployment.yaml
        ├── values.yaml            # defaults
        ├── values-dev.yaml        # edited by deployctl for dev
        └── values-prod.yaml       # edited by deployctl for prod
```

```yaml
# charts/reconcile/values-dev.yaml
backend:
  image:
    repository: registry.example.com/reconcile-backend
    tag: a1b2c3d          # managed by deployctl
  replicaCount: 3         # managed by deployctl
frontend:
  image:
    repository: registry.example.com/reconcile-frontend
    tag: a1b2c3d
  replicaCount: 1
```

And the Argo CD application:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: reconcile-dev
  namespace: gitops
spec:
  project: default
  source:
    repoURL: https://github.com/example/reconcile.git
    targetRevision: main
    path: charts/reconcile
    helm:
      valueFiles: [values.yaml, values-dev.yaml]
  destination:
    server: https://kubernetes.default.svc
    namespace: demo-dev
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

When automated sync is enabled, Argo CD applies a new commit by itself after
`argocd_refresh`. `AppState.AutoSync` reports whether it is enabled:

- No `spec.syncPolicy.automated`, or `automated.enabled: false`: `AutoSync = false`.
- `automated` exists and `enabled` is `true`, omitted or `null`: `AutoSync = true`.

These rules follow Argo CD's [automated sync policy](https://argo-cd.readthedocs.io/en/stable/user-guide/auto_sync/).
When `AutoSync` is false, the worker must request a manual sync after refreshing.
`selfHeal: true` reverts manual cluster changes while automated sync is enabled.

The pod template must carry the label used in `kube.labelSelector`:

```yaml
# templates/backend-deployment.yaml (excerpt)
spec:
  template:
    metadata:
      labels:
        app: reconcile-backend
```

## Argo CD setup

Create dedicated accounts instead of using `admin`, grant minimal permissions, then generate tokens. Full RBAC snippet in [security.md](security.md#argo-cd-accounts).

```bash
argocd account generate-token --account deployctl-worker
argocd account generate-token --account deployctl-api
```

## Reloading

Config is read at startup only. To change it, update the file (usually a ConfigMap) and restart the pods. In-flight requests are unaffected, because each request stores a snapshot of its resolved target when created.
