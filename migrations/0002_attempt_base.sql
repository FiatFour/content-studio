-- attempt_base is the attempt number at the last manual retry, so each manual retry gets a full
-- attempt budget while attempt numbers (and their directories) keep increasing.
ALTER TABLE jobs ADD COLUMN attempt_base int NOT NULL DEFAULT 0;
