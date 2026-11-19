# 0004. One binary, separate processes

- Status: Accepted
- Date: 2026-11-19

## Context

deployctl has three roles: an HTTP API, a background worker, and a CLI client. They share types (request kinds, step names, statuses, API payloads) and, for API and worker, database access code. They have very different privilege needs: the worker can push to Git; the API must not.

## Decision

Build **one Go binary** with subcommands: `deployctl api`, `deployctl worker`, `deployctl migrate`, and client commands (`deploy`, `status`, …).

Run API and worker as **separate processes and separate Kubernetes Deployments**, each with only its own credentials. Enforce the separation in three places:

1. Credentials: only the worker's Deployment mounts the GitHub App key and the Argo CD `sync` token.
2. Startup checks: `deployctl api` refuses to start if worker-only variables are set.
3. Build-time rule: `internal/api` may not import `internal/git` (`depguard`).

## Consequences

- Shared types are compiled together; API and worker from the same release cannot disagree on the meaning of a status.
- One artefact to build, sign and ship.
- Users install the same binary as the server; the CLI version and server version are visibly the same thing.
- The binary contains code it does not use in a given mode (for example, the CLI contains the worker). Size is a minor cost; the security cost is addressed by credential separation, which is what actually limits capability.
- During rolling upgrades, old and new versions of API and worker run together, so schemas and step outputs must stay backward compatible for one release.

## Alternatives considered

- **API and worker in one process.** Simpler to run, but the process would hold Git write credentials while serving internet traffic. Rejected.
- **Separate binaries and repos.** Strong separation, but duplicated types and version skew. Rejected for a project this size.
- **Separate binaries from one repo.** Viable. Rejected only because it adds build complexity without improving security beyond what credential separation already gives.
