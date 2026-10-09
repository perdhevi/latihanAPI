-- Optional, repeatable development data. Never run by application startup.
-- Needs the exercise catalog from migration 000008_seed_exercises.
BEGIN;
INSERT INTO user_profiles (id, display_name)
VALUES ('10000000-0000-4000-8000-000000000001', 'Demo Athlete')
ON CONFLICT (id) DO NOTHING;

INSERT INTO plans (id, user_id, name)
VALUES ('20000000-0000-4000-8000-000000000001', '10000000-0000-4000-8000-000000000001', 'Strength and cardio')
ON CONFLICT (id) DO NOTHING;

-- Planned: squat 2x8 at 50 kg, then 30 minutes of running.
INSERT INTO plan_exercises (id, plan_id, position, exercise_id, name, kind, notes, minutes, avg_bpm)
SELECT v.id::uuid, '20000000-0000-4000-8000-000000000001', v.position, e.id, e.name, e.category, v.notes, v.minutes, v.avg_bpm
FROM (VALUES
    ('50000000-0000-4000-8000-000000000001', 1, '1686b204-597c-5141-99e3-e24a5fc79b2e', 'Controlled descent', NULL::float8, NULL::int),
    ('50000000-0000-4000-8000-000000000002', 2, 'fb3f228e-fb47-55b6-92b5-0a5828d578de', '', 30::float8, 140::int)
) AS v(id, position, exercise_id, notes, minutes, avg_bpm)
JOIN exercises e ON e.id = v.exercise_id::uuid
ON CONFLICT (id) DO NOTHING;

INSERT INTO plan_sets (plan_exercise_id, set_number, repetitions, weight_kg) VALUES
    ('50000000-0000-4000-8000-000000000001', 1, 8, 50),
    ('50000000-0000-4000-8000-000000000001', 2, 8, 50)
ON CONFLICT DO NOTHING;

INSERT INTO sessions (id, user_id, name, performed_at, plan_id)
VALUES ('30000000-0000-4000-8000-000000000001', '10000000-0000-4000-8000-000000000001', 'Morning training',
    '2026-09-23T08:00:00Z', '20000000-0000-4000-8000-000000000001')
ON CONFLICT (id) DO NOTHING;

-- Done: one heavier squat set, a shorter run.
INSERT INTO session_exercises (id, session_id, position, exercise_id, plan_exercise_id, name, kind, notes, minutes, avg_bpm)
SELECT v.id::uuid, '30000000-0000-4000-8000-000000000001', v.position, e.id, v.plan_exercise_id::uuid, e.name, e.category, v.notes, v.minutes, v.avg_bpm
FROM (VALUES
    ('60000000-0000-4000-8000-000000000001', 1, '1686b204-597c-5141-99e3-e24a5fc79b2e', '50000000-0000-4000-8000-000000000001', 'Felt strong', NULL::float8, NULL::int),
    ('60000000-0000-4000-8000-000000000002', 2, 'fb3f228e-fb47-55b6-92b5-0a5828d578de', '50000000-0000-4000-8000-000000000002', '', 25::float8, 145::int)
) AS v(id, position, exercise_id, plan_exercise_id, notes, minutes, avg_bpm)
JOIN exercises e ON e.id = v.exercise_id::uuid
ON CONFLICT (id) DO NOTHING;

INSERT INTO session_sets (session_exercise_id, set_number, repetitions, weight_kg) VALUES
    ('60000000-0000-4000-8000-000000000001', 1, 10, 55)
ON CONFLICT DO NOTHING;

INSERT INTO measurements (id, user_id, measured_at, weight_kg, height_cm, body_fat_percent, waist_cm, chest_cm, hip_cm)
VALUES ('40000000-0000-4000-8000-000000000001', '10000000-0000-4000-8000-000000000001',
    '2026-09-23T07:00:00Z', 80, 180, 15, 80, 100, 90)
ON CONFLICT (id) DO NOTHING;
COMMIT;
