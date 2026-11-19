# 0012. Push directly first, pull request mode later

- Status: Proposed
- Date: 2026-11-19

## Context

There are two ways for the worker to get a change into the tracked branch:

1. **Direct push** to `main`.
2. **Pull request**: push a branch, open a PR, wait for it to be merged (by a human or by auto-merge after checks).

Many organisations protect `main` so that every change needs a reviewed PR. deployctl's approval step plays the same role as review for the changes it makes, but branch protection does not know that.

## Decision

v1 pushes **directly** to the tracked branch. Branch protection is configured so that **only the GitHub App** may bypass it, and humans go through PRs. Pull request mode is on the roadmap as an alternative `git_commit` strategy (`git.mode: pull_request`), with a `wait_merged` step.

## Consequences

- Simple, fast pipeline: one push, no merge wait, no PR noise for routine dev deploys.
- Approvals happen in deployctl, not in GitHub. Teams that want approval inside GitHub must wait for pull request mode.
- Requires an admin to add the App to the bypass list. If that is not allowed by policy, deployctl cannot be used until pull request mode exists. `git_commit` fails with a clear message (`GH006`) in that case.
- Combined with "humans use PRs", the tracked branch has exactly two kinds of writers: reviewed humans and deployctl. That makes "who changed this" easy to answer.

## Alternatives considered

- **Pull request mode only.** Safer by policy, but adds minutes of latency and a GitHub-side approval that duplicates deployctl's. Rejected for v1.
- **Direct push without branch protection.** Rejected: anyone with write access could bypass deployctl entirely ([security.md](../reference/security.md#threat-model), T8).
