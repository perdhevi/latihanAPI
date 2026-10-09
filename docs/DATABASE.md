# Database structure

Generated from `migrations/000001`–`000006`. The migrations are the source of truth; update this file when you add one.

```mermaid
erDiagram
    user_profiles ||--o{ measurements : "user_id"
    user_profiles ||--o{ plans : "user_id"
    user_profiles ||--o{ sessions : "user_id"
    user_profiles ||--o{ user_identities : "user_id (CASCADE)"
    plans |o--o{ sessions : "(plan_id, user_id)"
    local_credentials ||--o{ refresh_tokens : "credential_id (CASCADE)"
    local_credentials }o..o{ user_identities : "id = subject (logical, no FK)"

    exercises {
        uuid id PK
        text name
        text description
        text category "strength|cardio|mobility|stretching"
        text muscle_group
        text equipment
        timestamptz created_at
        timestamptz updated_at
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
        jsonb exercises "1..100 items"
    }
    sessions {
        uuid id PK
        uuid user_id FK
        text name
        text notes
        timestamptz performed_at
        uuid plan_id FK "nullable"
        jsonb plan_snapshot "nullable"
        jsonb exercises "1..100 items"
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
| `exercises` | 1 | Global exercise catalog, not user-owned |
| `user_profiles` | 2 | The local user; every user-owned row hangs off it |
| `measurements` | 2 | Body measurements over time |
| `plans` | 2 | Workout plans; exercises/sets stored as one JSONB document |
| `sessions` | 2 | Performed workouts; optional link to a plan plus a snapshot of it |
| `user_identities` | 3 | Maps (issuer, subject) from an auth provider to a profile |
| `local_credentials` | 4 | Built-in JWT provider accounts (email + argon2id hash) |
| `refresh_tokens` | 4 | Hashed, rotating refresh tokens grouped in families |
| `idempotency_keys` | 5 | Cached POST responses, kept 24h |
| `audit_events` | 6 | Append-only log of who changed what, never values |

## Notable design choices

- `sessions` references plans through a composite FK `(plan_id, user_id)`, so a session can never point at another user's plan.
- `audit_events` has no FK to `user_profiles`, so records survive erasure of the user. Triggers write it in the same transaction as the change. The API role can only `INSERT` into it.
- `user_identities` is keyed by `(issuer, subject)`, and cascades when a profile is deleted.
- Plan and session exercise lists are JSONB documents, always read and written whole. Their shape is validated in Go (`internal/training`).
