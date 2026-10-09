-- Plans and sessions stored their exercises as JSONB documents. Progress
-- ("planned vs done") is a question about exercises and sets, so they become
-- rows: user -> sessions -> session_exercises -> session_sets, and
-- plans -> plan_exercises -> plan_sets, with a session exercise pointing at the
-- plan exercise it was meant to fulfil.
--
-- The JSONB data is discarded, not converted. Deleting through the tables lets
-- the audit triggers record it.
DELETE FROM sessions;
DELETE FROM plans;

ALTER TABLE sessions DROP COLUMN plan_snapshot;
ALTER TABLE sessions DROP COLUMN exercises;
ALTER TABLE plans DROP COLUMN exercises;

-- Planned exercises of a plan. exercise_id links the shared catalog; name is
-- always present so an exercise outside the catalog can still be planned.
-- Strength exercises have sets; every other kind is timed.
CREATE TABLE plan_exercises (
    id UUID PRIMARY KEY,
    plan_id UUID NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 1),
    exercise_id UUID REFERENCES exercises(id),
    name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    kind TEXT NOT NULL CHECK (kind IN ('strength', 'cardio', 'mobility', 'stretching')),
    notes TEXT NOT NULL DEFAULT '' CHECK (char_length(notes) <= 1000),
    minutes DOUBLE PRECISION CHECK (minutes > 0 AND minutes <= 1440),
    avg_bpm INTEGER CHECK (avg_bpm BETWEEN 1 AND 300),
    UNIQUE (plan_id, position),
    CHECK ((kind = 'strength' AND minutes IS NULL AND avg_bpm IS NULL) OR (kind <> 'strength' AND minutes IS NOT NULL))
);
CREATE INDEX plan_exercises_exercise_idx ON plan_exercises (exercise_id) WHERE exercise_id IS NOT NULL;

CREATE TABLE plan_sets (
    plan_exercise_id UUID NOT NULL REFERENCES plan_exercises(id) ON DELETE CASCADE,
    set_number INTEGER NOT NULL CHECK (set_number BETWEEN 1 AND 100),
    repetitions INTEGER NOT NULL CHECK (repetitions BETWEEN 1 AND 1000),
    weight_kg DOUBLE PRECISION NOT NULL CHECK (weight_kg >= 0 AND weight_kg <= 2000),
    PRIMARY KEY (plan_exercise_id, set_number)
);

-- What was actually done. plan_exercise_id is set when the entry fulfils a
-- planned exercise; entries without it are extra, unplanned work.
CREATE TABLE session_exercises (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 1),
    exercise_id UUID REFERENCES exercises(id),
    plan_exercise_id UUID REFERENCES plan_exercises(id),
    name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    kind TEXT NOT NULL CHECK (kind IN ('strength', 'cardio', 'mobility', 'stretching')),
    notes TEXT NOT NULL DEFAULT '' CHECK (char_length(notes) <= 1000),
    minutes DOUBLE PRECISION CHECK (minutes > 0 AND minutes <= 1440),
    avg_bpm INTEGER CHECK (avg_bpm BETWEEN 1 AND 300),
    UNIQUE (session_id, position),
    CHECK ((kind = 'strength' AND minutes IS NULL AND avg_bpm IS NULL) OR (kind <> 'strength' AND minutes IS NOT NULL))
);
-- A planned exercise is fulfilled at most once per session.
CREATE UNIQUE INDEX session_exercises_plan_entry_idx ON session_exercises (session_id, plan_exercise_id) WHERE plan_exercise_id IS NOT NULL;
-- Also supports checking whether a plan's exercises are in use.
CREATE INDEX session_exercises_plan_exercise_idx ON session_exercises (plan_exercise_id) WHERE plan_exercise_id IS NOT NULL;
CREATE INDEX session_exercises_exercise_idx ON session_exercises (exercise_id) WHERE exercise_id IS NOT NULL;

CREATE TABLE session_sets (
    session_exercise_id UUID NOT NULL REFERENCES session_exercises(id) ON DELETE CASCADE,
    set_number INTEGER NOT NULL CHECK (set_number BETWEEN 1 AND 100),
    repetitions INTEGER NOT NULL CHECK (repetitions BETWEEN 1 AND 1000),
    weight_kg DOUBLE PRECISION NOT NULL CHECK (weight_kg >= 0 AND weight_kg <= 2000),
    PRIMARY KEY (session_exercise_id, set_number)
);
