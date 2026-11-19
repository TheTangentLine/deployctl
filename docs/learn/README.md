# Learn deployctl: a week on the Reconcile team

This is the story track. It is for people who are new to some or all of: containers, Kubernetes, GitOps, cloud identity and security. It explains those ideas from the ground up by following one new developer through their first week, from "how do I deploy?" on Monday to approving a production rollback on Friday.

If you already know the background and want the specification, go to the [reference docs](../reference/) instead.

## The cast

- **Linh** joined the Reconcile team this Monday as a backend developer. They know how to write Go and use Git, and have never touched Kubernetes.
- **Sam** is the team's platform engineer. Sam set up the cluster, Argo CD and deployctl, and remembers why each piece exists.
- **Priya** is the tech lead. Priya reviews production changes and is one of the people who can approve them.

The product they work on is **Reconcile**, a small web app that matches incoming bank payments to open invoices. It has two parts: `backend` (a Go API) and `frontend` (a web UI). It runs in two environments: `dev`, where anything goes, and `prod`, where customers are.

## How the week goes

| Chapter | Day | What Linh learns | Time |
|---|---|---|---|
| [1. The problem](01-the-problem.md) | Monday | Why the team stopped deploying by hand, and what deployctl is for | 10 min |
| [2. Containers and images](02-containers-and-images.md) | Monday | What is actually being deployed: images, tags, registries | 12 min |
| [3. Kubernetes in an afternoon](03-kubernetes.md) | Monday | Pods, Deployments, rolling updates, readiness, and the reconciliation loop | 20 min |
| [4. GitOps and Argo CD](04-gitops-and-argocd.md) | Monday | Desired state in Git, Argo CD syncing it, drift and self-healing | 15 min |
| [5. Who are you? Identity](05-identity.md) | Tuesday | Logging in with OIDC, what a JWT is, how the API trusts it | 15 min |
| [6. What may you do? Access and least privilege](06-access-and-least-privilege.md) | Tuesday | Roles, approvals, scoped credentials, audit logs, blast radius | 15 min |
| [7. The first deploy, step by step](07-first-deploy.md) | Tuesday | Everything that happens between pressing Enter and "Deployed in 48s" | 20 min |
| [8. When things break](08-when-things-break.md) | Wednesday | Crashes, races, retries, a bad deploy and a rollback | 15 min |
| [9. Going to production](09-production.md) | Thu–Fri | Approvals, the audit trail, a production rollback, and the week in review | 15 min |
| [Glossary](glossary.md) | — | Every term in one place | — |

About two hours end to end. Each chapter ends with a few **check yourself** questions with hidden answers. If you can answer them without peeking, you have the idea.

## How to read

- Read in order. Each chapter builds on the previous one, and the story carries a few threads (a bug fix, a mysterious incident from last year) through to the end.
- You do not need a cluster to follow along. Command output is shown in the text. If you do want to try things, [development.md](../reference/development.md) explains how to set up a local cluster.
- **Bold** marks a term the first time it is defined. They are all in the [glossary](glossary.md).
- Boxes like this one hold side notes you can skip on a first read:

> **Aside.** Side notes go here.

A note on status: deployctl is in the design stage. The story describes how it is designed to behave. Where the design is still open, the reference docs say so.
