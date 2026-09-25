# latihanApi

The public Go backend for Latihan and its Flutter client, `latihan_mobile`.
It provides personal workout sessions, reusable training plans, planned-versus-actual
comparisons, user profiles, and historical body measurements.

```text
latihan_mobile -- REST / HTTPS --> latihanApi --> PostgreSQL
```

This is an idiomatic Go modular monolith using `net/http`, `log/slog`, `pgxpool`,
raw SQL, and explicit constructor injection. Latihan follows an open-core model:
the public fitness domain works without private repositories. Gym memberships,
subscriptions, billing, coaches, bookings, tenant management, entitlements, and
advertising remain outside this repository. Cloud/offline synchronization is not
implemented yet.

Every `/api/v1` request needs a bearer token from a pluggable provider chosen in
`.env` with no code changes: the built-in email and password accounts, Firebase,
Amazon Cognito or any OpenID Connect issuer (see [Authentication](#authentication)).

Callers only ever see and change their own records. The owner of every record comes
from the verified token, never from request input, and another user's records answer
404 exactly as if they did not exist. Compose binds to localhost; see the
[hardening roadmap](docs/ROADMAP.md) for what comes next.

## Breaking change: cursor pagination

List endpoints take `cursor` instead of `offset` and return `next_cursor` instead of
`offset`. Requests that still send `offset` get 400.

## Breaking change: owners come from the token

Requests no longer carry `user_id`. Plan and session bodies that include it, and
list queries such as `/sessions?user_id=...`, are rejected with 400 because unknown
fields and parameters are never ignored. Lists return the caller's own records, and
`/users/me` addresses the caller's profile. Responses still include `user_id`.

## What changed from the Exercise Library API

`/api/v1/exercises` and `/api/v1/exercises/{id}` have been removed and return 404.
A session is a completed workout, containing named exercise entries with their
actual metrics. Exercises no longer require a shared catalog entry. Existing
catalog data is preserved in the original `exercises` table by the migrations;
it is not converted to sessions because catalog rows have no user or workout data.

Migration `000002_create_training` adds profiles, measurements, plans, and sessions.
Existing installations should apply it before running the new API. Compose runs
pending migrations automatically through a separate one-shot migration container.

## Quick start

Prerequisites: Docker Engine/Desktop with Compose v2, ports 8080 and 5432 available.

```sh
cp .env.example .env
docker compose up --build
```

Use `Copy-Item .env.example .env` on PowerShell. The example uses the built-in
`jwt` provider, so nothing else is needed. Compose starts PostgreSQL 17, waits for
its health check, applies migrations, creates a signing key in the `jwt-keys` volume
on first start (and keeps it afterwards), and starts the non-root API. The API
refuses to start without a valid `AUTH_PROVIDER`; the provider's settings and
keys are checked before the server accepts any request.
The API also retries database connectivity for up to 30 seconds at startup.
Credentials come from `secrets/dev/`, **committed development-only values**; a real
deployment generates its own (see [Production deployment](#production-deployment)).

```sh
curl http://localhost:8080/health
curl http://localhost:8080/ready
```

Both return `{"status":"ok"}`. Readiness returns 503 on database connectivity
failure; liveness does not query PostgreSQL. Readiness checks connectivity, while
the migration job establishes schema readiness.

Create an account and keep its access token (valid for 15 minutes; use the
`refresh_token` to get a new one):

```sh
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"alex@example.com","password":"correct horse battery staple"}' \
  | sed -E 's/.*"access_token":"([^"]+)".*/\1/')
```

Then create your profile and use the API with `-H "Authorization: Bearer $TOKEN"`,
as shown in [Profile → plan → session](#profile--plan--session).

Optional development data (one user, plan, session, and measurement):

```sh
docker compose exec -T postgres psql -U latihan -d latihan -v ON_ERROR_STOP=1 < seeds/development.sql
```

PowerShell equivalent:

```powershell
Get-Content -Raw seeds/development.sql | docker compose exec -T postgres psql -U latihan -d latihan -v ON_ERROR_STOP=1
```

The seed is transactional, repeatable, and never part of application startup.
The seeded user has no login. To explore it, link your own identity to it, using
the `iss` and `sub` claims from your token. Decode the token locally rather than
pasting a live token into a website:

```sh
echo "$TOKEN" | cut -d. -f2 | tr '_-' '/+' | base64 -d 2>/dev/null; echo
```

```sh
docker compose exec -T postgres psql -U latihan -d latihan -c \
  "INSERT INTO user_identities (issuer, subject, user_id) VALUES ('YOUR_ISS', 'YOUR_SUB', '10000000-0000-4000-8000-000000000001')"
```

Then try the seeded comparison:

```sh
curl -H "Authorization: Bearer $TOKEN" 'http://localhost:8080/api/v1/plans/20000000-0000-4000-8000-000000000001/comparison?session_id=30000000-0000-4000-8000-000000000001'
curl -H "Authorization: Bearer $TOKEN" 'http://localhost:8080/api/v1/sessions'
```

`docker compose down` retains the database. `docker compose down -v` deletes it.

## Authentication

A provider signs a token for the client, and the API verifies that token's
signature, issuer, audience and lifetime on every request. With an external
provider the API never sees a password: the Flutter app signs in with the
provider's SDK. The built-in `jwt` provider is its own identity provider.

| `AUTH_PROVIDER` | Settings | Token the client sends |
| --- | --- | --- |
| `jwt` | `AUTH_JWT_ISSUER`, `AUTH_JWT_KEY_FILE`, optional `AUTH_JWT_PREVIOUS_KEY_FILES`, `AUTH_JWT_AUDIENCE`, `AUTH_JWT_ACCESS_TTL`, `AUTH_JWT_REFRESH_TTL` | Access token from `/api/v1/auth/login` (see [Built-in accounts](#built-in-accounts)) |
| `firebase` | `AUTH_FIREBASE_PROJECT_ID` | Firebase ID token (`getIdToken()`) |
| `cognito` | `AUTH_COGNITO_REGION`, `AUTH_COGNITO_USER_POOL_ID`, `AUTH_COGNITO_CLIENT_ID`, `AUTH_COGNITO_TOKEN_USE` (`access` or `id`) | Cognito access token (or ID token if configured) |
| `oidc` | `AUTH_OIDC_ISSUER`, `AUTH_OIDC_AUDIENCE`, optional `AUTH_OIDC_JWKS_URL`, `AUTH_OIDC_ALGORITHMS` | Access token from Keycloak, Auth0, Zitadel or another OIDC issuer |

Send it as `Authorization: Bearer <token>`. The first call for a new identity is
`POST /api/v1/users`, which creates a profile and links the identity to it; every
other `/api/v1` call requires that profile.

| Status | Code | Meaning |
| --- | --- | --- |
| 401 | `unauthenticated` | Missing, expired or invalid token. `WWW-Authenticate` carries the RFC 6750 challenge. |
| 403 | `profile_required` | Valid token, but this identity has not created its profile yet. |
| 409 | `profile_exists` | `POST /api/v1/users` for an identity that already has a profile. |
| 503 | `auth_unavailable` | The provider's signing keys could not be loaded. |

Verification details: only the configured asymmetric algorithms are accepted (never
`none` or HMAC), key URLs must be HTTPS, tokens over 8 KiB are refused, and clocks may
differ by up to 30 seconds. Signing keys are cached, refreshed hourly, and refetched
for an unknown key ID at most once a minute; during a key-server outage the last
good keys keep working. `/health` and `/ready` need no token.

Identities live in `user_identities (issuer, subject) → user_id`. A user can have
several identities, so moving to another provider means linking the new identity
to the existing user rather than migrating data. Deleting a user deletes its links.

### Built-in accounts

`AUTH_PROVIDER=jwt` keeps email and password accounts in this service's database
(migration `000004_create_local_auth`) and signs its own tokens. Like an external
provider's accounts, they are separate from profiles: register or log in first,
then create the profile with `POST /api/v1/users`.

| Endpoint | Body | Result |
| --- | --- | --- |
| `POST /api/v1/auth/register` | `{"email","password"}` | 201 with tokens; 409 `email_taken` |
| `POST /api/v1/auth/login` | `{"email","password"}` | 200 with tokens; 401 `invalid_credentials` |
| `POST /api/v1/auth/refresh` | `{"refresh_token"}` | 200 with a new token pair; 401 `invalid_refresh_token` |
| `POST /api/v1/auth/logout` | `{"refresh_token"}` | 204, always |
| `GET /.well-known/jwks.json` | | The public signing keys |

Tokens come back as `{"access_token","token_type":"Bearer","expires_in","refresh_token"}`
with `Cache-Control: no-store`. How it is hardened:

- **Tokens.** Access tokens are EdDSA (Ed25519) JWTs, valid for 15 minutes and
  verified by the same code as external providers' tokens. Refresh tokens are 256
  random bits, stored only as SHA-256 hashes, valid for 30 days, and replaced on
  every use.
- **Stolen refresh tokens.** Presenting a refresh token that was already used
  revokes its whole session family, so whichever of the thief or the user refreshes
  second is logged out and the theft is logged. Two refreshes racing with one token
  count as reuse too. Logout revokes the family; access tokens already issued remain
  valid until they expire.
- **Passwords.** 12 to 128 characters, no composition rules (NIST SP 800-63B).
  Hashed with argon2id (19 MiB, 2 passes) and at most four hashes run at once, so a
  burst of logins queues instead of exhausting memory. Stored hashes with older
  parameters are upgraded at the next login.
- **Guessing and enumeration.** Unknown emails, wrong passwords and locked accounts
  get the same answer after the same hashing work. Five consecutive failures lock
  the account for 15 minutes. Registration does reveal that an email is taken
  (409); hiding that needs an email-verification step this provider does not have.
- **Keys.** The signing key is an Ed25519 PEM file; its key ID is the RFC 7638
  thumbprint. `api keygen -out FILE` creates one and never overwrites (Compose runs
  it for you; locally, `make keygen`). To rotate, create a new key, make it
  `AUTH_JWT_KEY_FILE`, list the old one in `AUTH_JWT_PREVIOUS_KEY_FILES`, restart,
  and drop the old key once `AUTH_JWT_ACCESS_TTL` has passed.

Not included: email verification, password reset and account deletion. They need
outbound email and are good reasons to pick an external provider instead.

### Moving between providers

`AUTH_PROVIDER` accepts a comma-separated list, such as `firebase,jwt`. Each token
goes to the provider whose issuer matches its `iss` claim; that claim only selects
the verifier, which then checks everything. Link each user's new identity to the
existing profile in `user_identities`, run both providers while clients move over,
then remove the old one.

### Adding another provider

Providers are compiled in and listed in [cmd/api/plugins.go](cmd/api/plugins.go).
A provider implements the small interface in the standalone
[`auth`](auth/auth.go) module (standard library only) and registers itself by name:

```go
func init() { auth.Register("ldap", New) }
```

To use one from another repository, run `go get` on it, add its import to
`plugins.go`, rebuild, and set `AUTH_PROVIDER` to its name. JWT-based providers
should pass [`auth/authtest`](auth/authtest/authtest.go), the conformance suite
the built-in providers run: expired and future tokens, wrong issuer or audience,
`alg: none`, HMAC key confusion, unknown or wrong keys, and tampered payloads.

## Endpoints

All paths below are relative to `/api/v1`. The contract, schemas, bounds, and
response codes are in [api/openapi.yaml](api/openapi.yaml) (OpenAPI 3.0.3).

| Endpoint | Methods | Purpose |
| --- | --- | --- |
| `/users` | POST | Create the caller's profile and return its UUID |
| `/users/me` | GET, PUT, DELETE | Read, replace, or delete the caller's profile |
| `/users/me/measurements` | GET, POST | Historical measurements and new records |
| `/users/me/measurements/{id}` | GET, PUT, DELETE | Read, correct, or delete a historical record |
| `/plans` | GET, POST | List the caller's plans or create one |
| `/plans/{id}` | GET, PUT, DELETE | Plan CRUD |
| `/plans/{id}/comparison?session_id=UUID` | GET | Compare the attached plan snapshot with actual results |
| `/sessions` | GET, POST | List the caller's completed workouts or record one |
| `/sessions/{id}` | GET, PUT, DELETE | Session CRUD |

`me` may be replaced by the caller's own user ID; any other user ID returns 404.
Lists contain only the caller's records and use cursor pagination: `limit` (1..100,
default 20) and `cursor`. Responses are `{"items":[],"limit":20,"next_cursor":"..."}`;
pass `next_cursor` back as `cursor` for the next page, which is `null` on the last
page. Sessions sort by `performed_at DESC, id DESC`,
measurements by `measured_at DESC, id DESC`, and plans by `created_at DESC, id DESC`.
Lists return an empty page when the caller has no records. Cursors are positions,
not counts, so records added or deleted between pages never cause skipped or
repeated items, and a deep page costs the same as the first.

Creates return 201 with `Location`; reads and replacements return 200; deletes
return 204. Missing records, and records owned by someone else, return 404. PUT
never upserts and cannot transfer ownership. Deleting a plan used by a session, or a user with dependent records,
returns 409 instead of silently deleting history. Delete dependencies explicitly.

## Profile → plan → session

Create a profile first. `TOKEN` holds a token from your provider:

```sh
curl -X POST http://localhost:8080/api/v1/users \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"display_name":"Alex"}'
```

Send the same `Authorization` header on every request below; the API knows
whose records they are from the token. User profiles currently contain a display name;
body measurements live in timestamped history rather than mutable profile fields.

Create a plan by posting this body to `/api/v1/plans`:

```json
{
  "name": "Leg day",
  "notes": "Strength followed by cardio",
  "exercises": [
    {"name":"Squat","kind":"strength","sets":[
      {"repetitions":8,"weight_kg":50},
      {"repetitions":8,"weight_kg":50}
    ]},
    {"name":"Running","kind":"cardio","minutes":30,"avg_bpm":140}
  ]
}
```

Each exercise in the plan response has a server-generated `id`. To record actual
results, POST to `/api/v1/sessions` using the plan and exercise IDs returned above:

```json
{
  "name": "Morning training",
  "performed_at": "2026-09-23T08:00:00Z",
  "plan_id": "PLAN_UUID",
  "exercises": [
    {"plan_exercise_id":"SQUAT_ENTRY_UUID","name":"Squat","kind":"strength",
      "sets":[{"repetitions":10,"weight_kg":55}]},
    {"plan_exercise_id":"RUN_ENTRY_UUID","name":"Running","kind":"cardio",
      "minutes":25,"avg_bpm":145}
  ]
}
```

Omit `plan_id` for a standalone session. Omit `plan_exercise_id` for an unplanned
exercise; this also lets a planned session include extra exercises. Linked entries
must belong to the attached snapshot, use the same kind, and must not repeat.
Repeated exercise names in a plan are supported because matching uses entry IDs.

### Comparison semantics

`GET /api/v1/plans/PLAN_UUID/comparison?session_id=SESSION_UUID` returns the exact
planned and actual exercise details, set-by-set comparisons, and totals/deltas.
All differences are **actual minus planned**. The example above produces:

- Planned weightlifting volume: `8×50 + 8×50 = 800 kg`; actual: `10×55 = 550 kg`;
  delta: `-250 kg`.
- Cardio minutes: `25 - 30 = -5`; average BPM: `145 - 140 = +5`.
- First-set repetitions delta: `+2`; weight delta: `+5 kg`; the second set is missing.

Entries are `matched`, `missed`, or `unplanned`. `matched` means linked, not that
the target was met. Missing records are `null`; missing BPM is unknown, never zero.
There is no aggregate BPM across exercises. Sets match by their position within
each exercise. Planned entries appear in plan order, followed by unplanned entries
in session order. This is a factual comparison, not a training recommendation.

Attaching a plan stores its full snapshot in the session in the same transaction.
Editing the reusable plan cannot alter past comparisons. PUT replaces a plan's
exercise entries and generates new entry IDs. Existing sessions continue to use
their snapshot IDs. Updating a session with the same `plan_id` preserves its original
snapshot. Changing the plan (or detaching and reattaching) captures the current
version. A plan must belong to the same user as the session; the database enforces
this with a composite foreign key. Comparison of an unrelated session returns 404.

## Measurement history

POST to `/api/v1/users/me/measurements`:

```json
{
  "measured_at":"2026-09-23T07:00:00Z",
  "weight_kg":80,
  "height_cm":180,
  "body_fat_percent":15,
  "waist_cm":80,
  "chest_cm":100,
  "hip_cm":90
}
```

At least one numeric measurement is required. Omitted/null measurements mean not
recorded. Backdated entries are supported and sorted by measurement time. PUT
corrects a full historical record and clears any omitted metrics. GET lists history
with pagination; it does not overwrite or merge previous measurements.

## Validation and operational behavior

- Names are trimmed, required, and limited to 200 Unicode characters; notes allow
  4000. Exercise kind is normalized to `strength` or `cardio`.
- Plans/sessions contain 1..100 exercises. Strength entries contain 1..100 sets;
  each requires 1..1000 repetitions and explicit weight in kg, 0..2000. Use 0 for
  bodyweight. Cardio requires minutes greater than 0 and at most 1440; optional
  average BPM is 1..300. Mixed strength/cardio metrics are rejected.
- Measurements: positive weight up to 1000 kg, height up to 300 cm, circumferences
  up to 500 cm, and body fat 0..100%. These are input bounds, not health targets.
- All timestamps use RFC3339 and are returned in UTC. `performed_at`/`measured_at`
  must be nonzero. Database-managed `created_at` and `updated_at` cannot be supplied.
- JSON bodies are limited to 1 MiB. Unknown fields, unknown/repeated query
  parameters, multiple JSON values, and malformed IDs return 400. Oversized bodies
  return 413; non-JSON content types return 415. Optional notes accept null as empty.
- Errors have the shape `{"error":{"code":"session_not_found","message":"session not found"}}`.
  Unexpected failures return generic 500 responses; SQL and internal details stay
  out of client responses. Request bodies are not logged.
- Every response has a generated `X-Request-ID` and `Cache-Control: no-store`; logs
  record ID, client address, method, path, status, and duration. Health, readiness, recovery, and error privacy are tested.
- HTTP timeouts: headers 5s, read 10s, write 15s, idle 60s. Database request contexts
  have a 10s deadline; readiness has 2s. SIGINT/SIGTERM drain requests for up to 10s,
  force-close remaining requests if necessary, then close the pool. Compose allows 15s.
- Request headers are capped at 16 KiB (`MaxHeaderBytes`; Go adds 4 KiB of slack, so
  requests with about 20 KiB of headers or more get 431).

## Safe retries

Mobile networks drop responses, and two devices can edit the same record. Both
mechanisms below are opt-in per request, so existing clients keep working.

**Idempotency-Key on creating POSTs** (`/users`, `/plans`, `/sessions`,
`/users/me/measurements`). Send a random key per intended action and reuse it
when retrying:

```sh
curl -X POST http://localhost:8080/api/v1/sessions -H "Authorization: Bearer $TOKEN" \n  -H 'Content-Type: application/json' -H 'Idempotency-Key: 6f1c2a9e-2b54-4f0e-9d3a-7c1e5b0a8d42' -d .json
```

| Retry with the same key | Result |
| --- | --- |
| Same body, first request finished | The stored status, body, `Location` and `ETag`, plus `Idempotent-Replayed: true`. Nothing is created. |
| First request still running | 409 `idempotency_key_in_progress` with `Retry-After`. |
| Different body or endpoint | 422 `idempotency_key_reused`. |
| First request got 5xx or 429 | Runs again; those outcomes are not stored. |

Keys are private to the caller's identity and kept for 24 hours (purged hourly).
A claim whose request died mid-way is taken over after a minute. One gap remains:
the record and the stored response are written in separate transactions, so a
process crash between the two lets a retry run the request again.

**ETag and If-Match on PUT and DELETE** (plans, sessions, `/users/me`,
measurements). Every single-record response carries an `ETag`. Send it back:

```sh
curl -X PUT http://localhost:8080/api/v1/plans/PLAN_UUID -H "Authorization: Bearer $TOKEN" \n  -H 'Content-Type: application/json' -H 'If-Match: "hmm8doc5yf"' -d .json
```

If someone changed the record since that read, the answer is 412
`precondition_failed` and nothing is written: fetch it again, merge, retry. The
version check runs inside the `UPDATE`/`DELETE` itself (sessions check it under a
row lock), so of two writers holding the same ETag exactly one succeeds. `*`
matches any version; weak ETags never match. Without `If-Match` the write applies
to the current version, unless `REQUIRE_IF_MATCH=true`, which answers 428.

## Production deployment

[docs/DEPLOY.md](docs/DEPLOY.md) walks through the whole path: VM baseline (SSH keys
only, firewall, automatic updates, Docker defaults), secrets, GitHub environment
setup, releasing with `git tag v1.2.3`, and rollback. In short:

```sh
scripts/init-secrets.sh          # random credentials in ./secrets (never overwrites)
cp .env.example .env              # then set SECRETS_DIR=./secrets, DOMAIN, ACME_EMAIL,
                                  # and AUTH_JWT_ISSUER=https://DOMAIN (or another provider)
deploy/deploy.sh <commit> ghcr.io/perdhevi/latihanapi@sha256:<digest>
```

**Releases.** Pushing a `v*` tag runs [the Release workflow](.github/workflows/release.yml):
it builds the image, pushes it to GHCR with an SPDX SBOM and SLSA provenance
attached, fails on fixable HIGH or CRITICAL vulnerabilities (Trivy), and signs it
with cosign using GitHub's workflow identity (no signing keys to store). With
deployment enabled, it then waits for approval and runs `deploy/deploy.sh` over SSH.
That script deploys only images pinned by digest **and** signed by this
repository's release workflow for a `v*` tag, checked out at the matching commit.

**The image** is `distroless/static`, pinned by digest like every other image: no
shell, no package manager, running as UID 65532, about 35 MB. The binary checks
its own health (`api healthcheck`) since there is no `wget`. In production the
containers also get memory and PID limits and rotated logs. CI scans every build
with Trivy, not only releases.

[docker-compose.prod.yml](docker-compose.prod.yml) adds [Caddy](deploy/Caddyfile) in front:

- **TLS.** Caddy obtains and renews a Let's Encrypt certificate for `DOMAIN`,
  redirects HTTP to HTTPS, and adds HSTS, a deny-all Content-Security-Policy,
  `X-Frame-Options: DENY` and `Referrer-Policy: no-referrer`.
- **Nothing else is reachable.** Only Caddy publishes ports; the API, its admin
  port and PostgreSQL are only on the Compose network.
- **Real client addresses.** Caddy has a fixed address (`172.30.0.10`), the only
  entry in `TRUSTED_PROXIES`, and replaces any `X-Forwarded-For` a client sends.

**Secrets** are files, mounted as Compose secrets and read through `X_FILE` settings,
so they never appear in `docker inspect`, process listings or `.env`.
`scripts/init-secrets.sh` keeps its directory private (0700) and makes the files
readable (0644) because the containers run as different users. Passwords apply when
the database volume is first created; change them later with `ALTER ROLE ... PASSWORD`
and update the files.

**Database roles.** [db/init/10-roles.sh](db/init/10-roles.sh) runs when the volume is
first created:

| Role | Used by | Can |
| --- | --- | --- |
| `latihan` | Nothing after initialisation | Everything (superuser) |
| `latihan_migrator` | The migration job | Own and change the schema |
| `latihan_app` | The API | `SELECT`, `INSERT`, `UPDATE`, `DELETE` on rows; no DDL, no `TRUNCATE` |

An SQL injection in the API could therefore still read and change rows, but not
drop tables or alter the schema. CI proves it: the smoke test runs the stack and
checks that `latihan_app` cannot `DROP`, `TRUNCATE`, `ALTER` or `CREATE`.

**Upgrading an existing development database.** Volumes created before these roles
existed have neither role. For development data, recreate the volume with
`docker compose down -v`.

**Managed PostgreSQL.** Point `DATABASE_URL` (via a secret file) at it with
`sslmode=verify-full`, create the two roles with the SQL in `db/init/10-roles.sh`,
and run migrations as the migrator. The API warns at startup when a remote
database connection is not certificate-verified.

## Observability

The API serves `/metrics` on a separate admin listener, `ADMIN_ADDR` (default
`:9090`). Compose never publishes that port: Prometheus scrapes it over the Compose
network. `/debug/pprof` is added only with `ADMIN_PPROF=true`, because profiles
expose internals and a CPU profile costs CPU.

```sh
docker compose --profile observability up --build
```

That also starts Prometheus (http://localhost:9091) and Jaeger (http://localhost:16686).
Set `OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318` in `.env` to send traces.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `latihan_http_requests_total` | `route`, `method`, `code` | Requests. `route` is the pattern, such as `/api/v1/sessions/{id}`, never the raw path; `none` for requests refused before routing (429, 503) |
| `latihan_http_request_duration_seconds` | `route`, `method` | Latency histogram |
| `latihan_http_requests_in_flight` | | Requests being served |
| `latihan_http_rejected_total` | `reason` | Refused before a handler: `ip`, `user`, `public`, `overloaded`, `too_many_clients` |
| `latihan_auth_failures_total` | `reason` | `unauthenticated`, `unavailable`, `profile_required` |
| `latihan_idempotent_replays_total` | | Responses replayed for a repeated `Idempotency-Key` |
| `latihan_http_panics_total` | | Recovered handler panics |
| `latihan_db_pool_*` | | Connections by state, pool size, waits for a free connection |
| `latihan_build_info` | `version`, `go_version` | Which build is running (`docker build --build-arg VERSION=...`) |

Labels are bounded on purpose: raw paths would create a new time series for every
ID a client touches, and unknown HTTP methods are folded into `OTHER`.

**Traces** (OpenTelemetry) are off unless `OTEL_EXPORTER_OTLP_ENDPOINT` is set;
the standard `OTEL_*` variables choose the exporter and sampler (for example
`OTEL_TRACES_SAMPLER=parentbased_traceidratio` with `OTEL_TRACES_SAMPLER_ARG=0.1`).
An incoming W3C `traceparent` is continued. Each request is a server span named
after its route, and every PostgreSQL query is a child span with its SQL text but
not its arguments, which hold user data.

**Logs** are JSON. Every line logged during a request carries `trace_id` and
`span_id`; the request line adds `request_id`, `client_ip`, `user_id` (once
authenticated), method, path, status and duration. A panic logs its stack trace and
answers 500, except `http.ErrAbortHandler`, which is passed on to `net/http` to
close the connection as intended.

## Abuse limits

Every limit has a default, so a fresh deployment is protected; tune them in `.env`.
Rate limits use token buckets written `N/unit:burst` (unit `s`, `m` or `h`), or `off`.

| Setting | Default | Applies to |
| --- | --- | --- |
| `RATE_LIMIT_IP` | `50/s:100` | Every client address (IPv6 per /64), all routes except `/health` and `/ready` |
| `RATE_LIMIT_USER` | `10/s:30` | Every authenticated identity, whatever address it uses |
| `RATE_LIMIT_AUTH` | `10/m:10` | Every client address on routes the auth provider mounts (login, register, refresh) |
| `MAX_IN_FLIGHT` | `256` | Concurrent requests; excess ones get 503 `overloaded` at once instead of queueing |
| `TRUSTED_PROXIES` | none | Addresses or CIDRs allowed to set `X-Forwarded-For` |
| `DB_MAX_CONNS` | `10` | PostgreSQL connections |
| `DB_STATEMENT_TIMEOUT` | `5s` | Enforced by PostgreSQL itself, with a 3s `lock_timeout` and a 10s idle-in-transaction timeout |

Limited requests get 429 `rate_limited` with `Retry-After` in seconds.

- **Client addresses.** `X-Forwarded-For` is ignored unless the connection comes
  from `TRUSTED_PROXIES`; then the rightmost address that is not a trusted proxy is
  the client, and anything a client wrote to the left of it is ignored. Behind a
  reverse proxy, list the proxy here, or every client shares the proxy's limit.
- **Memory.** Limiters live in memory: fine for one instance, but several instances
  would each allow the full rate. At most 100,000 addresses are tracked; beyond
  that new addresses get 429 until idle ones expire, rather than evicting (and so
  resetting) the limits of active clients.
- **Password hashing.** On top of `RATE_LIMIT_AUTH`, at most four argon2id hashes
  run at once (see [Built-in accounts](#built-in-accounts)).
- **Mobile networks.** Many phones can share one address behind carrier NAT. If
  real users hit `RATE_LIMIT_IP`, raise it; `RATE_LIMIT_USER` still bounds each account.

## Local Go development

Prerequisites: Go 1.27+, PostgreSQL 17 (or Docker), optionally GNU Make and `psql`.
Runtime dependencies are `pgx/v5`, `google/uuid`, `golang-jwt/jwt/v5` (token
parsing) and `golang.org/x/crypto` (argon2id). `golang-migrate` is a separate
pinned CLI for versioned migrations. No ORM or server code generator is used.

```sh
cp .env.example .env
docker compose up -d postgres
make migrate-up
make keygen   # signing key for AUTH_PROVIDER=jwt
make run
```

Make loads and exports `.env`; the Go binary itself only reads environment variables.
Compose uses fixed local credentials and its internal hostname for the database;
the log level and `AUTH_*` settings come from `.env`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Listener |
| `DATABASE_URL` | Required | PostgreSQL connection URL, as `latihan_app` |
| `X_FILE` | | Any setting `X` may instead name a file holding its value (Docker secrets); setting both is an error |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `AUTH_PROVIDER` | Required | `jwt`, `firebase`, `cognito`, `oidc`, or a comma-separated list; see [Authentication](#authentication) and `.env.example` |
| `AUTH_*` | Provider-specific | Settings for the selected provider |
| `ADMIN_ADDR` | `:9090` | Admin listener for `/metrics`; never publish it |
| `ADMIN_PPROF` | `false` | Adds `/debug/pprof` to the admin listener |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | Turns on trace export (OTLP/HTTP) |
| `REQUIRE_IF_MATCH` | `false` | Reject PUT and DELETE without `If-Match` (428); see [Safe retries](#safe-retries) |
| Abuse limits | See [Abuse limits](#abuse-limits) | `RATE_LIMIT_*`, `MAX_IN_FLIGHT`, `TRUSTED_PROXIES`, `DB_*` |
| `TEST_DATABASE_URL` | Required for integration tests | Disposable test database |

Without Make (PowerShell):

```powershell
$env:DATABASE_URL = 'postgres://latihan:latihan@localhost:5432/latihan?sslmode=disable'
go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3 -path migrations -database $env:DATABASE_URL up
go run ./cmd/api
```

`sslmode=disable` is fine on the same host or Compose network; for any other
database use `sslmode=verify-full` (the API logs a warning otherwise). Use `curl.exe` on Windows if `curl`
is a PowerShell alias, or use `Invoke-RestMethod`.

## Packages and persistence

```text
cmd/api            application composition and lifecycle
internal/profile   profiles, historical measurements, validation, SQL
internal/training  plans, sessions, snapshot comparisons, validation, SQL
auth               standalone module: provider contract and registry (stdlib only)
auth/authtest      conformance suite for JWT-based providers
internal/httpapi   REST routing, transport, middleware
internal/validation shared text/numeric/pagination validation
internal/database  PostgreSQL pool and bounded startup retry
internal/authn     built-in providers: jwks (shared verifier), local (jwt), firebase,
                   cognito, oidc
internal/httpjson  JSON request/response conventions shared with providers
internal/config    environment configuration
internal/testdb    integration-test schema setup (integration build tag only)
migrations         reversible versioned SQL
seeds              optional development data
api                OpenAPI contract
```

Ordered exercise/set arrays and the attached plan snapshot are bounded JSONB
documents, validated by the service and saved atomically. These aggregates are read
and replaced as a whole; user ownership, plan linkage, dates, and measurements are
relational. Session writes lock the existing session and, when taking a new snapshot,
the selected plan. This prevents partially saved workouts or mixed plan versions.
Indexes support actual per-user history ordering and foreign-key deletion checks.

UUIDs avoid sequential-ID dependencies. APIs currently generate IDs; persistence
can accept UUIDs without schema changes for future offline work. There is no sync
engine yet. Concurrent replacements are guarded by ETag and If-Match, and retried
creates by Idempotency-Key (see [Safe retries](#safe-retries)); a write without
If-Match still replaces whatever version is current. Hard deletion is explicit and future tombstones need
a separate migration and sync contract.

## Testing

```sh
make check   # golangci-lint (incl. gofmt/goimports), govulncheck, unit tests
go build ./...
docker compose up -d postgres
TEST_DATABASE_URL='postgres://latihan:latihan@localhost:5432/latihan?sslmode=disable' \
  go test -count=1 -tags=integration ./...
```

PowerShell uses `$env:TEST_DATABASE_URL = 'postgres://...'` followed by
`go test -count=1 -tags=integration ./...`.

Integration tests create a unique schema, apply the real migrations, exercise full
HTTP → service → PostgreSQL workflows, and remove only their own schema. The database
user needs schema creation permission. Never use a production database for tests.
The integration tag fails if its database URL is missing. Tests cover CRUD,
ownership constraints, stable plan snapshots, plan changes/detachment, comparison
metrics, missing/unplanned exercises, history ordering, pagination, cancellation,
deletion conflicts, migration rollback/reapplication, identity linking, the 401/403/409/503
authentication responses, observability (route-pattern labels under many IDs, rejection, auth and panic
metrics, panic stacks, ErrAbortHandler, trace continuation, log correlation, query
spans without arguments, pprof off by default), safe retries (exact replay, key reuse, in-progress and abandoned keys, parallel
retries creating one record; stale and concurrent If-Match writes), the built-in provider end to end (register, profile,
refresh, reuse detection, logout; concurrent refreshes of one token), abuse limits (per-IP,
per-user and auth-route limits, spoofed `X-Forwarded-For`, IPv6 /64 grouping, load
shedding), cursor pagination across ties and concurrent inserts, PostgreSQL-enforced
statement timeouts, keyset index use, and cross-user access: an intruder holding every ID of
another user's records gets 404 on every route and the records stay unchanged. Every built-in provider also runs the
`auth/authtest` conformance suite against a fake TLS identity provider.

CI runs three jobs: **lint** (tidy modules, golangci-lint with gosec and other
security linters, formatting), **vulnerabilities** (govulncheck; fails only on
vulnerabilities reachable from this code), and **test** (race-enabled unit and
integration tests, build, Compose validation, Docker build). Actions are pinned to
commit SHAs and Dependabot updates Go modules, actions and base images weekly.
Linter configuration lives in `.golangci.yml`.

### Beyond example-based tests

**Fuzzing.** Twelve fuzz targets cover everything that parses untrusted input:
JSON bodies, bearer tokens and multi-provider routing, `X-Forwarded-For`, cursors,
`If-Match`, rate-limit policies, emails, stored password hashes, JWKs, text
validation and the plan comparison. Each checks an invariant, not just "no
panic": for example, an untrusted peer can never choose its client address, and a
decoded cursor re-encodes to the same position. `sh scripts/fuzz.sh` runs each for
`FUZZTIME` (CI: 15s per target); failing inputs land in `testdata/fuzz/` and are
replayed by plain `go test` forever after. Fuzzing found a NaN rate accepted as a
rate-limit policy and a carriage return accepted as a bearer token.

**Contract tests.** Every response in the integration tests is validated against
[api/openapi.yaml](api/openapi.yaml): the status must be documented for that
operation and the body must match its schema exactly. A unit test also checks
that every error code in the source appears in the documented `Error` schema.
Together they found an invalid schema, 14 undocumented error codes and an
undocumented 404.

**Attacker view.** [attacker_integration_test.go](internal/httpapi/attacker_integration_test.go)
replays another user's `Idempotency-Key`, uses a victim's real ETag, forges cursors
into a victim's history, compares answers for real and made-up IDs, and injects a
victim's user ID in paths, bodies and queries.

**Load tests** ([loadtest/](loadtest)), run against the Compose stack with k6:

```sh
docker run --rm -i --network latihanapi_default -e BASE_URL=http://latihan-api:8080 \n  grafana/k6:2.3.0 run - < loadtest/capacity.js   # start the stack with RATE_LIMIT_*=off first
docker run --rm -i --network latihanapi_default -e BASE_URL=http://latihan-api:8080 \n  grafana/k6:2.3.0 run - < loadtest/pressure.js   # default limits
```

The network is `<project>_default`; the project name is the checkout's directory
name in lower case unless `COMPOSE_PROJECT_NAME` is set.

On a 16-core development machine (so only indicative), 50 users mixing reads,
idempotent creates and conditional updates reached about 4,000 requests/s with p95
of 10 ms (reads) and 23 ms (writes), limited by the 10-connection pool:
`latihan_db_pool_empty_acquires_total` climbed steadily. With `DB_MAX_CONNS=30`
the same test reached about 7,100 requests/s (p95 6.5 ms / 16 ms). Watch that
metric before raising the pool size, and keep it within PostgreSQL's
`max_connections`. Under pressure (600 requests/s of credential stuffing and
unauthenticated reads from one address), the defaults answered 10,911 requests with
429 and `Retry-After`, let 10 login attempts reach password hashing, returned no
server errors, and kept p99 under 1 ms.

Make targets: `run`, `build`, `test`, `fmt`, `vet`, `lint`, `vuln`, `check`, `keygen`,
`test-integration`, `seed`, `migrate-up`, `migrate-down`, `docker-up`, `docker-down`.
`make seed` requires `psql`. `make migrate-down` rolls back one migration and
**deletes that migration's data**: the newest one holds the built-in provider's
accounts and refresh tokens; earlier ones hold identity links, then sessions, plans,
measurements and profiles. The exercise catalog remains until its own migration is
rolled back. Inspect migration failures before repairing; never blindly force a dirty version.
