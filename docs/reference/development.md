# Development

> **Status:** M0 (steps and clients) is implemented and tested with fakes; `make check` runs everything CI runs. The local environment below (Postgres, API, worker, kind + Argo CD) is the M1 workflow and does not run yet.

- [Prerequisites](#prerequisites)
- [First run](#first-run)
- [Local environment in detail](#local-environment-in-detail)
- [Dev auth mode](#dev-auth-mode)
- [Make targets](#make-targets)
- [Project structure](#project-structure)
- [Coding guidelines](#coding-guidelines)
- [Editing YAML safely](#editing-yaml-safely)
- [Testing](#testing)
- [Adding a new request kind](#adding-a-new-request-kind)

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Go 1.22+ | Build and test |
| Docker and Docker Compose | Postgres and a dev identity provider |
| kind or Minikube | A local cluster |
| Argo CD in the cluster | With applications pointing at a test config repo |
| `git` | The worker shells out to it |
| A GitHub App on a test repo | Or `--git-mode=local` with a local bare repo *(planned)* |
| `golangci-lint` | `make lint` |

## First run

```bash
git clone https://github.com/<you>/deployctl.git && cd deployctl
cp deployctl.config.example.yaml deployctl.config.yaml

make db-up           # Postgres via docker compose
make migrate         # apply migrations
make api             # start the API on :8080
make worker          # start the worker (separate terminal)
```

Then, in a third terminal:

```bash
export DEPLOYCTL_API_URL=http://127.0.0.1:8080
export DEPLOYCTL_TOKEN=dev                  # dev auth mode
./bin/deployctl whoami
./bin/deployctl status
./bin/deployctl deploy backend --env dev --tag e4f5a6b
```

## Local environment in detail

### Cluster and Argo CD

```bash
kind create cluster --name deployctl
kubectl create namespace gitops
kubectl apply -n gitops -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
kubectl -n gitops rollout status deploy/argocd-server
```

Port-forward the Argo CD API on a port that does not clash with deployctl's `:8080`:

```bash
kubectl port-forward svc/argocd-server -n gitops 8443:443
export DEPLOYCTL_ARGOCD_SERVER=127.0.0.1:8443
```

Create the `deployctl-worker` and `deployctl-api` accounts and tokens as in [security.md](security.md#argo-cd-accounts).

### Test config repo

Create a small repo (for example `reconcile-config-dev`) with a Helm chart and `values-dev.yaml` as in [configuration.md](configuration.md#mapping-to-the-config-repo), and an Argo CD `Application` pointing at it. Any public image works for the app (for example `nginx`, deploying tags `1.27.0`, `1.27.1`), so you do not need your own registry to exercise the pipeline.

### Without GitHub *(planned)*

`--git-mode=local` makes the worker push to a local bare repo (`file:///tmp/config.git`), and Argo CD can read it if it is served from inside the cluster (for example a `git-daemon` pod). This removes the GitHub App from the inner loop.

## Dev auth mode

Standing up an identity provider is a lot for a first run. `auth.mode: dev` (or `--auth=dev`) accepts the fixed token `dev` and maps it to a fixed identity and role:

```bash
./bin/deployctl api --auth=dev --dev-user=linh --dev-role=developer
```

To test approvals you need two identities, so dev mode also accepts `dev:<user>:<role>` tokens, for example `DEPLOYCTL_TOKEN=dev:priya:approver`. *(proposed)*

Dev mode refuses to start when bound to a non-loopback address, and when `DEPLOYCTL_ENV=production`.

`docker-compose.yaml` also starts Keycloak with a realm export containing the `deployctl-*` groups and three users, for testing real OIDC from M2 onward.

## Make targets

```bash
make build      # build ./bin/deployctl
make test       # unit tests
make test-db    # store tests against a Postgres container (testcontainers)
make test-e2e   # (planned) full flow against kind
make lint       # golangci-lint, including depguard rules
make fmt        # gofmt and goimports
make db-up      # start Postgres (and Keycloak) via docker compose
make db-reset   # drop and recreate the dev database
make migrate    # apply migrations
make api        # run the API in dev auth mode
make worker     # run the worker
```

## Project structure

```
deployctl/
├── cmd/
│   ├── deployctl/main.go      # (M1) mode dispatch
│   ├── ghcheck/               # read-only check of a GitHub App setup
│   └── testgit/               # manual read of a real config repo through internal/git
├── internal/
│   ├── steps/                 # one file per step, deps.go (interfaces), fakes/
│   ├── git/                   # go-git cached clone, YAML value edit, commit, push
│   ├── githubapp/             # GitHub App installation tokens
│   ├── argocd/                # Argo CD client
│   ├── kube/                  # read-only Kubernetes client
│   ├── archtest/              # dependency rule tests
│   ├── api/                   # (M1) handlers, middleware, SSE hub
│   ├── auth/                  # (M2) OIDC verification, role mapping, policy.go
│   ├── worker/                # (M1) claim loop, leases, heartbeats
│   ├── store/                 # (M1) Postgres access and transactions
│   ├── audit/                 # (M3) audit event construction
│   ├── config/                # (M1) load and validate config
│   └── client/                # (M4) CLI: API client, login, rendering
├── migrations/                # (M1)
├── deployctl.config.example.yaml  # (M1)
├── docker-compose.yaml        # (M1)
├── Makefile
└── go.mod
```

See [architecture.md](architecture.md#code-organisation) for dependency rules and interfaces.

### Checking a real GitHub App and repo

Two small commands talk to GitHub for real, read-only, without the rest of the system:

```bash
# App ID, key and installation are accepted; a token can read the config repo
DEPLOYCTL_GITHUB_APP_ID=... \
DEPLOYCTL_GITHUB_APP_INSTALLATION_ID=... \
DEPLOYCTL_GITHUB_APP_PRIVATE_KEY_PATH=$HOME/.config/deployctl/github-app.pem \
go run ./cmd/ghcheck

# Read a few values through internal/git, authenticated as the App
go run ./cmd/testgit
```

Neither pushes. Keep the private key outside the repository with mode `600`.

## Coding guidelines

- **Standard library first.** `net/http` with Go 1.22 routing patterns, `log/slog`, `context`. Add a dependency when it saves real work (`pgx`, `client-go`, `yaml.v3`, `go-oidc`, a migration tool, `cobra` for the CLI).
- **Transactions are explicit.** `store` exposes `WithTx(ctx, func(tx Tx) error)`; every state change plus its audit event plus its `NOTIFY` goes in one call.
- **No business logic in handlers.** Handlers parse, authorise, call a service function, render.
- **Errors carry a class.** Steps return `steps.Retryable(err)` or a plain error (permanent). The worker decides what to do; steps do not sleep or loop on errors themselves.
- **Context everywhere.** Every external call takes a `context.Context` and respects cancellation, which is how a lost lease stops a step.
- **Interfaces at the consumer.** `steps` defines the small interfaces it needs from `git`, `argocd`, `kube`; those packages do not export interfaces.

## Editing YAML safely

The values file is edited in place, and its comments and formatting must survive, because humans read and review that file. See [ADR 0011](../adr/0011-edit-yaml-with-the-node-api.md).

**Wrong:** unmarshal into `map[string]any`, set the value, marshal. This drops comments, reorders keys and may change quoting.

**Also not good enough:** parse into a `yaml.Node` tree, change the scalar, and re-encode the tree. Comments mostly survive, but the encoder normalises indentation, alignment and quoting elsewhere in the file, so the diff is no longer one line.

**What `internal/git` does** (`SetYAMLValue` in `yaml_set.go`): use the parser only to **find** the value, then replace exactly those bytes in the original file.

1. Parse into a `yaml.Node` tree and walk the dotted path (`backend.image.tag`). A missing key is an error wrapping `steps.ErrNotFound`; deployctl changes values, humans own the file's structure.
2. Refuse anything that is not a plain or quoted single-line scalar: mappings, `null`, block scalars (`|`, `>`), explicit tags (`!!str`), and values with newlines.
3. Find the scalar's exact byte span from the node's line and column, including its quotes.
4. Render the new value: for an image tag (`ValueString`), keep the existing quote style, and quote a plain value that would otherwise read back as something other than a string (`1.10`, `0123`, `true`, `null`, dates); for a replica count (`ValueInt`), write a plain integer.
5. Splice it in, then parse the result again and check the key now reads back as exactly the new value with the right type. If not, return an error instead of writing a broken file.

Everything else in the file, including the trailing `# managed by deployctl` comment and its alignment, stays byte-for-byte the same.

Cases the tests cover (`internal/git/yaml_set_test.go`, table tests with the expected output inline):

- aligned trailing comments survive, and the diff is exactly one line,
- numeric- and bool-looking tags such as `1.10` and `true` stay strings,
- the existing quote style (double or single) is kept,
- flow mappings, CRLF line endings and non-ASCII text earlier on the line,
- the same key name under different parents (`backend.image.tag` vs `frontend.image.tag`),
- missing keys, non-scalars, `null`, block scalars, explicit tags, empty values and values containing newlines are errors.

## Testing

| Layer | What | How |
|---|---|---|
| Steps | Each step's logic, idempotency and error classes | Fake `git`, `argocd`, `kube` implementations; table tests |
| Policy | Every role × action × environment type, plus ownership and self-approval | One table test that asserts full coverage |
| YAML | Edits touch only the intended line, comments survive, numeric-looking tags stay strings | Table tests with the expected file inline |
| Store | `SKIP LOCKED` claiming, lease expiry and reclaim, fencing, one-active-request index, self-approval `CHECK`, audit immutability for `deployctl_app` | Real Postgres in a container |
| Idempotency | Replay the same API request; run `git_commit` twice; crash after push | Fakes for git, plus a test that kills the step after push and reclaims |
| API | Handlers, status codes, problem types, SSE | `httptest` with a real store and fake clients |
| CLI | Rendering and exit codes | Golden output from canned API responses |
| End to end *(planned)* | Full deploy, prod approval, failed rollout, rollback | kind + Argo CD + local bare repo in CI |

What M0 has, all run by `make check`:

- **Steps** (`internal/steps`): table tests for every step against the fakes in `internal/steps/fakes`, including a re-run after a crash for each (for `git_commit`, a push that happened but was not recorded must not be pushed again). `wait_healthy` runs on a fake clock, so no test waits for real.
- **Git** (`internal/git`): against local bare repositories, including concurrent pushes from two workers and "someone pushed first", plus the YAML cases above.
- **GitHub App** (`internal/githubapp`): key parsing, token fetching and caching (concurrent callers share one refresh), the bot identity, and error classes, against an `httptest` server.
- **Argo CD** (`internal/argocd`): against an `httptest` server returning Argo CD-shaped responses, covering state mapping, operation tracking, refresh, sync and desired images.
- **Argo CD smoke test** (`internal/argocd/live_test.go`): read-only (`Get` and `GetDesiredImages`, never `Sync`) against a real Argo CD. It is skipped unless `DEPLOYCTL_ARGOCD_SMOKE=1`, and then needs `DEPLOYCTL_ARGOCD_SERVER`, `DEPLOYCTL_ARGOCD_TOKEN`, `DEPLOYCTL_ARGOCD_SMOKE_APP`, `DEPLOYCTL_ARGOCD_SMOKE_REVISION` (a full commit SHA), `DEPLOYCTL_ARGOCD_SMOKE_NAMESPACE`, `DEPLOYCTL_ARGOCD_SMOKE_SELECTOR` and `DEPLOYCTL_ARGOCD_SMOKE_EXPECTED_IMAGES` (JSON, container name → image). For a private CA, also set `DEPLOYCTL_ARGOCD_SMOKE_CA_FILE` and, if needed, `DEPLOYCTL_ARGOCD_SMOKE_TLS_SERVER_NAME`.
- **Kubernetes** (`internal/kube`): without a cluster. client-go fakes exercise pod mapping and event selection, while HTTP/transport fixtures check pagination, log query parameters, stream closure, byte limits, cancellation and error classes. The real HPA lookup is deferred to M5 behind `steps.AutoscalingReader`; the kube client implements only `steps.KubeReader`.
- **Architecture** (`internal/archtest`): the import rules from [architecture.md](architecture.md#code-organisation).

Store tests worth writing first, because they protect the core guarantees:

```go
func TestClaim_TwoWorkersNeverGetSameStep(t *testing.T)
func TestClaim_ExpiredLeaseIsReclaimed(t *testing.T)
func TestComplete_StaleWorkerIsFenced(t *testing.T)
func TestCreate_SecondActiveRequestConflicts(t *testing.T)
func TestApproval_SelfApprovalRejectedByDatabase(t *testing.T)
func TestAudit_AppRoleCannotUpdateOrDelete(t *testing.T)
```

## Adding a new request kind

For example, a future `restart` (bump a `podAnnotations.restartedAt` value):

1. Add the kind to the `requests.kind` `CHECK` in a migration.
2. Add an endpoint and body type in `internal/api`.
3. Map it to an action in `policy.go` and extend the policy table test.
4. Teach `validate` how to compute `from`/`to` for the kind.
5. Teach `git_commit` which key to edit (ideally a new config key, for example `restartKey`).
6. Decide what `wait_healthy` must check for it.
7. Add a CLI command.
8. Add an ADR if the kind changes any principle (for example, if it needs a cluster write).
