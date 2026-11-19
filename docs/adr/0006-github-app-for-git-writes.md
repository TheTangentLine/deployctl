# 0006. A GitHub App for Git writes

- Status: Accepted
- Date: 2026-11-19

## Context

The worker must push commits to the config repo on GitHub. Options for the credential:

| Credential | Scope | Lifetime | Tied to a person |
|---|---|---|---|
| Classic personal access token | Every repo the user can access | Long, often no expiry | Yes |
| Fine-grained PAT | Selected repos and permissions | Up to a year | Yes |
| Deploy key (SSH) | One repo, read or read/write | No expiry | No |
| GitHub App installation token | Repos the App is installed on, with the App's permissions | 1 hour | No |

## Decision

Use a **GitHub App** installed only on the config repo, with **Contents: read and write** and **Metadata: read**. The worker holds the App's private key and mints a one-hour installation token, refreshed before expiry.

## Consequences

- No credential is tied to an employee; nobody leaving breaks deploys.
- The token the worker actually uses on the wire expires within an hour; a leaked token has a short life.
- The private key is long-lived and is the real secret. It must be stored as a Kubernetes Secret or in a secrets manager, mounted only into worker pods, and rotated (GitHub supports several active keys for zero-downtime rotation).
- Commits are attributed to the App's bot identity. Human attribution lives in commit trailers (`Requested-by`, `Approved-by`) and in deployctl's audit log.
- Branch protection can name the App in its bypass list, so humans can be forced through PRs while deployctl pushes directly ([0012](0012-direct-push-before-pull-request-mode.md)).
- Ties deployctl to GitHub. Supporting GitLab or others later means an equivalent (for example a GitLab project access token) behind the `internal/git` interface.

## Alternatives considered

- **Personal access token.** Rejected: tied to a person and usually broader than one repo.
- **Deploy key.** Scoped to one repo and simple, but no expiry, SSH-only, and cannot be named in branch protection bypass lists in the same way. Acceptable fallback for non-GitHub hosts.
