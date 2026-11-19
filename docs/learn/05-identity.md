# 5. Who are you? Identity

*Tuesday, 9:15.*

Linh installs the `deployctl` binary and types the first command Sam gave them:

```bash
$ deployctl status
Error: not logged in. Run `deployctl login`.
```

"Before deployctl does anything for you, it needs to answer two questions," Sam says. "**Who are you?** That's authentication. **What may you do?** That's authorization. Today we do both, starting with the first."

## Authentication versus authorization

These two words are often blurred, so fix them now:

- **Authentication** (often "authn"): proving who you are. The answer is an identity: "this is Linh".
- **Authorization** ("authz"): deciding whether that identity may do a specific thing. The answer is yes or no: "Linh may deploy to dev, but not approve production changes".

A building is a good picture. Showing your badge at the door is authentication. Whether your badge opens the server room is authorization. They are different checks, done by different parts of the system, and mixing them up causes real bugs.

## Why not a password?

deployctl could keep its own list of usernames and passwords. It deliberately does not:

- Every system with its own passwords is another place for passwords to leak and another account to forget to delete when someone leaves.
- The company already has a central login, with single sign-on and multi-factor authentication, where joiners and leavers are handled once.

So deployctl **delegates** authentication to the company's **identity provider** (IdP). The team uses Keycloak; Okta, Microsoft Entra ID, Google and others work the same way. deployctl never sees Linh's password.

## OIDC and tokens

The standard that lets an app delegate login to an identity provider is **OpenID Connect** (**OIDC**), built on top of **OAuth 2.0**. The core idea is a **token**: after you log in at the identity provider, it gives your app a token that says, in effect, "the bearer of this is Linh, signed, the IdP, valid until 9:30".

The app then sends that token with every request, and the receiving service checks it. No passwords travel after the initial login, and the token expires quickly, so a stolen token is only useful for a short time.

### What a token looks like: JWT

The tokens are **JSON Web Tokens** (**JWT**, pronounced "jot"). A JWT is three Base64URL-encoded parts separated by dots:

```
eyJhbGciOiJSUzI1NiIsImtpZCI6Ims0In0.eyJpc3MiOiJodHRwczovL2tleWNsb2FrLmV4YW1wbGUuY29tL3JlYWxtcy9kZXBsb3ljdGwiLCJzdWIiOiJmM2MxLi4uIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c
└──────────── header ──────────────┘ └──────────────────────────── payload ───────────────────────────────┘ └──────────── signature ────────────┘
```

Decoded, the header and payload are plain JSON:

```json
// header
{ "alg": "RS256", "kid": "k4", "typ": "JWT" }

// payload ("claims")
{
  "iss": "https://keycloak.example.com/realms/deployctl",
  "sub": "f3c1a9e2-7b4d-4c1e-9a2f-5d8e6b7c3a10",
  "aud": "deployctl",
  "exp": 1795600200,
  "iat": 1795599300,
  "name": "Linh Tran",
  "email": "linh@example.com",
  "groups": ["deployctl-developers"]
}
```

Each field in the payload is a **claim**:

| Claim | Meaning |
|---|---|
| `iss` (issuer) | Which identity provider issued it |
| `sub` (subject) | A stable, unique ID for the user **within that issuer**. Never changes, even if the email does |
| `aud` (audience) | Which service the token is meant for |
| `exp` (expiry) | Unix time after which it is invalid; here 15 minutes after `iat` |
| `iat` (issued at) | When it was issued |
| `groups` | Groups the user belongs to (a custom claim the IdP is configured to add) |

Two things to notice:

1. **A JWT is signed, not encrypted.** Anyone holding it can read the claims. Never put secrets in a token, and treat the token itself like a password, because whoever holds it *is* you until it expires.
2. **The signature is what makes it trustworthy.** Change one character in the payload and the signature no longer matches.

### How the API checks a token

The identity provider signs tokens with a **private key** that only it has. It publishes the matching **public keys** at a well-known URL as a **JWKS** (JSON Web Key Set). The `kid` in the header says which key was used.

When a request arrives, deployctl's API:

1. Reads the token from the `Authorization: Bearer ...` header.
2. Rejects it if the `alg` is not one it expects (an old trick is a token with `"alg": "none"` and no signature).
3. Looks up the public key by `kid` in its cached copy of the JWKS.
4. Verifies the signature with that public key.
5. Checks `iss` is the expected issuer, `aud` includes `deployctl`, and `exp` is in the future.
6. Reads `sub` and `groups`.

No call to the identity provider is needed per request. The API downloaded the public keys once and keeps them cached. This is also why, if the identity provider goes down, people who are already logged in can keep working until their tokens expire, while new logins fail.

> **Aside: public-key signatures in one paragraph.** A key pair is two numbers that belong together. What the private key signs, the public key can verify, but the public key cannot be used to sign. So the identity provider can hand its public key to every service in the company, and none of them can forge a token. Only the holder of the private key can.

## Logging in from a terminal: PKCE

Linh runs:

