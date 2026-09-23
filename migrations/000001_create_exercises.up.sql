CREATE TABLE exercises (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    description TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 4000),
    category TEXT NOT NULL CHECK (category IN ('strength', 'cardio', 'mobility', 'stretching')),
    muscle_group TEXT NOT NULL DEFAULT '' CHECK (char_length(muscle_group) <= 100),
    equipment TEXT NOT NULL DEFAULT '' CHECK (char_length(equipment) <= 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (updated_at >= created_at)
);

-- Supports the API's deterministic, paginated catalog ordering.
CREATE INDEX exercises_name_id_idx ON exercises (name, id);
