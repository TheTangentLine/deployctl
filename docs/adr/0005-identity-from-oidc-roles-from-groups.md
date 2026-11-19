# 0005. Identity from OIDC, roles from group claims

- Status: Accepted
- Date: 2026-11-19

## Context

deployctl needs to know who is calling and what they may do. The organisation already has an identity provider (Keycloak, or any OIDC provider) where people log in with SSO and MFA, and where team membership is managed as groups. Joiners and leavers are handled there.

## Decision

- **Authentication:** OIDC. The CLI uses the Authorization Code flow with PKCE and a loopback redirect. Machines use the client credentials flow or an injected short-lived token. The API verifies JWTs against the provider's JWKS.
- **Identity key:** `(issuer, sub)`. Never email.
- **Authorization:** four fixed roles (`viewer`, `developer`, `approver`, `admin`), derived on every request from the token's groups claim through a mapping in the config file. deployctl stores no memberships.
- **Policy:** one function, `Policy.Authorize(principal, action, app, env)`, plus ownership helpers, all in `internal/auth`, all covered by one table test.
- **"Production"** is any environment with `requiresApproval: true`, never a name match.

## Consequences

- No passwords, no user management UI, no membership sync jobs. Removing someone from a group removes their access when their access token expires (minutes).
- MFA, SSO and session policies are inherited from the identity provider.
- The role mapping is reviewed in Git like other config.
- Per-app permissions are not possible in v1; every developer can deploy every non-prod environment. The policy signature already takes `app` so this can be added.
- deployctl trusts the identity provider completely. A compromised IdP admin can grant any role. This is out of scope for deployctl's threat model.
- Token lifetime bounds how quickly revocation takes effect. Short access tokens (5–15 minutes) are recommended.

## Alternatives considered

- **Local users and API keys in deployctl.** Rejected: a second identity system to manage, and joiners/leavers drift.
- **Kubernetes RBAC as the authority** (deployctl checks `SubjectAccessReview`). Rejected: ties deploy permissions to cluster permissions, which is what we are trying to separate.
- **Policy engine (OPA, Cedar).** Powerful, but six actions and four roles fit in a Go switch that is easy to read and test. Revisit if per-app or attribute-based rules arrive.