```bash
$ deployctl login
Opening browser to log in...
```

A browser tab opens on the company login page. Linh enters their password and taps the MFA prompt on their phone. The tab says "You can close this window", and the terminal prints:

```
Logged in as Linh Tran <linh@example.com>
roles: developer, viewer
```

Under the hood, this is the OIDC **Authorization Code flow with PKCE** (Proof Key for Code Exchange, pronounced "pixy"). It solves a specific problem: a CLI cannot keep a secret, because anyone can take the binary apart. So how does the identity provider know the code it hands back is going to the same program that asked for it?

The coat-check analogy works well:

1. Before going to the counter, the CLI invents a random secret, the **code verifier**, and keeps it.
2. It computes a hash of it, the **code challenge**, and sends only the hash along with the login request. The identity provider remembers the hash.
3. Linh logs in in the browser. The identity provider redirects the browser to `http://127.0.0.1:<random port>/callback?code=...`. The CLI is listening on that port on Linh's own machine, so it receives the **authorization code**, a one-time ticket.
4. The CLI exchanges the code for tokens, and sends the original **verifier** with it. The identity provider hashes the verifier and checks it matches the challenge from step 2.

Someone who intercepted the code in step 3 cannot redeem it, because they do not have the verifier, and they cannot work it out from the hash.

```mermaid
sequenceDiagram
    participant CLI as deployctl (Linh's laptop)
    participant B as Browser
    participant IdP as Identity provider

    CLI->>CLI: verifier = random(), challenge = SHA256(verifier)
    CLI->>B: open IdP login page (challenge, redirect to 127.0.0.1:port)
    B->>IdP: Linh logs in, with MFA
    IdP-->>B: redirect to 127.0.0.1:port with one-time code
    B->>CLI: code arrives at the local listener
    CLI->>IdP: exchange code + verifier
    IdP->>IdP: SHA256(verifier) == challenge?
    IdP-->>CLI: access token, refresh token
```

The CLI ends up with two tokens:

- an **access token**: the JWT above, short-lived (15 minutes), sent to the API with every call,
- a **refresh token**: longer-lived, used only to get new access tokens from the identity provider without logging in again.

Both go into the operating system's **keychain**, the same secure store your browser uses for saved passwords, not a plain text file.

## From groups to roles

The API now knows that the caller is `sub = f3c1...` from the company's issuer, and that they are in the group `deployctl-developers`. deployctl's configuration maps groups to roles:

```yaml
auth:
  roleMapping:
    viewer:    [deployctl-viewers]
    developer: [deployctl-developers]
    approver:  [deployctl-approvers]
    admin:     [deployctl-admins]
```

So Linh is a `developer`. deployctl does not store this; it reads it from the token on every request. When Linh someday leaves the team and is removed from the group, their access ends within fifteen minutes, when the current token expires, without anyone touching deployctl.

```bash
$ deployctl whoami
Linh Tran <linh@example.com>
roles: developer, viewer
issuer: https://keycloak.example.com/realms/deployctl
```

## Machines are users too

CI pipelines also call deployctl, to deploy every merge to dev automatically. They must not use a person's login. Instead, each pipeline has its own **machine identity** in the identity provider, and gets its own short-lived token (many CI systems can mint one per job, so no long-lived secret is stored at all). The token goes in `DEPLOYCTL_TOKEN`. To deployctl, the pipeline is just another principal with groups and roles, usually limited to non-production environments.

"Remember the stolen laptop?" Sam says. "If it happened today, what would the thief have?"

Linh thinks. "A refresh token in the keychain, which is locked with my laptop password. Even if they got it, it gets them a developer role. They could deploy existing images to dev until someone revokes my session. They couldn't deploy to prod alone, because that needs an approver. And they'd have nothing that talks to the cluster or GitHub directly."

"That's the whole point of the next chapter."

---

## Check yourself

<details>
<summary>1. Is a JWT secret? Can someone who intercepts one read Linh's email from it?</summary>

The contents are not secret: they are only Base64-encoded and anyone can decode them, so yes, they can read the email. But the token as a whole must be protected, because whoever holds a valid token can use it as Linh until it expires.
</details>

<details>
<summary>2. Why does deployctl identify users by <code>(iss, sub)</code> and not by email?</summary>

Emails can change (name change, domain change) and can be reassigned to a different person later. `sub` is a stable, unique ID that the identity provider never reuses, scoped to its issuer.
</details>

<details>
<summary>3. The identity provider is down for an hour. What still works?</summary>

Requests with tokens that are still valid, because the API checks signatures with cached public keys and does not need to call the identity provider. New logins and token refreshes fail, so people drop off as their access tokens expire.
</details>

<details>
<summary>4. What attack does PKCE prevent?</summary>

Interception of the authorization code. Without PKCE, anything that could see the redirect (another program on the machine listening for it, a malicious browser extension, logs) could exchange the code for tokens. With PKCE, the exchange requires the code verifier, which never left the CLI.
</details>

Next: [6. What may you do? Access and least privilege](06-access-and-least-privilege.md)
