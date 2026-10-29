package api_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/store/storetest"
)

// breakAudit makes every audit insert fail while leaving the rest of the schema alone.
func breakAudit(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `ALTER TABLE audit_log RENAME TO audit_log_hidden`); err != nil {
		t.Fatal(err)
	}
}

func TestCancelAndRetryRollBackWhenTheAuditRowCannotBeWritten(t *testing.T) {
	ts, db := serverDB(t)
	ctx := context.Background()
	_, ep := storetest.Episode(t, db)
	var runningRun, failedRun int64
	if err := db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'fictional_series', 'full', 'running') RETURNING id`, ep).Scan(&runningRun); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state) VALUES ($1, 'script', 0, 'queued')`, runningRun); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'fictional_series', 'full', 'failed') RETURNING id`, ep).Scan(&failedRun); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state, error_class, error_message) VALUES ($1, 'script', 0, 'failed', 'internal', 'boom')`, failedRun); err != nil {
		t.Fatal(err)
	}

	breakAudit(t, db)
	if code, body := call(t, ts, "POST", "/api/runs/"+strconv.FormatInt(runningRun, 10)+"/cancel", "", "{}"); code != 500 {
		t.Fatalf("cancel with a broken audit returned %d %v, want 500", code, body)
	}
	if code, body := call(t, ts, "POST", "/api/runs/"+strconv.FormatInt(failedRun, 10)+"/retry", "", "{}"); code != 500 {
		t.Fatalf("retry with a broken audit returned %d %v, want 500", code, body)
	}
	var runA, runB, jobA, jobB string
	if err := db.QueryRow(ctx, `SELECT (SELECT state FROM workflow_runs WHERE id = $1), (SELECT state FROM workflow_runs WHERE id = $2),
		(SELECT state FROM jobs WHERE run_id = $1), (SELECT state FROM jobs WHERE run_id = $2)`, runningRun, failedRun).Scan(&runA, &runB, &jobA, &jobB); err != nil {
		t.Fatal(err)
	}
	if runA != "running" || jobA != "queued" || runB != "failed" || jobB != "failed" {
		t.Fatalf("queue changed without its audit row: %s/%s %s/%s", runA, jobA, runB, jobB)
	}
}

func TestNotificationToggleIsAuditedOnceAndRollsBackWithoutAudit(t *testing.T) {
	ts, db, _, _ := notifyServer(t, false, 0, nil)
	ctx := context.Background()

	if code, body := call(t, ts, "PUT", "/api/notifications/events/nope", "", `{"enabled":true}`); code != 404 {
		t.Fatalf("%d %v", code, body)
	}
	call(t, ts, "PUT", "/api/notifications/events/episode_ready", "", `{}`) // refused: enabled missing
	wantActions(t, db)

	if code, body := call(t, ts, "PUT", "/api/notifications/events/version_approved", "", `{"enabled":true}`); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	wantActions(t, db, "notification.toggle")
	var event string
	var enabled bool
	if err := db.QueryRow(ctx, `SELECT detail->>'event', (detail->>'enabled')::bool FROM audit_log`).Scan(&event, &enabled); err != nil {
		t.Fatal(err)
	}
	if event != "version_approved" || !enabled {
		t.Fatalf("audit detail event=%q enabled=%v", event, enabled)
	}

	breakAudit(t, db)
	if code, body := call(t, ts, "PUT", "/api/notifications/events/version_approved", "", `{"enabled":false}`); code != 500 {
		t.Fatalf("toggle with a broken audit returned %d %v, want 500", code, body)
	}
	var on bool
	if err := db.QueryRow(ctx, `SELECT enabled FROM notification_settings WHERE event = 'version_approved'`).Scan(&on); err != nil || !on {
		t.Fatalf("toggle changed without its audit row: enabled=%v err=%v", on, err)
	}
}
