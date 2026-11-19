# CLI

> **Status:** design. Commands and flags are from the original README; output formats and the few additions are marked *(proposed)*.

The CLI is a thin client over the [HTTP API](api.md). It logs in once, then every command is an API call. It needs **no** Git, Argo CD or kubeconfig access, which is the point: a laptop with deployctl on it can deploy, but cannot change the cluster.

- [Install and configure](#install-and-configure)
- [Authentication](#authentication)
- [status](#status)
- [deploy](#deploy)
- [scale](#scale)
- [rollback](#rollback)
- [Requests](#requests)
- [Approvals](#approvals)
- [Audit](#audit)
- [Global flags and environment](#global-flags-and-environment)
- [Output and exit codes](#output-and-exit-codes)
- [Using it from CI](#using-it-from-ci)

---

## Install and configure

The CLI is the same `deployctl` binary as the server. Point it at an API:

```bash
export DEPLOYCTL_API_URL=https://deployctl.example.com
# or per command: --api https://deployctl.example.com
```

*(proposed)* `deployctl config set api https://deployctl.example.com` writes `~/.config/deployctl/config.yaml` so the variable is not needed. The CLI discovers the OIDC issuer and client ID from the API (`GET /v1/auth-config`, unauthenticated), so users configure only the URL.

## Authentication

```bash
deployctl login        # opens the browser for OIDC login (PKCE, loopback redirect)
deployctl whoami       # show current user and roles
deployctl logout       # delete local tokens *(proposed)*
```

```
$ deployctl whoami
Linh Tran <linh@example.com>
roles: developer, viewer
issuer: https://keycloak.example.com/realms/deployctl
```

On a machine without a browser, `deployctl login --no-browser` prints the URL to open elsewhere; the redirect still has to reach the loopback listener, so this works over SSH only with port forwarding. *(proposed: device authorization flow as a fallback.)*

## `status`

```bash
deployctl status                 # all apps
deployctl status backend         # one app, all environments
deployctl status backend --env prod
```

```
APP        ENV    SYNC     HEALTH    IMAGE TAG   REPLICAS   ACTIVE REQUEST
backend    dev    Synced   Healthy   a1b2c3d     3/3
backend    prod   Synced   Healthy   9f8e7d6     5/5        0b8e (awaiting_approval)
frontend   dev    Synced   Healthy   a1b2c3d     1/1
```

`IMAGE TAG` is what is **running** (from pods). If it differs from what Git says, the column shows both, for example `a1b2c3d → e4f5a6b`, which means a rollout is in progress or stuck.

## `deploy`

```bash
deployctl deploy backend --env dev --tag e4f5a6b
```

```
✔ Request 0b8e created
✔ validate        a1b2c3d → e4f5a6b
✔ git_commit      pushed 3c2d1e0
✔ argocd_refresh
⏳ wait_healthy    2/3 pods ready
✔ Deployed in 48s
```

| Flag | Description |
|---|---|
| `--env <name>` | Target environment (required) |
| `--tag <tag>` | Image tag to deploy (required) |
| `--reason <text>` | Why. Shown to approvers *(proposed)* |
| `--dry-run` | Validate and show the planned change. Nothing is stored or committed |
| `--no-wait` | Return once the request is accepted, without streaming progress |
| `--timeout <dur>` | How long the CLI follows progress before exiting with code `4` or `1`. The request itself keeps running. Default: until the request ends |

For environments that require approval, the CLI shows the pause and who can approve:

```
✔ Request 0b8e created
✔ validate        9f8e7d6 → e4f5a6b
⏸ approval_gate   waiting for approval
                  approvers: Priya, Sam (deployctl-approvers)
                  ask them to run: deployctl approve 9c41
```

It keeps following the stream. Ctrl-C detaches **without cancelling**; reattach with `deployctl request 0b8e --watch`.

When a rollout fails, the CLI prints the diagnostics:

```
✖ wait_healthy    timed out after 5m0s (1/3 pods ready)

  Events (backend-7d9f8c-x2kq):
    Warning  BackOff   Back-off restarting failed container backend
  Logs (backend-7d9f8c-x2kq, previous):
    panic: missing env INVOICE_ROUNDING_MODE
  
✖ Request 0b8e failed. The new tag is still in Git.
  Roll back:  deployctl rollback backend --env dev
  Retry:      deployctl retry 0b8e
```

## `scale`

```bash
deployctl scale backend --env dev --replicas 5
```

If an HPA targets the workload, `validate` fails with a message to change the HPA's min and max instead, because a fixed `replicaCount` and an HPA fight each other.

## `rollback`

```bash
deployctl rollback backend --env prod
deployctl rollback backend --env prod --dry-run     # see which tag it would pick
```

Sets the tag to the last successfully deployed tag that differs from the current one, with a new commit. Production rollbacks need approval like any other production change.

## Requests

```bash
deployctl requests                              # recent requests you can see
deployctl requests --app backend --status failed
deployctl requests --mine
deployctl request 0b8e                          # detail with steps, errors, diagnostics
deployctl request 0b8e --watch                  # stream progress
deployctl retry 0b8e                            # resume a failed request from the failed step
deployctl cancel 0b8e                           # only before git_commit
```

IDs can be shortened to any unique prefix of at least 4 characters.

## Approvals

```bash
deployctl approvals                              # pending approvals you can decide
deployctl approve 9c41 --comment "Looks good"
deployctl reject 9c41 --comment "Wrong tag"      # comment required
```

```
$ deployctl approvals
ID     APP      ENV   CHANGE                REQUESTED BY   AGE   WARNINGS
9c41   backend  prod  deploy 9f8e7d6→e4f5a6b Linh Tran      3m
```

`approve` and `reject` also accept the **request** ID, since that is what the requester usually pastes into chat. *(proposed)*

## Audit

```bash
deployctl audit --app backend --since 7d
deployctl audit --actor me --action approval.approved
```

Requires the `approver` or `admin` role.

## Global flags and environment

| Flag or variable | Description |
|---|---|
| `--api <url>` / `DEPLOYCTL_API_URL` | API address |
| `--output json` / `-o json` | Machine-readable output (the API's JSON, one object per line when streaming) |
| `DEPLOYCTL_TOKEN` | Bearer token for non-interactive use. Overrides stored login |
| `--no-color` / `NO_COLOR` | Disable colour |
| `--verbose` / `-v` | Log HTTP calls (without tokens) to stderr |

## Output and exit codes

Human output goes to stdout, progress and warnings to stderr, so `-o json` output can be piped.

| Code | Meaning |
|---|---|
| `0` | Success (including `no_change`) |
| `1` | Request failed (unhealthy rollout, timeout, Git error), or was rejected or cancelled |
| `2` | Invalid usage or configuration |
| `3` | Not authenticated or not authorised |
| `4` | Waiting for approval (with `--no-wait`, or `--timeout` reached while waiting) |
| `5` | Conflict: another request is active for this app and environment *(proposed)* |

## Using it from CI

A typical pipeline after the image is pushed:

```yaml
# GitHub Actions example
- name: Deploy to dev
  env:
    DEPLOYCTL_API_URL: https://deployctl.example.com
    DEPLOYCTL_TOKEN: ${{ steps.oidc.outputs.token }}   # short-lived token for the CI identity
  run: |
    deployctl deploy backend --env dev --tag "${GITHUB_SHA::7}" \
      --reason "CI: ${GITHUB_REF_NAME}" --timeout 10m
```

- Use a **machine identity**, not a person's token.
- Send the same idempotency key on pipeline re-runs to avoid duplicate requests: `--idempotency-key "$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"` *(proposed)*.
- Exit code `0` covers `no_change`, so re-running a pipeline for an already deployed tag is safe.
- Make the CI workflow that *builds* images ignore changes to the config repo paths, so deployctl's commits do not trigger builds.
