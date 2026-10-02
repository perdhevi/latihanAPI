# latihanApi

The public Go backend for the Latihan fitness platform and `latihan_mobile`.
Record workouts, compare sessions with training plans, and track body measurements.
Authentication supports built-in email/password accounts, Firebase, Cognito, and OIDC.

```text
latihan_mobile → HTTPS / REST → latihanApi → PostgreSQL
```

The public fitness platform works independently of commercial repositories. Gym
memberships, billing, bookings, coaches, advertising, and commercial entitlements
are outside this repository. Offline/cloud synchronization is not implemented yet.

This README is the deployment and API usage guide. The previous README is preserved
unchanged as [build-readme.md](build-readme.md), with architecture, build, testing,
security, and implementation details. Full request/response schemas are in
[api/openapi.yaml](api/openapi.yaml).

## Contents

- [Deploy on a local Linux host](#deploy-on-a-local-linux-host)
- [Secrets and database accounts](#secrets-and-database-accounts)
- [Existing databases and password recovery](#existing-databases-and-password-recovery)
- [Connect from another device](#connect-from-another-device)
- [Deploy with HTTPS and signed images](#deploy-with-https-and-signed-images)
- [Endpoint reference](#endpoint-reference)
- [API walkthrough](#api-walkthrough)
- [Routine operations](#routine-operations)
- [Troubleshooting](#troubleshooting)
- [Build and reference documentation](#build-and-reference-documentation)

## Deploy on a local Linux host

These steps build from source and bind the API to the host's loopback address.
They require no local Go installation. For an existing installation, preserve its
`.env`, secret files, signing keys, and database volume; read the recovery section
before generating new credentials.

### 1. Check prerequisites and the container engine

Install Git, curl, and jq (jq is used by the API examples):

```bash
sudo apt update
sudo apt install git curl jq
docker --version
docker compose version
```

For Docker Engine, follow the official [Ubuntu installation instructions](https://docs.docker.com/engine/install/ubuntu/),
including the Compose plugin. Use a Compose version that supports optional
`env_file` entries and the `!reset` tags used by the production overlay.

If the CLI prints **Emulate Docker CLI using podman**, Docker Engine is not handling
these commands. You are using Podman compatibility. [Podman's Compose command](https://docs.podman.io/en/latest/markdown/podman-compose.1.html)
delegates to an external Compose provider; available features depend on that provider.
Check `podman --version` and `docker compose version`, then validate the configuration
as shown below. The commands here use `docker compose`; substitute `podman compose`
if that is your configured command. The repository's deployment workflow targets
Docker Compose; full Podman compatibility is not asserted.

With rootless Podman, keep using the same Linux user. Running commands with `sudo`
can select a different container store and make existing containers/volumes appear
missing. Do not switch engines or delete volumes as a troubleshooting shortcut.

### 2. Get the repository

For a new checkout:

```bash
git clone https://github.com/perdhevi/latihanAPI.git
cd latihanAPI
cp .env.example .env
```

For an existing checkout, enter its directory and keep the existing `.env`.
All following commands run from the repository root.

### 3. Create the secret files before starting PostgreSQL

For a fresh installation with its own credentials:

```bash
sh scripts/init-secrets.sh
```

This creates four files in `./secrets`. It keeps existing files unchanged, including
empty or incorrect existing files; inspect and repair those instead of assuming
rerunning the script will replace them. The generated passwords are alphanumeric
and safe to include in a connection URL. The directory is private (0700), while
individual files are readable (0644) for the different container users.

Check file types and sizes without printing credentials:

```bash
for name in postgres_password migrator_password app_password app_database_url; do
  test -f "secrets/$name" && test -s "secrets/$name" \
    || { printf 'Missing, empty, or not a file: secrets/%s\n' "$name"; exit 1; }
  stat -c '%n | %F | %s bytes | mode %a' "secrets/$name"
done
```

Do not place passwords in directory names. `app_database_url` is a regular text
file containing the connection string, not a directory or an environment assignment.

### 4. Configure `.env`

Edit `.env` and set these values, replacing existing entries rather than adding duplicates:

```dotenv
SECRETS_DIR=./secrets
AUTH_PROVIDER=jwt
AUTH_JWT_ISSUER=http://localhost:8080
LOG_LEVEL=info
```

The JWT issuer identifies this installation; keep it stable. For HTTPS deployment,
use the installation's HTTPS URL. Compose generates the signing key automatically
and sets `AUTH_JWT_KEY_FILE=/keys/signing.pem` inside the containers.

The example `.env` also contains `DATABASE_URL` for running Go directly on the host.
**Compose deliberately overrides it:**

```yaml
DATABASE_URL: ""
DATABASE_URL_FILE: /run/secrets/app_database_url
```

Inside Compose the database hostname is `postgres`. In a host-side Go process it
is `localhost` when using the published database port. The Flutter client connects
to the API URL, never to PostgreSQL or `DATABASE_URL`.

For a disposable development environment only, the default `SECRETS_DIR` is
`./secrets/dev`, which contains committed development passwords. Do not mix those
credentials with an existing volume initialized using different passwords.

### 5. Validate and start the complete stack

```bash
docker compose config --quiet
docker compose up -d --build
docker compose ps -a
docker compose logs --tail=80 postgres migrate jwt-keys latihan-api
```

Startup order:

1. PostgreSQL initializes a new volume and database roles, then becomes healthy.
2. `migrate` applies pending SQL migrations as `latihan_migrator`.
3. `jwt-keys` creates a signing key if one does not already exist.
4. `latihan-api` starts as an unprivileged user and connects as `latihan_app`.

`migrate` and `jwt-keys` are one-shot jobs: **Exited (0) is success**. PostgreSQL
and the API should remain running. Use the full stack command on first deployment;
`--no-deps` is only for later API-only recreation when dependencies already work.

### 6. Verify health

```bash
curl --fail-with-body http://localhost:8080/health
curl --fail-with-body http://localhost:8080/ready
```

Both should return `{"status":"ok"}`. `/health` checks process liveness;
`/ready` checks database connectivity and returns 503 when unavailable. A healthy
PostgreSQL container does not prove that the API's database password is correct.

## Secrets and database accounts

| File | Database account | Used by | Purpose |
| --- | --- | --- | --- |
| `postgres_password` | `latihan` | PostgreSQL initialization | Administrator password for a fresh database volume |
| `migrator_password` | `latihan_migrator` | Database initialization and migration job | Schema-owner password used to apply migrations |
| `app_password` | `latihan_app` | Database initialization script | Sets the application account's password on first initialization |
| `app_database_url` | `latihan_app` | **API process** | Full connection URL, including the application password |

The API reads **`app_database_url`**, not `app_password`. For example:

```text
app_password contains:
  YOUR_APP_PASSWORD

app_database_url contains:
  postgres://latihan_app:YOUR_APP_PASSWORD@postgres:5432/latihan?sslmode=disable
```

The password in both files must match the password stored in PostgreSQL for
`latihan_app`. If a manually chosen password contains URL-special characters,
percent-encode them in the URL only. Keep the password file and the PostgreSQL
password as the original value. The initialization script avoids this by generating
alphanumeric passwords.

**Existing database volumes are not reinitialized.** Changing a secret file or
rebuilding an image does not change passwords already stored in PostgreSQL.
Protect and back up secrets and the `jwt-keys` volume; never commit generated secrets.

## Existing databases and password recovery

If PostgreSQL reports `password authentication failed for user "latihan_app"`,
keep the volume and synchronize the application password:

1. Choose the intended password in `secrets/app_password` (or your actual
   `SECRETS_DIR`). Update `app_database_url` privately to contain the same password.
2. Connect through the database container as its administrator:

   ```bash
   docker compose exec postgres psql -U latihan -d latihan
   ```

3. At the psql prompt, run:

   ```text
   \password latihan_app
   ```

   Enter the intended password twice, then exit with `\q`. The interactive
   [psql password command](https://www.postgresql.org/docs/17/app-psql.html)
   avoids putting the plaintext password in your shell command history.

4. Test TCP authentication using that password:

   ```bash
   docker compose exec postgres \
     psql -h postgres -U latihan_app -d latihan -W -c 'SELECT current_user;'
   ```

5. Recreate the API so its configuration and file mounts are refreshed:

   ```bash
   docker compose up -d --no-deps --force-recreate latihan-api
   docker compose logs --tail=50 latihan-api
   curl --fail-with-body http://localhost:8080/ready
   ```

The same procedure applies to `latihan_migrator` or `latihan` only if that specific
account needs a password change; update its matching file as well. If administrator
access prompts for a password, use the database's existing administrator credential.
An unsuccessful API connection is not a reason to reset all database accounts.

If `\du latihan_app` reports no role, the volume may predate the role setup.
Review [db/init/10-roles.sh](db/init/10-roles.sh) and arrange a backed-up role/grant
migration for that existing database; do not assume restarting reruns init scripts.
Never use `docker compose down -v` to repair a database containing data you need.

### Temporary direct URL for disposable local development

If you intentionally use a direct URL, edit the API service's environment in
`docker-compose.yml`, preserving its other settings:

```yaml
DATABASE_URL: "postgres://latihan_app:latihan_app_dev@postgres:5432/latihan?sslmode=disable"
DATABASE_URL_FILE: ""
```

Reset the existing `latihan_app` database password to `latihan_app_dev` using
`\password` and recreate the API. This bypasses the API's secret file; it does not
change the database password automatically. Do not use or commit real credentials
in this example. Restore the file-based configuration for deployment.

## Connect from another device

The base Compose file publishes `127.0.0.1:8080:8080`, so another device cannot
connect to it directly. For a development connection from another computer, keep
the binding private and use an SSH tunnel:

```bash
# Run on the client computer; replace the SSH destination.
ssh -N -L 18080:127.0.0.1:8080 raditya@YOUR_LINUX_HOST
```

The client computer can then call `http://localhost:18080`. For phones and regular
remote use, deploy an HTTPS endpoint as described next. Publishing plain HTTP on a
LAN exposes passwords and bearer tokens in transit. Never publish port 9090 (admin)
or PostgreSQL just to let the mobile app connect.

## Deploy with HTTPS and signed images

The production overlay uses Caddy for HTTPS, removes host-published API/database
ports, and runs a signed release image. Detailed VM and CI setup is in
[docs/DEPLOY.md](docs/DEPLOY.md); this is the application deployment sequence.

1. Prepare the Linux host, Docker Compose, Git, and cosign as described in that guide.
   Configure a domain whose DNS points to the host, and make ports 80/443 reachable.
   The current Caddy configuration expects domain-based certificate issuance;
   a private LAN-only hostname needs a separately configured certificate approach.
2. Clone the repository, create `.env`, and generate secrets **before** the first
   database startup, as in the local-host steps above.
3. On a separate trusted machine, generate an age backup key:

   ```bash
   age-keygen -o latihan-backup-key.txt
   ```

   Keep the private key safely off the server. Put the printed public recipient in
   the server's `.env` along with:

   ```dotenv
   SECRETS_DIR=./secrets
   DOMAIN=api.example.com
   ACME_EMAIL=operator@example.com
   AUTH_PROVIDER=jwt
   AUTH_JWT_ISSUER=https://api.example.com
   BACKUP_AGE_RECIPIENT=age1_REPLACE_WITH_YOUR_PUBLIC_RECIPIENT
   BACKUP_DIR=./backups
   BACKUP_RETENTION_DAYS=30
   BACKUP_UID=1000
   BACKUP_GID=1000
   ```

   Replace the domain, email, recipient, UID, and GID. Use `id -u` and `id -g`
   to obtain the host account's IDs; create a writable backup directory:

   ```bash
   mkdir -p backups
   chmod 700 backups
   ```

4. Obtain a release's commit SHA and image digest from the repository's release
   workflow. If the registry is private, authenticate with `docker login ghcr.io`.
   The overlay requires an image digest, not `latihan-api:local`.
5. Deploy the matching release through the signature-verifying script:

   ```bash
   sh deploy/deploy.sh COMMIT_SHA ghcr.io/perdhevi/latihanapi@sha256:IMAGE_DIGEST
   ```

   Replace both placeholders. This verifies the signature, checks out that commit,
   applies migrations, and waits for service health. Use a dedicated deployment
   checkout because the script changes its Git revision.
6. Verify the public endpoint:

   ```bash
   curl --fail-with-body https://api.example.com/health
   curl --fail-with-body https://api.example.com/ready
   ```

For subsequent production management commands, use **both Compose files** and the
same release image. `deploy/.current` records the deployed commit and image:

```bash
export LATIHAN_IMAGE="$(awk '{print $2}' deploy/.current)"
docker compose -f docker-compose.yml -f docker-compose.prod.yml ps -a
docker compose -f docker-compose.yml -f docker-compose.prod.yml logs --tail=80 latihan-api
```

Production requires encrypted backups. Copy backups off-host and rehearse restoration;
see [backup and restore instructions](docs/DEPLOY.md#backups). Preserve JWT signing
keys and secrets separately from database dumps. Roll back application versions only
when compatible with the current schema; do not blindly reverse data migrations.

## Endpoint reference

Paths are relative to the deployment base URL. Health and JWT provider routes are
public; fitness/profile/account routes require `Authorization: Bearer ACCESS_TOKEN`.
Registering an account does not create its fitness profile: call `POST /api/v1/users`
after obtaining tokens. Firebase/Cognito/OIDC callers obtain tokens from their provider.

### Health and built-in authentication

The authentication routes below are registered when `AUTH_PROVIDER` includes `jwt`.

| Method | Path | Request / purpose |
| --- | --- | --- |
| GET | `/health` | Process liveness; 200 |
| GET | `/ready` | Database connectivity; 200 or 503 |
| POST | `/api/v1/auth/register` | `{"email":"...","password":"..."}`; 201 with tokens |
| POST | `/api/v1/auth/login` | Same fields; 200 with tokens |
| POST | `/api/v1/auth/refresh` | `{"refresh_token":"..."}`; 200 with a new token pair |
| POST | `/api/v1/auth/logout` | `{"refresh_token":"..."}`; revoke refresh session; 204 |
| GET | `/.well-known/jwks.json` | Public signing keys |

Token responses contain `access_token`, `token_type`, `expires_in`, and
`refresh_token`. Store refresh tokens securely. Refresh rotates the token; replace
your stored value with the new one and avoid concurrent refresh requests. Logout
revokes the refresh-token family; an already-issued access token can remain valid
until it expires. Defaults are 15 minutes for access and 30 days for refresh tokens.

### Profiles and measurement history

`{userID}` accepts `me` or the caller's own profile UUID. Other users' IDs return 404.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/v1/users` | Create the authenticated caller's profile: `{"display_name":"Alex"}` |
| GET | `/api/v1/users/{userID}` | Read profile |
| PUT | `/api/v1/users/{userID}` | Replace display name |
| DELETE | `/api/v1/users/{userID}` | Delete profile; dependent records can cause 409 |
| GET | `/api/v1/users/{userID}/measurements` | List history, newest measurement time first |
| POST | `/api/v1/users/{userID}/measurements` | Record timestamped measurements |
| GET | `/api/v1/users/{userID}/measurements/{id}` | Read a historical record |
| PUT | `/api/v1/users/{userID}/measurements/{id}` | Replace a historical record |
| DELETE | `/api/v1/users/{userID}/measurements/{id}` | Delete a historical record |

Measurements require `measured_at` and at least one of `weight_kg`, `height_cm`,
`body_fat_percent`, `waist_cm`, `chest_cm`, or `hip_cm`. Missing values are unknown,
not zero. PUT clears optional fields omitted from the replacement body.

### Sessions and plans

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/sessions` | List your sessions, newest performed time first |
| POST | `/api/v1/sessions` | Record a workout and optional plan attachment |
| GET | `/api/v1/sessions/{id}` | Read a session |
| PUT | `/api/v1/sessions/{id}` | Replace a session |
| DELETE | `/api/v1/sessions/{id}` | Delete a session |
| GET | `/api/v1/plans` | List your plans, newest creation first |
| POST | `/api/v1/plans` | Create a reusable plan |
| GET | `/api/v1/plans/{id}` | Read a plan |
| PUT | `/api/v1/plans/{id}` | Replace a plan |
| DELETE | `/api/v1/plans/{id}` | Delete a plan; attached sessions cause 409 |
| GET | `/api/v1/plans/{id}/comparison?session_id=UUID` | Compare saved plan targets with actual session results |

Bodies contain no `user_id`: ownership comes from the verified token. Strength
exercises contain individual sets with repetitions and weight in kg; cardio entries
contain minutes and optional average BPM. There is no public `/api/v1/exercises`
endpoint. The original catalog table remains only to preserve legacy data.

### Account and internal administration

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/account/export` | Download the caller's profile, identities, measurements, plans, and sessions |
| DELETE | `/api/v1/account` | Erase caller data and, for built-in authentication, its login account |

Account erasure differs from deleting a profile: it removes dependent fitness data.
External-provider identity deletion depends on the provider's capabilities; do not
assume an external Firebase/Cognito/OIDC account is deleted. Existing backups can
retain erased data until their configured retention period ends.

The separate admin listener (default port 9090) exposes `GET /metrics` and, only with
`ADMIN_PPROF=true`, `/debug/pprof/` plus profiling handlers. These are not public API
routes and the supplied Compose stack does not publish the admin port.

### Shared API rules

- Send JSON bodies with `Content-Type: application/json`. Maximum body size is 1 MiB.
- Creates return 201 with a `Location` header; reads/updates return 200; deletes return 204.
- PUT replaces editable fields and never creates a missing record.
- Lists use `limit` (1..100, default 20) and optional `cursor`. Responses contain
  `items`, `limit`, and `next_cursor`; null means no next page. Send the returned
  cursor unchanged. `offset` and `user_id` query parameters are rejected.
- GET responses for individual editable resources provide an `ETag`. Send it as
  `If-Match` when updating/deleting to prevent overwriting a newer version. A stale
  match returns 412; missing `If-Match` returns 428 when `REQUIRE_IF_MATCH=true`.
- Creating POSTs for profiles, plans, sessions, and measurements accept
  `Idempotency-Key` for safe retries. Reuse the same key only for the same request.
- Typical errors: 400 invalid input, 401 invalid/missing authentication, 403 profile
  required, 404 missing/not-owned record, 409 conflict, 413 oversized body, 415 media
  type, 429 rate limit, and 500 unexpected failure. Errors use
  `{"error":{"code":"...","message":"..."}}`. Keep `X-Request-ID` for diagnosis.

## API walkthrough

These examples use Bash, curl, and jq on the API host. Replace the base URL for HTTPS.
The example account/password are for development only. Use `login` instead of
`register` if the account already exists.

```bash
BASE_URL=http://localhost:8080
AUTH=$(curl --fail-with-body -sS "$BASE_URL/api/v1/auth/register" \
  -H 'Content-Type: application/json' \
  -d '{"email":"alex@example.com","password":"correct horse battery staple"}')
TOKEN=$(printf '%s' "$AUTH" | jq -er '.access_token')
REFRESH_TOKEN=$(printf '%s' "$AUTH" | jq -er '.refresh_token')

curl --fail-with-body -sS "$BASE_URL/api/v1/users" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"display_name":"Alex"}'
curl --fail-with-body -sS "$BASE_URL/api/v1/users/me" \
  -H "Authorization: Bearer $TOKEN"
```

Create a plan:

```bash
PLAN=$(curl --fail-with-body -sS "$BASE_URL/api/v1/plans" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Leg day","exercises":[
    {"name":"Squat","kind":"strength","sets":[{"repetitions":8,"weight_kg":50}]},
    {"name":"Running","kind":"cardio","minutes":30,"avg_bpm":140}
  ]}')
PLAN_ID=$(printf '%s' "$PLAN" | jq -er '.id')
SQUAT_ID=$(printf '%s' "$PLAN" | jq -er '.exercises[0].id')
RUN_ID=$(printf '%s' "$PLAN" | jq -er '.exercises[1].id')
```

Record actual results, using plan-entry IDs to match the exercises:

```bash
SESSION_BODY=$(jq -n --arg plan "$PLAN_ID" --arg squat "$SQUAT_ID" \
  --arg run "$RUN_ID" --arg performed "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{name:"Morning training",performed_at:$performed,plan_id:$plan,exercises:[
    {plan_exercise_id:$squat,name:"Squat",kind:"strength",sets:[{repetitions:10,weight_kg:55}]},
    {plan_exercise_id:$run,name:"Running",kind:"cardio",minutes:25,avg_bpm:145}
  ]}')
SESSION=$(curl --fail-with-body -sS "$BASE_URL/api/v1/sessions" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$SESSION_BODY")
SESSION_ID=$(printf '%s' "$SESSION" | jq -er '.id')

curl --fail-with-body -sS \
  "$BASE_URL/api/v1/plans/$PLAN_ID/comparison?session_id=$SESSION_ID" \
  -H "Authorization: Bearer $TOKEN" | jq
```

Deltas are actual minus planned: this example has weightlifting volume `550 - 400
= +150 kg`, minutes `25 - 30 = -5`, and BPM `145 - 140 = +5`. Missing BPM remains
unknown. Comparisons distinguish matched, missed, and unplanned exercises and compare
sets by position. A saved plan snapshot keeps historical comparisons stable when
the reusable plan changes. Keeping the same plan ID on session updates retains that
snapshot; detaching and reattaching takes the current version. Plan replacements
generate new exercise-entry IDs; old sessions continue using their snapshot's IDs.

Record and list measurements:

```bash
curl --fail-with-body -sS "$BASE_URL/api/v1/users/me/measurements" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"measured_at":"2026-10-02T07:00:00Z","weight_kg":80,"height_cm":180,
       "body_fat_percent":15,"waist_cm":80,"chest_cm":100,"hip_cm":90}'
curl --fail-with-body -sS "$BASE_URL/api/v1/users/me/measurements?limit=20" \
  -H "Authorization: Bearer $TOKEN"
```

Refresh tokens when needed (replace both saved tokens after every refresh):

```bash
AUTH=$(jq -n --arg token "$REFRESH_TOKEN" '{refresh_token:$token}' \
  | curl --fail-with-body -sS "$BASE_URL/api/v1/auth/refresh" \
      -H 'Content-Type: application/json' --data-binary @-)
TOKEN=$(printf '%s' "$AUTH" | jq -er '.access_token')
REFRESH_TOKEN=$(printf '%s' "$AUTH" | jq -er '.refresh_token')
```

## Routine operations

For the source-built local stack:

```bash
docker compose ps -a
docker compose logs -f --tail=50 latihan-api
docker compose stop
docker compose up -d
```

After reviewing and retrieving a compatible source update, rebuild and run pending
migrations with `docker compose up -d --build`. Back up the database before schema
changes. After an environment/secret change, use `up --force-recreate`, not just
`restart`, to reload configuration and mounts. No rebuild is needed for a password
file change alone.

Keep the same Compose project name (the repository directory normally determines
it); changing it can select different volumes. `docker compose down` keeps named
volumes, while `down -v` destroys the database and signing-key volumes. Back up both.

For production, always use both Compose files and the deployed image as described
above. Backups, release verification, and restoration are covered in
[docs/DEPLOY.md](docs/DEPLOY.md). Do not apply the local-only management commands
to a production overlay without its corresponding configuration.

## Troubleshooting

| Symptom | Check / next action |
| --- | --- |
| `DATABASE_URL is required` | Check the API environment, `DATABASE_URL_FILE`, and the configured secret directory; exporting a host variable does not override Compose's explicit environment settings. |
| `DATABASE_URL_FILE` read error | Verify the selected host path is a nonempty regular file and readable by container UID 65532. Inspect the actual mount, not a similarly named file elsewhere. |
| Both `DATABASE_URL` and `DATABASE_URL_FILE` set | Choose either the file or direct URL; clear the other. |
| `database unavailable during startup` | Inspect PostgreSQL logs. Check URL hostname `postgres`, account `latihan_app`, and its actual database password. |
| Password authentication failed | Synchronize the role password and matching secret files using the recovery steps. |
| Skipping PostgreSQL initialization | Normal for an existing volume; changed initialization passwords and init scripts are not reapplied. |
| `AUTH_PROVIDER` required | Set the provider in `.env` and recreate the API; see `.env.example`. |
| `profile_required` | Authenticate, then create your profile with `POST /api/v1/users`. |
| 400 on list endpoints | Use `cursor`, not `offset`; do not send `user_id`. |
| Works on server but not another device | Base Compose binds to loopback; use a tunnel or HTTPS deployment. |
| Runtime says Podman | Verify engine/provider versions and Compose support; do not assume Docker-specific runtime behavior. |

Inspect mounted paths without exposing secret values:

```bash
container_id=$(docker compose ps -aq latihan-api)
docker inspect "$container_id" \
  --format '{{range .Mounts}}{{println .Source "->" .Destination}}{{end}}'
```

Inspect recent connection errors:

```bash
docker compose logs --since=5m postgres latihan-api
```

The runtime image is distroless: it has no shell, `cat`, or `wget`. Do not expect
`docker compose exec latihan-api sh` to work. Use the host-side file checks, logs,
and the binary's healthcheck instead. Do not share secret contents or full container
environment dumps. AppArmor signal denials involving Podman's `pasta` process do
not by themselves establish a denial of secret-file access.

## Build and reference documentation

- [Original build README](build-readme.md): preserved engineering history, architecture,
  authentication providers, tests, observability, and security details.
- [OpenAPI specification](api/openapi.yaml): complete schemas and response contracts.
- [Production VM/release guide](docs/DEPLOY.md): host setup, signed images, CI deployment,
  encrypted backups, restore, and rollback.
- [Environment template](.env.example), [base Compose](docker-compose.yml),
  [production overlay](docker-compose.prod.yml), and [secret initialization](scripts/init-secrets.sh).
- [Docker Engine on Ubuntu](https://docs.docker.com/engine/install/ubuntu/),
  [Podman Compose](https://docs.podman.io/en/latest/markdown/podman-compose.1.html),
  and [PostgreSQL psql](https://www.postgresql.org/docs/17/app-psql.html).

For source development, use Go 1.27+ and the [Makefile](Makefile): `make test`,
`make vet`, `make check`, and `make test-integration`. Integration tests need
`TEST_DATABASE_URL` pointing at a disposable database with schema-creation rights.
`make run` loads `.env`; invoking the Go binary directly does not load it for you.
