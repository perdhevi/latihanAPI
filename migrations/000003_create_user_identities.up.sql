-- Links identities vouched for by an authentication provider to local users.
-- A user may have several identities (for example while moving from one
-- provider to another), but each identity belongs to exactly one user.
-- Identities are credentials, not history, so they go when the user goes.
CREATE TABLE user_identities (
    issuer TEXT NOT NULL CHECK (char_length(issuer) BETWEEN 1 AND 500),
    subject TEXT NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 255),
    user_id UUID NOT NULL REFERENCES user_profiles(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (issuer, subject)
);
CREATE INDEX user_identities_user_idx ON user_identities (user_id);
