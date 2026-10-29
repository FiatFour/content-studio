package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/notify"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// leakyFail fails every time with a message that holds a path and a webhook-looking URL, which the
// notification must not repeat.
var leakyFail = stageFunc(func(context.Context, *jobs.Job) error {
	return jobs.Errorf(jobs.ErrValidationFailed, nil, "bad output at /home/studio/storage/run1/a.png via https://discord.com/api/webhooks/1/SECRETTOKEN")
})

type recvNotifier struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (r *recvNotifier) Send(_ context.Context, m notify.Message) notify.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return notify.Result{OK: true, HTTPStatus: 204}
}

func failedNotifications(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT payload::text FROM notifications WHERE event = 'job_failed' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestExhaustedRetriesQueueOneSafeJobFailedNotification(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	w := newWorker(db, map[string]jobs.Stage{"a": leakyFail})
	run, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if runState(t, db, run) != jobs.RunFailed {
		t.Fatalf("run is %s, want failed", runState(t, db, run))
	}
	got := failedNotifications(t, db)
	if len(got) != 1 {
		t.Fatalf("%d job_failed notifications after exhausting retries, want 1 (retries in between must not notify): %v", len(got), got)
	}
	for _, bad := range []string{"/home/studio", "SECRETTOKEN", "discord.com", "webhooks"} {
		if strings.Contains(got[0], bad) {
			t.Fatalf("notification leaks %q: %s", bad, got[0])
		}
	}
	for _, want := range []string{"Job failed", "stage a failed", "validation_failed"} {
		if !strings.Contains(got[0], want) {
			t.Fatalf("notification lacks %q: %s", want, got[0])
		}
	}

	// It is delivered through the outbox to the (fake) receiver.
	rcv := &recvNotifier{}
	svc := &notify.Service{DB: db, Notifier: rcv, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if ran, err := svc.RunOne(context.Background()); err != nil || !ran {
		t.Fatalf("dispatch: ran=%v err=%v", ran, err)
	}
	if len(rcv.msgs) != 1 || rcv.msgs[0].Title != "Job failed" {
		t.Fatalf("receiver got %+v", rcv.msgs)
	}
}

func TestRepeatedFailuresOfOneEpisodeCollapseWithinTenMinutes(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	_, ep := storetest.Episode(t, db)
	_, otherEp := storetest.Episode(t, db)
	w := newWorker(db, map[string]jobs.Stage{"a": leakyFail})

	run, _ := w.Queue.CreateRun(ctx, ep, "full", nil)
	drain(t, w)
	for i := 0; i < 2; i++ { // two manual retries that fail again, all inside the window
		if _, err := w.Queue.Resume(ctx, run); err != nil {
			t.Fatal(err)
		}
		drain(t, w)
	}
	if got := failedNotifications(t, db); len(got) != 1 {
		t.Fatalf("%d notifications for one episode inside 10 minutes, want 1", len(got))
	}

	// Another episode has its own window.
	other, _ := w.Queue.CreateRun(ctx, otherEp, "full", nil)
	drain(t, w)
	if got := failedNotifications(t, db); len(got) != 2 {
		t.Fatalf("%d notifications with a second episode, want 2", len(got))
	}

	// After the window, the next failure goes out and counts the two that were held back.
	if _, err := db.Exec(ctx, `UPDATE job_failed_collapse SET window_start = now() - interval '11 minutes' WHERE episode_id = $1`, ep); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Queue.Resume(ctx, run); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	got := failedNotifications(t, db)
	if len(got) != 3 || !strings.Contains(got[2], "2 more failure(s)") {
		t.Fatalf("after the window: %d notifications, last %q; want 3 and a count of 2", len(got), got[len(got)-1])
	}
	_ = other
}

func TestJobFailedNotificationRespectsToggleAndNeverFailsTheQueue(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	_, ep := storetest.Episode(t, db)
	if _, err := db.Exec(ctx, `INSERT INTO notification_settings (event, enabled) VALUES ('job_failed', false)`); err != nil {
		t.Fatal(err)
	}
	w := newWorker(db, map[string]jobs.Stage{"a": leakyFail})
	run, _ := w.Queue.CreateRun(ctx, ep, "full", nil)
	drain(t, w)
	if got := failedNotifications(t, db); len(got) != 0 {
		t.Fatalf("toggled off but %d notifications queued", len(got))
	}
	var windows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM job_failed_collapse`).Scan(&windows); err != nil || windows != 0 {
		t.Fatalf("toggled off but %d collapse windows recorded (err %v)", windows, err)
	}

	// Broken notification storage must not change the queue outcome.
	if _, err := db.Exec(ctx, `DELETE FROM notification_settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE job_failed_collapse RENAME TO job_failed_collapse_hidden`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Queue.Resume(ctx, run); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if runState(t, db, run) != jobs.RunFailed {
		t.Fatalf("run is %s after a notification error, want failed", runState(t, db, run))
	}
	var state string
	if err := db.QueryRow(ctx, `SELECT state FROM jobs WHERE run_id = $1`, run).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("job is %s (err %v), want failed", state, err)
	}
}

func TestLostLeaseAtTheAttemptLimitAlsoNotifies(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 1, LeaseTTL: 1}
	run, _ := q.CreateRun(ctx, ep, "full", nil)
	if _, err := q.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE jobs SET lease_expires_at = now() - interval '1 minute' WHERE run_id = $1`, run); err != nil {
		t.Fatal(err)
	}
	if n, err := q.RecoverExpired(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d, err %v", n, err)
	}
	if runState(t, db, run) != jobs.RunFailed {
		t.Fatalf("run is %s, want failed", runState(t, db, run))
	}
	got := failedNotifications(t, db)
	if len(got) != 1 || !strings.Contains(got[0], "worker_restart") {
		t.Fatalf("notifications %v, want one naming worker_restart", got)
	}
}
