-- Accounts for the built-in "jwt" authentication provider. Like an external
-- provider's accounts, they are separate from profiles: a credential's id is
-- the token subject, linked to a profile through user_identities.
CREATE TABLE local_credentials (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254 AND email = lower(email)),
    -- PHC-formatted argon2id hash, including its parameters and salt.
    password_hash TEXT NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    failed_logins INTEGER NOT NULL DEFAULT 0 CHECK (failed_logins >= 0),
    locked_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX local_credentials_email_idx ON local_credentials (email);

-- Refresh tokens are stored only as SHA-256 hashes. Each use rotates the token
-- within its family; presenting an already-used token revokes the family.
CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY,
    credential_id UUID NOT NULL REFERENCES local_credentials(id) ON DELETE CASCADE,
    family_id UUID NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_credential_idx ON refresh_tokens (credential_id);
