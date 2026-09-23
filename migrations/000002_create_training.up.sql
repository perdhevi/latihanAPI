-- The original exercises table is retained to preserve existing catalog data.
CREATE TABLE user_profiles (
    id UUID PRIMARY KEY,
    display_name TEXT NOT NULL CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 200),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE measurements (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES user_profiles(id),
    measured_at TIMESTAMPTZ NOT NULL,
    weight_kg DOUBLE PRECISION CHECK (weight_kg > 0 AND weight_kg <= 1000),
    height_cm DOUBLE PRECISION CHECK (height_cm > 0 AND height_cm <= 300),
    body_fat_percent DOUBLE PRECISION CHECK (body_fat_percent >= 0 AND body_fat_percent <= 100),
    waist_cm DOUBLE PRECISION CHECK (waist_cm > 0 AND waist_cm <= 500),
    chest_cm DOUBLE PRECISION CHECK (chest_cm > 0 AND chest_cm <= 500),
    hip_cm DOUBLE PRECISION CHECK (hip_cm > 0 AND hip_cm <= 500),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (num_nonnulls(weight_kg, height_cm, body_fat_percent, waist_cm, chest_cm, hip_cm) > 0)
);
CREATE INDEX measurements_user_history_idx ON measurements (user_id, measured_at DESC, id DESC);

-- Bounded ordered exercise/set documents are always read and replaced as a unit.
-- Their shape and metric constraints are validated by the training service.
CREATE TABLE plans (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES user_profiles(id),
    name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    notes TEXT NOT NULL DEFAULT '' CHECK (char_length(notes) <= 4000),
    exercises JSONB NOT NULL CHECK (jsonb_typeof(exercises) = 'array' AND jsonb_array_length(exercises) BETWEEN 1 AND 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (id, user_id)
);
CREATE INDEX plans_user_created_idx ON plans (user_id, created_at DESC, id DESC);

CREATE TABLE sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES user_profiles(id),
    name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    notes TEXT NOT NULL DEFAULT '' CHECK (char_length(notes) <= 4000),
    performed_at TIMESTAMPTZ NOT NULL,
    plan_id UUID,
    plan_snapshot JSONB,
    exercises JSONB NOT NULL CHECK (jsonb_typeof(exercises) = 'array' AND jsonb_array_length(exercises) BETWEEN 1 AND 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (plan_id, user_id) REFERENCES plans(id, user_id),
    CHECK ((plan_id IS NULL AND plan_snapshot IS NULL) OR (plan_id IS NOT NULL AND plan_snapshot IS NOT NULL AND jsonb_typeof(plan_snapshot) = 'object'))
);
CREATE INDEX sessions_user_history_idx ON sessions (user_id, performed_at DESC, id DESC);
-- Supports checking whether a plan can be deleted without orphaning sessions.
CREATE INDEX sessions_plan_idx ON sessions (plan_id) WHERE plan_id IS NOT NULL;
