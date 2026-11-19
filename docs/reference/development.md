# Development

> **Status:** design. Nothing is implemented yet. This describes the intended developer workflow so that the first commits follow it.

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
├── cmd/deployctl/main.go      # mode dispatch
├── internal/
│   ├── api/                   # handlers, middleware, SSE hub
│   ├── auth/                  # OIDC verification, role mapping, policy.go
│   ├── worker/                # claim loop, leases, heartbeats
│   ├── steps/                 # one file per step
│   ├── store/                 # Postgres access and transactions
│   ├── git/                   # cached clone, YAML node edit, commit, push
│   ├── argocd/                # Argo CD client
│   ├── kube/                  # read-only Kubernetes client
│   ├── audit/                 # audit event construction
│   ├── config/                # load and validate config
│   └── client/                # CLI: API client, login, rendering
├── migrations/
├── testdata/                  # golden YAML files, sample configs
├── deployctl.config.example.yaml
├── docker-compose.yaml
├── Makefile
└── go.mod
```

See [architecture.md](architecture.md#code-organisation) for dependency rules and interfaces.

## Coding guidelines

- **Standard library first.** `net/http` with Go 1.22 routing patterns, `log/slog`, `context`. Add a dependency when it saves real work (`pgx`, `client-go`, `yaml.v3`, `go-oidc`, a migration tool, `cobra` for the CLI).
- **Transactions are explicit.** `store` exposes `WithTx(ctx, func(tx Tx) error)`; every state change plus its audit event plus its `NOTIFY` goes in one call.
- **No business logic in handlers.** Handlers parse, authorise, call a service function, render.
- **Errors carry a class.** Steps return `steps.Retryable(err)` or a plain error (permanent). The worker decides what to do; steps do not sleep or loop on errors themselves.
- **Context everywhere.** Every external call takes a `context.Context` and respects cancellation, which is how a lost lease stops a step.
- **Interfaces at the consumer.** `steps` defines the small interfaces it needs from `git`, `argocd`, `kube`; those packages do not export interfaces.

## Editing YAML safely

The values file is edited in place, and its comments and formatting must survive, because humans read and review that file.

**Wrong:** unmarshal into `map[string]any`, set the value, marshal. This drops comments, reorders keys and may change quoting.

**Right:** parse into a `yaml.Node` tree, walk the dotted path, change only the scalar's `Value`, encode the tree.

```go
// SetValue sets the scalar at path. kind is "!!str" for image tags and
// "!!int" for replica counts, so each keeps its YAML type.
func SetValue(doc *yaml.Node, path []string, value, kind string) (changed bool, err error) {
    n := doc.Content[0] // document -> mapping
    for i, key := range path {
        if n.Kind != yaml.MappingNode {
            return false, fmt.Errorf("%s is not a mapping", strings.Join(path[:i], "."))
        }
        next := (*yaml.Node)(nil)
        for j := 0; j+1 < len(n.Content); j += 2 {
            if n.Content[j].Value == key {
                next = n.Content[j+1]
                break
            }
        }
        if next == nil {
            return false, fmt.Errorf("key %s not found", strings.Join(path[:i+1], "."))
        }
        n = next
    }
    if n.Kind != yaml.ScalarNode {
        return false, fmt.Errorf("%s is not a scalar", strings.Join(path, "."))
    }
    if n.Value == value {
        return false, nil
    }
    n.Value = value
    n.Tag = kind
    // An image tag like 1.10 must stay the string "1.10", not become the float 1.1.
    // Quote it explicitly whenever plain style would read back as a non-string.
    if kind == "!!str" && looksNonString(value) { // number, bool, null, timestamp
        n.Style = yaml.DoubleQuotedStyle
    }
    return true, nil
}
```

Caveats to test for:

- `yaml.v3` re-indents the whole document to its configured indent. Set the encoder's indent to match the file (detect it from the first nested key) so the diff is one line.
- Tags such as `1.10` or `0123` must stay strings. Image tags that look like numbers are a classic bug.
- A missing key is an error, not "create it". deployctl changes values; humans own the file's structure.

Golden tests in `testdata/yaml/` check that the diff between input and output is exactly the intended line.

## Testing

| Layer | What | How |
|---|---|---|
| Steps | Each step's logic, idempotency and error classes | Fake `git`, `argocd`, `kube` implementations; table tests |
| Policy | Every role × action × environment type, plus ownership and self-approval | One table test that asserts full coverage |
| YAML | Edits touch only the intended line, comments survive, numeric-looking tags stay strings | Golden files |
| Store | `SKIP LOCKED` claiming, lease expiry and reclaim, fencing, one-active-request index, self-approval `CHECK`, audit immutability for `deployctl_app` | Real Postgres in a container |
| Idempotency | Replay the same API request; run `git_commit` twice; crash after push | Fakes for git, plus a test that kills the step after push and reclaims |
| API | Handlers, status codes, problem types, SSE | `httptest` with a real store and fake clients |
| CLI | Rendering and exit codes | Golden output from canned API responses |
| End to end *(planned)* | Full deploy, prod approval, failed rollout, rollback | kind + Argo CD + local bare repo in CI |

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
