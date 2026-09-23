-- Optional, repeatable development data. Never run by application startup.
BEGIN;
INSERT INTO user_profiles (id, display_name)
VALUES ('10000000-0000-4000-8000-000000000001', 'Demo Athlete')
ON CONFLICT (id) DO NOTHING;

INSERT INTO plans (id, user_id, name, exercises) VALUES (
    '20000000-0000-4000-8000-000000000001',
    '10000000-0000-4000-8000-000000000001',
    'Strength and cardio',
    '[{"id":"50000000-0000-4000-8000-000000000001","name":"Squat","kind":"strength","sets":[{"repetitions":8,"weight_kg":50},{"repetitions":8,"weight_kg":50}]},
      {"id":"50000000-0000-4000-8000-000000000002","name":"Running","kind":"cardio","minutes":30,"avg_bpm":140}]'
) ON CONFLICT (id) DO NOTHING;

INSERT INTO sessions (id, user_id, name, performed_at, plan_id, plan_snapshot, exercises)
SELECT '30000000-0000-4000-8000-000000000001', p.user_id, 'Morning training',
    '2026-09-23T08:00:00Z', p.id, to_jsonb(p),
    '[{"plan_exercise_id":"50000000-0000-4000-8000-000000000001","name":"Squat","kind":"strength","sets":[{"repetitions":10,"weight_kg":55}]},
      {"plan_exercise_id":"50000000-0000-4000-8000-000000000002","name":"Running","kind":"cardio","minutes":25,"avg_bpm":145}]'::jsonb
FROM plans p WHERE p.id = '20000000-0000-4000-8000-000000000001'
ON CONFLICT (id) DO NOTHING;

INSERT INTO measurements (id, user_id, measured_at, weight_kg, height_cm, body_fat_percent, waist_cm, chest_cm, hip_cm)
VALUES ('40000000-0000-4000-8000-000000000001', '10000000-0000-4000-8000-000000000001',
    '2026-09-23T07:00:00Z', 80, 180, 15, 80, 100, 90)
ON CONFLICT (id) DO NOTHING;
COMMIT;
