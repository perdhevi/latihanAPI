# Hardening roadmap

This repository is the base code for a tutorial series: taking a working Go REST
API to a hardened service that a reader can clone and deploy **without code
changes**, configuring everything through `.env` and Compose.

Each phase is tagged (`phase-00`, `phase-01`, …) so readers can
`git diff phase-02..phase-03` alongside the matching blog post. Every post follows
the same shape: threat → before → after → a test that proves it → trade-offs.

## Deployment target

A single VM running Docker Compose, with images pulled from any OCI registry
(ECR, GHCR, Docker Hub). No platform lock-in.

```text
GitHub Actions → build, scan, SBOM, sign → registry
                                              ↓ pull by digest
VM: Caddy (TLS :443) → latihan-api → PostgreSQL   (only Caddy publishes ports)
```

## Phases

| Tag | Topic | Outcome | Status |
| --- | --- | --- | --- |
| phase-00 | Baseline and threat model | Module path `github.com/perdhevi/latihanAPI`; STRIDE review of the API | Done (STRIDE doc pending) |
| phase-01 | Guardrails | golangci-lint (gosec, errorlint, noctx, …), govulncheck, SHA-pinned Actions, Dependabot | Done |
| phase-02 | Pluggable authentication | `auth` contract module, JWKS verifier, `firebase` / `cognito` / `oidc` providers, conformance kit, identity linking | Done |
| phase-03 | Authorization | Owner comes from the token only (`user_id` removed from input, `/users/me`); owner-scoped SQL; 404 for other users' records; cross-user test suite | Done |
| phase-04 | Built-in token issuer | `jwt` provider: register/login/refresh/logout, Ed25519 keys with rotation, argon2id, refresh-token rotation with reuse detection, lockout; multiple providers at once; zero-account Compose quickstart | Done |
| phase-05 | Abuse resistance | Rate limits (429 + `Retry-After`), cursor pagination, `statement_timeout`, header and concurrency caps | Planned |
| phase-06 | Safe retries | `Idempotency-Key` on POST, `ETag` / `If-Match` on PUT | Planned |
| phase-07 | Observability | OpenTelemetry traces and metrics, panic stacks, admin port for metrics/pprof | Planned |
| phase-08 | Secrets, TLS, least privilege | `*_FILE` secrets, Caddy TLS, `sslmode=verify-full`, migrator/app DB roles, optional Postgres row-level security as a second ownership layer | Planned |
| phase-09 | Shipping | Distroless image by digest, SBOM, cosign, hardened Compose, VM baseline, deploy workflow | Planned |
| phase-10 | Proving it | Fuzzing, OpenAPI contract tests, load tests, cross-user access tests | Planned |
| phase-11 | Health data responsibly | Export, erasure, audit log, backups and restore drills | Planned |

## Authentication design

**Principle:** providers answer *who is this?*; the core decides *what may they
touch?* Providers never see the database or make authorization decisions.

### Selection is configuration, not code

`AUTH_PROVIDER` is required. There is no default and no fallback: a missing or
unknown value, or invalid provider settings, stop the service before it serves any
request, and the error lists the registered names. (The database opens first,
because the built-in provider stores its accounts there.)

| `AUTH_PROVIDER` | Who signs tokens | Required settings |
| --- | --- | --- |
| `firebase` | Google | `AUTH_FIREBASE_PROJECT_ID` |
| `cognito` | AWS | `AUTH_COGNITO_REGION`, `AUTH_COGNITO_USER_POOL_ID`, `AUTH_COGNITO_CLIENT_ID`, `AUTH_COGNITO_TOKEN_USE` (`access` or `id`) |
| `oidc` | Any OIDC issuer (Keycloak, Auth0, Zitadel, …) | `AUTH_OIDC_ISSUER`, `AUTH_OIDC_AUDIENCE`, optional `AUTH_OIDC_JWKS_URL`, `AUTH_OIDC_ALGORITHMS` |
| `jwt` | This service | `AUTH_JWT_ISSUER`, `AUTH_JWT_KEY_FILE`, optional previous keys, audience and token lifetimes |

`firebase` and `cognito` are thin presets over one shared verifier
(`internal/authn/jwks`); they only derive the issuer, audience rule and key URL:

- Firebase: issuer `https://securetoken.google.com/<project>`, audience `<project>`,
  RS256 keys from Google's `securetoken` JWKS endpoint.
- Cognito: issuer `https://cognito-idp.<region>.amazonaws.com/<pool>`, keys from
  `<issuer>/.well-known/jwks.json`. Access tokens carry `client_id` instead of
  `aud`, so the preset checks `client_id` and `token_use=access`; ID tokens check
  `aud` and `token_use=id`.
- OIDC: keys come from the discovery document unless `AUTH_OIDC_JWKS_URL` is set;
  the document must name the configured issuer exactly.

The verifier accepts only the configured asymmetric algorithms, requires `iss`,
the audience claim, `exp` and a non-empty `sub`, rejects `iat`/`nbf` in the future,
allows 30 seconds of clock leeway and refuses tokens over 8 KiB. It also matches
the key type to the algorithm itself, independently of the JWT library. Keys are
cached for an hour, refetched for unknown key IDs at most once a minute, and kept
through key-server outages.

### Identity linking

External subjects are strings, not our UUIDs. `user_identities (issuer, subject)`
maps each identity to one internal user; a user may have several identities.
`POST /api/v1/users` creates a profile and links the caller's identity in one
transaction. Every other `/api/v1` route requires a linked identity and answers
403 `profile_required` otherwise. Changing providers keeps data: link the new
identity to the existing user.

### Code-level extension (requires a rebuild)

```text
github.com/perdhevi/latihanAPI/auth   separate module, stdlib-only: Identity,
                                      Authenticator, optional RouteRegistrar and
                                      IssuerBound, Deps (incl. *sql.DB),
                                      Deps, Register, New, BearerToken
auth/authtest                         conformance suite for JWT-based providers
cmd/api/plugins.go                    blank imports of every compiled-in provider
```

Add a provider by running `go get` on its module, adding its import to
`plugins.go` and rebuilding the image. Providers that serve their own endpoints
(login, key publication) implement `RouteRegistrar`; those routes are mounted
without authentication.

### Built-in `jwt` provider (phase-04)

- Email and password accounts in `local_credentials`, separate from profiles: the
  credential ID is the token subject, linked like any external identity.
- Ed25519 access tokens (15 minutes, `typ: at+jwt`) verified by the shared JWKS
  verifier; key IDs are RFC 7638 thumbprints; `/.well-known/jwks.json` publishes
  current and previous keys. `api keygen` creates keys and never overwrites.
- Opaque refresh tokens (256 bits, SHA-256 at rest, 30 days) rotated on every use;
  reusing one revokes its family. Row locks make concurrent reuse detectable.
- argon2id (OWASP minimum parameters) with a four-hash concurrency cap, rehash on
  login when parameters change, equal-cost answers for unknown or locked accounts,
  and a 15-minute lock after five failures.
- Dropped from the plan: separate `KeySource` / `CredentialStore` plugins. Replacing
  the whole provider covers those cases with less machinery.
- Not included: email verification, password reset, account deletion (they need
  outbound email).
- `AUTH_PROVIDER` accepts a list (`firebase,jwt`): the unverified `iss` claim only
  chooses the verifier, which then checks everything.
