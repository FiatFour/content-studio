-- Flood collapse for "job failed" notifications (notifications.md): at most one message per episode
-- per 10 minutes. One row per episode holds the current window and how many failures it swallowed;
-- that count is added to the next message.
CREATE TABLE job_failed_collapse (
    episode_id   bigint PRIMARY KEY REFERENCES episodes(id) ON DELETE CASCADE,
    window_start timestamptz NOT NULL,
    suppressed   int NOT NULL DEFAULT 0 CHECK (suppressed >= 0)
);
