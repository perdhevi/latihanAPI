-- Responses to POST requests that carried an Idempotency-Key, so a retry of the
-- same request gets the same answer instead of creating a duplicate. Keys are
-- scoped to the caller's identity (not the profile, which may not exist yet
-- when POST /api/v1/users is retried) and kept for 24 hours.
CREATE TABLE idempotency_keys (
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    key TEXT NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
    -- SHA-256 of method, path and body: the same key with a different request is refused.
    request_hash BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    -- NULL while the first request is still running.
    status_code INTEGER CHECK (status_code BETWEEN 100 AND 599),
    response_headers JSONB,
    response_body BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (issuer, subject, key)
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);
