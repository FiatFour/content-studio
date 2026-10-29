package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service owns settings, the outbox dispatcher and the test message. Notifier is nil when no
// webhook is configured: nothing is delivered and the status says "not configured".
type Service struct {
	DB       *pgxpool.Pool
	Notifier Notifier
	Suffix   string // last 4 characters of the webhook URL; the only part the UI may show
	Log      *slog.Logger

	MaxAttempts int                             // default 5
	Backoff     func(attempt int) time.Duration // default 30s doubling, capped at 5 min
	Poll        time.Duration                   // default 5s
}

const maxRetryWait = 5 * time.Minute

func (s *Service) maxAttempts() int {
	if s.MaxAttempts > 0 {
		return s.MaxAttempts
	}
	return 5
}

func (s *Service) backoff(attempt int) time.Duration {
	if s.Backoff != nil {
		return s.Backoff(attempt)
	}
	d := 30 * time.Second << min(attempt-1, 4)
	return min(d, maxRetryWait)
}

func (s *Service) Configured() bool { return s.Notifier != nil }

// Toggle is one event's current setting.
type Toggle struct {
	Event       Event
	Enabled     bool
	Default     bool
	Implemented bool
}

func (s *Service) Toggles(ctx context.Context) ([]Toggle, error) {
	rows, err := s.DB.Query(ctx, `SELECT event, enabled FROM notification_settings`)
	if err != nil {
		return nil, err
	}
	over := map[Event]bool{}
	for rows.Next() {
		var e string
		var on bool
		if err := rows.Scan(&e, &on); err != nil {
			return nil, err
		}
		over[Event(e)] = on
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Toggle, 0, len(Events))
	for _, i := range Events {
		on, ok := over[i.Event]
		if !ok {
			on = i.Default
		}
		out = append(out, Toggle{i.Event, on, i.Default, i.Implemented})
	}
	return out, nil
}

// ErrUnknownEvent is returned when toggling an event that is not in the spec list.
var ErrUnknownEvent = errors.New("unknown notification event")

func (s *Service) SetEnabled(ctx context.Context, e Event, on bool) error {
	if _, ok := lookup(e); !ok {
		return ErrUnknownEvent
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO notification_settings (event, enabled) VALUES ($1, $2)
		ON CONFLICT (event) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`, string(e), on)
	return err
}

// Delivery is one recorded attempt. It never holds the URL.
type Delivery struct {
	Event      string
	OK         bool
	HTTPStatus *int
	Error      string
	At         time.Time
}

func (s *Service) Recent(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := s.DB.Query(ctx, `SELECT event, ok, http_status, error, created_at FROM notification_deliveries ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.Event, &d.OK, &d.HTTPStatus, &d.Error, &d.At); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Service) record(ctx context.Context, notificationID *int64, e string, r Result) error {
	var code *int
	if r.HTTPStatus != 0 {
		code = &r.HTTPStatus
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO notification_deliveries (notification_id, event, ok, http_status, error) VALUES ($1, $2, $3, $4, $5)`,
		notificationID, e, r.OK, code, r.Error)
	return err
}

// ErrNotConfigured means no webhook is set.
var ErrNotConfigured = errors.New("discord webhook is not configured")

// SendTest posts a clearly labelled test message right away and returns the real result.
func (s *Service) SendTest(ctx context.Context) (Result, error) {
	if s.Notifier == nil {
		return Result{}, ErrNotConfigured
	}
	r := s.Notifier.Send(ctx, BuildMessage(Test, Info{}))
	if err := s.record(ctx, nil, string(Test), r); err != nil {
		return r, err
	}
	return r, nil
}

// Run delivers due notifications until ctx ends. It is its own loop with its own retries, so a
// Discord outage never touches the episode jobs.
func (s *Service) Run(ctx context.Context) {
	poll := s.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	for {
		for ctx.Err() == nil {
			ran, err := s.RunOne(ctx)
			if err != nil {
				s.Log.Error("notification dispatch failed", "err", err)
				break
			}
			if !ran {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// RunOne claims one due notification and attempts delivery. The claim pushes next_attempt_at out
// a lease, so a crash mid-send retries later instead of being lost (delivery is at-least-once;
// Discord offers no idempotency key).
func (s *Service) RunOne(ctx context.Context) (bool, error) {
	var id int64
	var event string
	var payload []byte
	var attempts int
	err := s.DB.QueryRow(ctx, `UPDATE notifications SET attempts = attempts + 1, next_attempt_at = now() + interval '2 minutes'
		WHERE id = (SELECT id FROM notifications WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING id, event, payload, attempts`).Scan(&id, &event, &payload, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		return true, s.finish(ctx, id, "failed", 0, Result{Error: "stored message is invalid"})
	}
	if s.Notifier == nil {
		return true, s.finish(ctx, id, "skipped", 0, Result{Error: "not configured"})
	}
	r := s.Notifier.Send(ctx, msg)
	log := s.Log.With("notification_id", id, "event", event, "attempt", attempts, "http_status", r.HTTPStatus)
	switch {
	case r.OK:
		log.Info("notification sent")
		return true, s.finish(ctx, id, "sent", 0, r)
	case attempts >= s.maxAttempts():
		log.Warn("notification failed, retries exhausted", "error", r.Error)
		return true, s.finish(ctx, id, "failed", 0, r)
	default:
		wait := s.backoff(attempts)
		if r.RetryAfter > 0 {
			wait = max(wait, min(r.RetryAfter, maxRetryWait)) // honour Discord's retry_after, bounded
		}
		log.Warn("notification attempt failed, will retry", "error", r.Error, "retry_in_s", wait.Seconds())
		return true, s.finish(ctx, id, "pending", wait, r)
	}
}

func (s *Service) finish(ctx context.Context, id int64, status string, wait time.Duration, r Result) error {
	// Detach from a cancelled ctx so a shutdown still records the attempt that just happened.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	event := ""
	if err := s.DB.QueryRow(ctx, `UPDATE notifications SET status = $2, sent_at = CASE WHEN $2::text = 'sent' THEN now() END,
		next_attempt_at = CASE WHEN $2::text = 'pending' THEN now() + make_interval(secs => $3) ELSE next_attempt_at END
		WHERE id = $1 RETURNING event`, id, status, wait.Seconds()).Scan(&event); err != nil {
		return err
	}
	return s.record(ctx, &id, event, r)
}
