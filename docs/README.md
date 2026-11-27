# deployctl documentation

> **Status:** M0 is implemented: the steps and the Git, GitHub App, Argo CD and Kubernetes clients (see the [roadmap](reference/roadmap.md)). The API, worker, store, auth and CLI are still design. Where a reference document describes something that exists, the code and the document must agree. Details marked *(proposed)* go beyond the original design and are open for review.

The docs come in three parts. Pick the one that matches why you are here.

## [Learn](learn/): the story track

**For:** people new to containers, Kubernetes, GitOps, cloud identity or security, or new to the team.

A narrative that follows a new developer through their first week: from "how do I deploy?" to approving a production rollback. Each chapter introduces the background concepts as they are needed and ends with self-check questions. About two hours.

1. [The problem](learn/01-the-problem.md): why deployctl exists
2. [Containers and images](learn/02-containers-and-images.md)
3. [Kubernetes in an afternoon](learn/03-kubernetes.md)
4. [GitOps and Argo CD](learn/04-gitops-and-argocd.md)
5. [Who are you? Identity](learn/05-identity.md)
6. [What may you do? Access and least privilege](learn/06-access-and-least-privilege.md)
7. [The first deploy, step by step](learn/07-first-deploy.md)
8. [When things break](learn/08-when-things-break.md)
9. [Going to production](learn/09-production.md)
10. [Glossary](learn/glossary.md)

## [Reference](reference/): the specification

**For:** people building, operating or reviewing deployctl.

| Document | Covers |
|---|---|
| [Architecture](reference/architecture.md) | Components, privilege boundaries, communication, topology, code layout, interfaces |
| [Request lifecycle](reference/request-lifecycle.md) | Request and step state machines, each step in detail, the worker loop, leases, retries, cancel, approvals, concurrency, idempotency, progress streaming |
| [Security](reference/security.md) | Authentication, authorization policy, credentials per component, audit, threat model, hardening checklist, known gaps |
| [HTTP API](reference/api.md) | Conventions, errors, every endpoint with request and response shapes |
| [CLI](reference/cli.md) | Every command, flags, output, exit codes, CI usage |
| [Configuration](reference/configuration.md) | Config file fields, validation, environment variables, config repo and Argo CD layout |
| [Data model](reference/data-model.md) | Schema as SQL, the constraints that enforce the design, key queries, grants, retention, migrations |
| [Operations](reference/operations.md) | Deployment, upgrades, metrics, alerts, failure modes, runbooks, credential rotation, recovery |
| [Development](reference/development.md) | Local setup, dev auth, project structure, coding guidelines, YAML editing, testing strategy |
| [Roadmap](reference/roadmap.md) | Milestones with done-criteria |

## [Decisions](adr/): architecture decision records

**For:** anyone asking "why is it like this?"

Twelve short records of the major design choices, each with the alternatives that were rejected: Git as the only write path, Postgres as the queue, OIDC roles from groups, the GitHub App, no automatic rollback, and more. See the [index](adr/README.md).

## Suggested reading paths

| You are... | Read | Time |
|---|---|---|
| New to GitOps and Kubernetes | Learn 1–9 in order | ~2 h |
| A developer who will *use* deployctl | Learn 1, 7, 8, 9, then [CLI](reference/cli.md) | ~1 h |
| An engineer who will *build* deployctl | Learn 7 and 8 for intuition, then [Architecture](reference/architecture.md), [Request lifecycle](reference/request-lifecycle.md), [Data model](reference/data-model.md), [Development](reference/development.md), and the ADRs | ~2 h |
| Reviewing security | Learn 5 and 6, then [Security](reference/security.md), [ADR 0001](adr/0001-git-is-the-only-write-path.md), [0005](adr/0005-identity-from-oidc-roles-from-groups.md), [0006](adr/0006-github-app-for-git-writes.md) | ~1 h |
| Running it in production | [Architecture](reference/architecture.md#runtime-topology), [Configuration](reference/configuration.md), [Operations](reference/operations.md), [Security hardening checklist](reference/security.md#hardening-checklist) | ~1 h |

## Contributing to the docs

- The **reference** docs are normative. If the code and the reference disagree, one of them is a bug; fix one in the same pull request as the other.
- New significant decisions get an **ADR** before or alongside the code.
- The **learn** track should stay a story. If a new feature changes what Linh would experience, update the relevant chapter, and keep the check-yourself answers accurate.
