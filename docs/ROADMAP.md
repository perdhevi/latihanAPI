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

| Tag | Topic | Outcome |
| --- | --- | --- |
| phase-00 | Baseline and threat model | STRIDE review of the current API; module path `github.com/perdhevi/latihanAPI` |
| phase-01 | Guardrails | golangci-lint (gosec, errcheck, bodyclose), govulncheck, SHA-pinned Actions, Dependabot |
| phase-02 | Pluggable authentication | `auth` contract module, built-in providers, fail-closed startup, conformance kit |
| phase-03 | Authorization | `user_id` comes from the principal only; owner-scoped SQL; 404 for other users' records; optional RLS |
| phase-04 | Abuse resistance | Rate limits (429 + `Retry-After`), cursor pagination, `statement_timeout`, header and concurrency caps |
| phase-05 | Safe retries | `Idempotency-Key` on POST, `ETag` / `If-Match` on PUT |
| phase-06 | Observability | OpenTelemetry traces and metrics, panic stacks, admin port for metrics/pprof |
| phase-07 | Secrets, TLS, least privilege | `*_FILE` secrets, Caddy TLS, `sslmode=verify-full`, migrator/app DB roles |
| phase-08 | Shipping | Distroless image by digest, SBOM, cosign, hardened Compose, VM baseline, deploy workflow |
| phase-09 | Proving it | Fuzzing, OpenAPI contract tests, load tests, cross-user access tests |
| phase-10 | Health data responsibly | Export, erasure, audit log, backups and restore drills |

## Authentication design (phase-02)

**Principle:** providers answer *who is this?*; the core decides *what may they
touch?* Authorization never lives in a provider.

### Selection is configuration, not code

`AUTH_PROVIDER` is required. There is no default and no fallback: an unknown or
missing value stops the service at startup and lists the registered names.

| `AUTH_PROVIDER` | Who signs tokens | Required settings |
| --- | --- | --- |
| `jwt` | This service (Ed25519, `kid` rotation, JWKS published) | `AUTH_JWT_ISSUER`, `AUTH_JWT_AUDIENCE`, signing key file |
| `firebase` | Google | `AUTH_FIREBASE_PROJECT_ID` |
| `cognito` | AWS | `AUTH_COGNITO_REGION`, `AUTH_COGNITO_USER_POOL_ID`, `AUTH_COGNITO_CLIENT_ID`, `AUTH_COGNITO_TOKEN_USE` (`access` or `id`) |
| `oidc` | Any OIDC issuer (Keycloak, Auth0, Zitadel, …) | `AUTH_OIDC_ISSUER`, `AUTH_OIDC_AUDIENCE`, optional `AUTH_OIDC_JWKS_URL` |

`firebase` and `cognito` are thin presets over one shared JWKS verifier. They only
derive the issuer, audience rule and key URL:

- Firebase: issuer `https://securetoken.google.com/<project>`, audience `<project>`,
  RS256 keys from Google's `securetoken` JWKS endpoint.
- Cognito: issuer `https://cognito-idp.<region>.amazonaws.com/<pool>`, keys from
  `<issuer>/.well-known/jwks.json`. Access tokens carry `client_id` instead of
  `aud`, so the preset checks `client_id` and `token_use=access`; ID tokens check
  `aud` and `token_use=id`.

Every verifier accepts only an explicit algorithm list and requires `iss`, `aud`
(or `client_id`), `exp` and a non-empty `sub`, with at most 30s of clock leeway.

### Identity mapping

External subjects are strings, not our UUIDs. `user_identities (issuer, subject)`
maps each external identity to an internal `user_id`. It is unique on
`(issuer, subject)`. `AUTH_AUTO_PROVISION` decides whether an unknown identity
gets a user on first request or receives 403. Changing providers keeps data:
link the new identity to the existing user.

`AUTH_PROVIDER` may list several providers (`firebase,jwt`) during a migration.
The core routes on the unverified `iss` claim only to *choose* a verifier; that
verifier then checks everything.

### Code-level extension (requires a rebuild)

Uncommon providers (LDAP, SAML bridges, KMS-held keys) use the registry:

```text
github.com/perdhevi/latihanAPI/auth   separate module, stdlib-only: Authenticator,
                                      optional RouteRegistrar, Deps, Register
cmd/api/plugins.go                    blank imports of every compiled-in provider
```

Add a provider by running `go get` on its module, adding its import to
`plugins.go` and rebuilding the image. The built-in `jwt` provider has its own
extension points (`KeySource`, `CredentialStore`) so its login flow can be kept
while passwords or keys come from elsewhere. `auth/authtest` is a conformance
suite that runs expired, wrong-audience, `alg:none`, wrong-key and tampered-token
cases against any provider.

### Built-in `jwt` provider requirements

- Access tokens ~15 minutes; refresh tokens are opaque, stored hashed, rotated on
  every use, with reuse detection that revokes the chain.
- Passwords hashed with argon2id; a dummy hash for unknown emails so timing does
  not reveal which accounts exist; login rate limiting and lockout.
- Endpoints under `/api/v1/auth` (`register`, `login`, `refresh`, `logout`) and
  `/.well-known/jwks.json`. They are mounted only when `jwt` is active.
