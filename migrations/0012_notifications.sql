-- Discord notifications (notifications.md). The webhook URL is a secret and is never stored here;
-- it comes from the environment. These tables hold only toggles, an outbox and an attempt log.
CREATE TABLE notification_settings (
    event      text PRIMARY KEY,
    enabled    boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Outbox: one row per event, unique on event_id so a retry never double-posts.
CREATE TABLE notifications (
    id               bigserial PRIMARY KEY,
    event_id         text NOT NULL UNIQUE,
    event            text NOT NULL,
    payload          jsonb NOT NULL,
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    attempts         int NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now(),
    sent_at          timestamptz
);
CREATE INDEX notifications_due ON notifications (next_attempt_at) WHERE status = 'pending';

-- Every delivery attempt: event, status, HTTP code, time. Never the URL.
CREATE TABLE notification_deliveries (
    id               bigserial PRIMARY KEY,
    notification_id  bigint REFERENCES notifications(id),
    event            text NOT NULL,
    ok               boolean NOT NULL,
    http_status      int,
    error            text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now()
);
