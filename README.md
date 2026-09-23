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
advertising remain outside this repository. Authentication and cloud/offline
synchronization are not implemented yet.

**User IDs identify record ownership; they do not authenticate callers.** Anyone
with network access can read or change records. Keep this slice on a trusted local
network until authentication and authorization are added. Compose binds to localhost.

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

Use `Copy-Item .env.example .env` on PowerShell. Compose starts PostgreSQL 17,
waits for its health check, applies migrations, and starts the non-root API.
The API also retries database connectivity for up to 30 seconds at startup.
The `latihan` database/user/password values are **local development credentials**.

```sh
curl http://localhost:8080/health
curl http://localhost:8080/ready
```

Both return `{"status":"ok"}`. Readiness returns 503 on database connectivity
failure; liveness does not query PostgreSQL. Readiness checks connectivity, while
the migration job establishes schema readiness.

Optional development data (one user, plan, session, and measurement):

```sh
docker compose exec -T postgres psql -U latihan -d latihan -v ON_ERROR_STOP=1 < seeds/development.sql
```

PowerShell equivalent:

```powershell
Get-Content -Raw seeds/development.sql | docker compose exec -T postgres psql -U latihan -d latihan -v ON_ERROR_STOP=1
```

The seed is transactional, repeatable, and never part of application startup.
Try the seeded comparison:

```sh
curl 'http://localhost:8080/api/v1/plans/20000000-0000-4000-8000-000000000001/comparison?session_id=30000000-0000-4000-8000-000000000001'
curl 'http://localhost:8080/api/v1/sessions?user_id=10000000-0000-4000-8000-000000000001'
```

`docker compose down` retains the database. `docker compose down -v` deletes it.

## Endpoints

All paths below are relative to `/api/v1`. The contract, schemas, bounds, and
response codes are in [api/openapi.yaml](api/openapi.yaml) (OpenAPI 3.0.3).

| Endpoint | Methods | Purpose |
| --- | --- | --- |
| `/users` | POST | Create a user profile and return its UUID |
| `/users/{userID}` | GET, PUT, DELETE | Read, replace, or delete a profile |
| `/users/{userID}/measurements` | GET, POST | Historical measurements and new records |
| `/users/{userID}/measurements/{id}` | GET, PUT, DELETE | Read, correct, or delete a historical record |
| `/plans` | GET, POST | List a user's plans or create one |
| `/plans/{id}` | GET, PUT, DELETE | Plan CRUD |
| `/plans/{id}/comparison?session_id=UUID` | GET | Compare the attached plan snapshot with actual results |
| `/sessions` | GET, POST | List a user's completed workouts or record one |
| `/sessions/{id}` | GET, PUT, DELETE | Session CRUD |

Session and plan lists require `user_id=UUID`. All lists support `limit` (1..100,
default 20) and `offset` (0..2147483647, default 0). Responses are
`{"items":[],"limit":20,"offset":0}`. Sessions sort by `performed_at DESC, id DESC`,
measurements by `measured_at DESC, id DESC`, and plans by `created_at DESC, id DESC`.
An existing user with no history has an empty measurement list; an unknown user
returns 404. Session/plan lists return an empty page when the user has no records.
Offset pagination is not a consistent snapshot during concurrent writes.

Creates return 201 with `Location`; reads and replacements return 200; deletes
return 204. Missing records return 404. PUT never upserts and cannot transfer
ownership. Deleting a plan used by a session, or a user with dependent records,
returns 409 instead of silently deleting history. Delete dependencies explicitly.

## Profile → plan → session

Create a profile first:

```sh
curl -X POST http://localhost:8080/api/v1/users \
  -H 'Content-Type: application/json' -d '{"display_name":"Alex"}'
```

Use the returned `id` as `user_id`. User profiles currently contain a display name;
body measurements live in timestamped history rather than mutable profile fields.

Create a plan by posting this body to `/api/v1/plans` (replace `USER_UUID`):

```json
{
  "user_id": "USER_UUID",
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
  "user_id": "USER_UUID",
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

POST to `/api/v1/users/USER_UUID/measurements`:

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
- Every response has a generated `X-Request-ID`; logs record ID, method, path, status,
  and duration. Health, readiness, recovery, and error privacy are tested.
- HTTP timeouts: headers 5s, read 10s, write 15s, idle 60s. Database request contexts
  have a 10s deadline; readiness has 2s. SIGINT/SIGTERM drain requests for up to 10s,
  force-close remaining requests if necessary, then close the pool. Compose allows 15s.

## Local Go development

Prerequisites: Go 1.27+, PostgreSQL 17 (or Docker), optionally GNU Make and `psql`.
Runtime dependencies remain `pgx/v5` and `google/uuid`. `golang-migrate` is a separate
pinned CLI for versioned migrations. No ORM or server code generator is used.

```sh
cp .env.example .env
docker compose up -d postgres
make migrate-up
make run
```

Make loads and exports `.env`; the Go binary itself only reads environment variables.
Compose uses fixed local credentials and its internal hostname; only its log level
is overridden from `.env`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Listener |
| `DATABASE_URL` | Required | PostgreSQL connection URL |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `TEST_DATABASE_URL` | Required for integration tests | Disposable test database |

Without Make (PowerShell):

```powershell
$env:DATABASE_URL = 'postgres://latihan:latihan@localhost:5432/latihan?sslmode=disable'
go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3 -path migrations -database $env:DATABASE_URL up
go run ./cmd/api
```

Use TLS-enabled database URLs and managed secrets in deployed environments;
`sslmode=disable` is only for local development. Use `curl.exe` on Windows if `curl`
is a PowerShell alias, or use `Invoke-RestMethod`.

## Packages and persistence

```text
cmd/api            application composition and lifecycle
internal/profile   profiles, historical measurements, validation, SQL
internal/training  plans, sessions, snapshot comparisons, validation, SQL
internal/httpapi   REST routing, transport, middleware
internal/validation shared text/numeric/pagination validation
internal/database  PostgreSQL pool and bounded startup retry
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
engine, optimistic concurrency token, or idempotency key yet. Concurrent replacements
use the last successful write. Hard deletion is explicit and future tombstones need
a separate migration and sync contract.

## Testing

```sh
gofmt -w cmd internal
go vet ./...
go test ./...
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
deletion conflicts, and migration rollback/reapplication. CI runs vet, race-enabled
unit/integration tests, formatting, build, Compose validation, and Docker build.

Make targets: `run`, `build`, `test`, `fmt`, `vet`, `test-integration`, `seed`,
`migrate-up`, `migrate-down`, `docker-up`, `docker-down`.
`make seed` requires `psql`. `make migrate-down` rolls back one migration and
**deletes that migration's data**: currently sessions, plans, measurements, and
profiles. The previous exercise catalog remains until its own migration is rolled
back. Inspect migration failures before repairing; never blindly force a dirty version.
