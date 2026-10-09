# Database structure

Generated from `migrations/000001`–`000008`. The migrations are the source of truth; update this file when you add one.

```mermaid
erDiagram
    user_profiles ||--o{ measurements : "user_id"
    user_profiles ||--o{ plans : "user_id"
    user_profiles ||--o{ sessions : "user_id"
    user_profiles ||--o{ user_identities : "user_id (CASCADE)"
    plans |o--o{ sessions : "(plan_id, user_id)"
    plans ||--o{ plan_exercises : "plan_id (CASCADE)"
    plan_exercises ||--o{ plan_sets : "plan_exercise_id (CASCADE)"
    sessions ||--o{ session_exercises : "session_id (CASCADE)"
    session_exercises ||--o{ session_sets : "session_exercise_id (CASCADE)"
    plan_exercises |o--o{ session_exercises : "plan_exercise_id (planned vs done)"
    exercises |o--o{ plan_exercises : "exercise_id"
    exercises |o--o{ session_exercises : "exercise_id"
    local_credentials ||--o{ refresh_tokens : "credential_id (CASCADE)"
    local_credentials }o..o{ user_identities : "id = subject (logical, no FK)"

    exercises {
        uuid id PK
        text name
        text description
        text category "strength|cardio|mobility|stretching"
        text muscle_group
        text equipment
    }
    user_profiles {
        uuid id PK
        text display_name
        timestamptz created_at
        timestamptz updated_at
    }
    measurements {
        uuid id PK
        uuid user_id FK
        timestamptz measured_at
        float weight_kg
        float height_cm
        float body_fat_percent
        float waist_cm
        float chest_cm
        float hip_cm
    }
    plans {
        uuid id PK
        uuid user_id FK
        text name
        text notes
    }
    plan_exercises {
        uuid id PK
        uuid plan_id FK
        int position
        uuid exercise_id FK "nullable, catalog"
        text name
        text kind "strength|cardio|mobility|stretching"
        text notes
        float minutes "timed kinds"
        int avg_bpm
    }
    plan_sets {
        uuid plan_exercise_id PK
        int set_number PK
        int repetitions
        float weight_kg
    }
    sessions {
        uuid id PK
        uuid user_id FK
        text name
        text notes
        timestamptz performed_at
        uuid plan_id FK "nullable"
    }
    session_exercises {
        uuid id PK
        uuid session_id FK
        int position
        uuid exercise_id FK "nullable, catalog"
        uuid plan_exercise_id FK "nullable, null = unplanned extra"
        text name
        text kind
        text notes
        float minutes "timed kinds"
        int avg_bpm
    }
    session_sets {
        uuid session_exercise_id PK
        int set_number PK
        int repetitions
        float weight_kg
    }
    user_identities {
        text issuer PK
        text subject PK
        uuid user_id FK
        timestamptz created_at
    }
    local_credentials {
        uuid id PK
        text email UK
        text password_hash "argon2id"
        int failed_logins
        timestamptz locked_until
    }
    refresh_tokens {
        uuid id PK
        uuid credential_id FK
        uuid family_id
        bytea token_hash UK "sha256"
        timestamptz expires_at
        timestamptz used_at
        timestamptz revoked_at
    }
    idempotency_keys {
        text issuer PK
        text subject PK
        text key PK
        bytea request_hash
        int status_code
        jsonb response_headers
        bytea response_body
        timestamptz created_at
    }
    audit_events {
        uuid id PK
        timestamptz occurred_at
        uuid user_id "no FK, by design"
        text action "insert|update|delete|export|erase"
        text resource_type
        uuid resource_id
    }
```

## Tables

| Table | Migration | Purpose |
|---|---|---|
| `exercises` | 1, seeded by 8 | Shared, read-only exercise catalog |
| `user_profiles` | 2 | The local user; every user-owned row hangs off it |
| `measurements` | 2 | Body measurements over time |
| `plans` | 2 | Workout plans (header: name, notes) |
| `plan_exercises`, `plan_sets` | 7 | What a plan asks for: exercises in order, each with its sets |
| `sessions` | 2 | Performed workouts (header); optional link to a plan |
| `session_exercises`, `session_sets` | 7 | What was done, each exercise optionally pointing at the planned one it fulfils |
| `user_identities` | 3 | Maps (issuer, subject) from an auth provider to a profile |
| `local_credentials` | 4 | Built-in JWT provider accounts (email + argon2id hash) |
| `refresh_tokens` | 4 | Hashed, rotating refresh tokens grouped in families |
| `idempotency_keys` | 5 | Cached POST responses, kept 24h |
| `audit_events` | 6 | Append-only log of who changed what, never values |

## Notable design choices

- **Planned vs done is a join.** `session_exercises.plan_exercise_id` points at the `plan_exercises` row it fulfils; `NULL` means unplanned extra work. `GET /api/v1/stats/progress` sums `plan_sets`/`session_sets` in SQL.
- **Frozen plans.** Once any session uses a plan, its exercises and sets cannot change (the API answers `409 plan_in_use`; name and notes can still change). That keeps past comparisons and statistics true without copying the plan into every session.
- **Catalog link is optional.** `exercise_id` ties an entry to the catalog for aggregation; `name` is always stored so a free-form exercise works too. Strength entries have sets; every other kind is timed (`minutes`).
- `sessions` references plans through a composite FK `(plan_id, user_id)`, so a session can never point at another user's plan.
- `audit_events` has no FK to `user_profiles`, so records survive erasure of the user. Triggers write it in the same transaction as the change, on the four parent tables; exercise and set rows are only ever rewritten together with their parent, which bumps its `updated_at`. The API role can only `INSERT` into it.
- `user_identities` is keyed by `(issuer, subject)`, and cascades when a profile is deleted.
- Primary keys are time-ordered UUIDv7 values generated in Go (`internal/ids`), so inserts append to the index.
- Migration 7 discarded the JSONB-era plans and sessions; its down migration empties them again.
