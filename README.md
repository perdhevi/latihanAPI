# latihanApi

The public Go backend for Latihan and its Flutter client, `latihan_mobile`.
It provides personal workout sessions, reusable training plans, planned-versus-actual
comparisons, user profiles, and historical body measurements.

```text
latihan_mobile -- REST / HTTPS --> latihanApi --> PostgreSQL
```

## Summary

- **Stack.** An idiomatic Go modular monolith: `net/http`, `log/slog`, `pgxpool`,
  raw SQL and explicit constructor injection. No ORM, no server code generation.
- **Scope.** Latihan follows an open-core model, and this repository is the public
  fitness domain. Gym memberships, billing, coaches, bookings and tenant management
  live elsewhere. Cloud/offline synchronization is not implemented yet.
- **Authentication.** Every `/api/v1` request needs a bearer token from a provider
  chosen in `.env`: built-in email and password accounts (`jwt`), Firebase, Amazon
  Cognito, or any OpenID Connect issuer.
- **Ownership.** Callers only see and change their own records. The owner comes from
  the verified token, never from request input, and another user's records answer
  404 exactly as if they did not exist.
- **Safe retries.** Creating POSTs accept an `Idempotency-Key`; PUT and DELETE accept
  `If-Match` with the record's `ETag`.
- **Health data.** Users can export or erase everything, changes are audited without
  copying values, and backups are encrypted.
- **Operations.** Rate limits and load shedding by default, Prometheus metrics,
  OpenTelemetry traces, JSON logs, signed release images, and least-privilege
  database roles.

## Quick start

Requires Docker with Compose v2, and ports 8080 and 5432 free.

```sh
cp .env.example .env
docker compose up --build
curl http://localhost:8080/health
```

Use `Copy-Item .env.example .env` on PowerShell. Compose starts PostgreSQL, applies
the migrations, creates a signing key and starts the API with the built-in `jwt`
provider.

## Endpoints

The base URL is `http://localhost:8080` in development. The full contract, with
schemas, bounds and response codes, is in [api/openapi.yaml](api/openapi.yaml).

### Access

| Endpoints | Access |
| --- | --- |
| `/health`, `/ready`, `/.well-known/jwks.json` | Public |
| `/api/v1/auth/*` | Public; only mounted when `AUTH_PROVIDER` includes `jwt` |
| `POST /api/v1/users`, `DELETE /api/v1/account` | Bearer token |
| Everything else under `/api/v1` | Bearer token and an existing profile |
| `/metrics`, `/debug/pprof` | Admin listener (`ADMIN_ADDR`, default `:9090`), never published |

Send the token as `Authorization: Bearer <token>`. A new identity first calls
`POST /api/v1/users` to create its profile; until then other calls answer 403
`profile_required`. A missing or invalid token answers 401 `unauthenticated`.

### Service

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/health` | Liveness; does not query PostgreSQL |
| GET | `/ready` | Readiness; 503 when the database is unreachable |

### Built-in accounts

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/v1/auth/register` | Create an account from `email` and `password`; returns tokens |
| POST | `/api/v1/auth/login` | Exchange `email` and `password` for tokens |
| POST | `/api/v1/auth/refresh` | Exchange a `refresh_token` for a new token pair |
| POST | `/api/v1/auth/logout` | Revoke a `refresh_token` and its session family |
| GET | `/.well-known/jwks.json` | The public signing keys |

### Profile and measurements

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/v1/users` | Create the caller's profile |
| GET, PUT, DELETE | `/api/v1/users/me` | Read, replace or delete the caller's profile |
| GET, POST | `/api/v1/users/me/measurements` | List measurement history or add a record |
| GET, PUT, DELETE | `/api/v1/users/me/measurements/{id}` | Read, correct or delete one record |

`me` may be replaced by the caller's own user ID; any other user ID answers 404.

### Plans and sessions

| Method | Path | Purpose |
| --- | --- | --- |
| GET, POST | `/api/v1/plans` | List the caller's plans or create one |
| GET, PUT, DELETE | `/api/v1/plans/{id}` | Read, replace or delete a plan |
| GET | `/api/v1/plans/{id}/comparison?session_id=UUID` | Compare a plan with a session's actual results |
| GET, POST | `/api/v1/sessions` | List the caller's completed workouts or record one |
| GET, PUT, DELETE | `/api/v1/sessions/{id}` | Read, replace or delete a session |

### Account data

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/account/export` | Download everything the caller owns as one JSON document |
| DELETE | `/api/v1/account` | Erase the caller's data and built-in login account |

### Conventions

- Lists take `limit` (1..100, default 20) and `cursor`, and return
  `{"items":[],"limit":20,"next_cursor":"..."}`; `next_cursor` is `null` on the last page.
- Creates return 201 with `Location`, reads and replacements 200, deletes 204.
- Errors have the shape `{"error":{"code":"session_not_found","message":"session not found"}}`.
- Unknown fields and query parameters are rejected with 400, never ignored.
- Rate-limited requests answer 429 `rate_limited` with `Retry-After`.

## Documentation

| Document | Contents |
| --- | --- |
| [docs/BUILD_GUIDE.md](docs/BUILD_GUIDE.md) | The full guide: authentication providers, request examples, validation, safe retries, observability, abuse limits, local development and testing |
| [docs/DEPLOY.md](docs/DEPLOY.md) | Production deployment, releases and rollback |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Hardening roadmap |
| [docs/blog/](docs/blog/README.md) | The hardening series, step by step |
| [api/openapi.yaml](api/openapi.yaml) | OpenAPI 3.0.3 contract |
