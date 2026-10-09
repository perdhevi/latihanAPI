-- The exercise rows cannot be turned back into documents, so sessions and plans
-- are emptied, as the up migration emptied them.
DELETE FROM sessions;
DELETE FROM plans;

DROP TABLE session_sets;
DROP TABLE session_exercises;
DROP TABLE plan_sets;
DROP TABLE plan_exercises;

ALTER TABLE plans ADD COLUMN exercises JSONB NOT NULL CHECK (jsonb_typeof(exercises) = 'array' AND jsonb_array_length(exercises) BETWEEN 1 AND 100);
ALTER TABLE sessions ADD COLUMN exercises JSONB NOT NULL CHECK (jsonb_typeof(exercises) = 'array' AND jsonb_array_length(exercises) BETWEEN 1 AND 100);
ALTER TABLE sessions ADD COLUMN plan_snapshot JSONB;
ALTER TABLE sessions ADD CHECK ((plan_id IS NULL AND plan_snapshot IS NULL) OR (plan_id IS NOT NULL AND plan_snapshot IS NOT NULL AND jsonb_typeof(plan_snapshot) = 'object'));
