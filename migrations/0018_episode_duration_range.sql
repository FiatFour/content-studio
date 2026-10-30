-- M5 T2: the episode length range is per content profile (Fictional Series 60-120 s, Knowledge Shorts
-- 30-60 s). The exact range per profile is enforced in Go from the profile Spec; this CHECK is only the
-- database backstop for the union of the ranges. It only widens the old 60-120 rule, so every existing
-- row still satisfies it and no data is touched.
ALTER TABLE episodes DROP CONSTRAINT episodes_target_duration_s_check;
ALTER TABLE episodes ADD CONSTRAINT episodes_target_duration_s_check CHECK (target_duration_s BETWEEN 30 AND 120);
