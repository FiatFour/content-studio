package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

var errHook = errors.New("forced hook failure")

func TestRequestCancelTxRollsBackWhenTheHookFails(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3}
	run, _ := q.CreateRun(context.Background(), ep, "full", nil)
	err := q.RequestCancelTx(context.Background(), run, func(pgx.Tx) error { return errHook })
	if !errors.Is(err, errHook) {
		t.Fatalf("err %v, want the hook error", err)
	}
	if runState(t, db, run) != jobs.RunRunning {
		t.Fatalf("run is %s after a failed hook, want running", runState(t, db, run))
	}
	var cancelled bool
	var jobState string
	if err := db.QueryRow(context.Background(), `SELECT r.cancel_requested, j.state FROM workflow_runs r JOIN jobs j ON j.run_id = r.id WHERE r.id = $1`, run).Scan(&cancelled, &jobState); err != nil {
		t.Fatal(err)
	}
	if cancelled || jobState != "queued" {
		t.Fatalf("cancel_requested=%v job=%s, want the cancel rolled back", cancelled, jobState)
	}
	called := false
	if err := q.RequestCancelTx(context.Background(), run, func(pgx.Tx) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("cancel with a passing hook: err %v called %v", err, called)
	}
	if runState(t, db, run) != jobs.RunCancelled {
		t.Fatalf("run is %s, want cancelled", runState(t, db, run))
	}
}

func TestResumeTxRollsBackWhenTheHookFailsAndPassesTheResumedRun(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	bad := stageFunc(func(context.Context, *jobs.Job) error {
		return jobs.Errorf(jobs.ErrValidationFailed, nil, "bad output")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": bad})
	run, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if runState(t, db, run) != jobs.RunFailed {
		t.Fatalf("setup: run is %s", runState(t, db, run))
	}
	if _, err := w.Queue.ResumeTx(context.Background(), run, func(pgx.Tx, int64) error { return errHook }); !errors.Is(err, errHook) {
		t.Fatalf("err %v, want the hook error", err)
	}
	var jobState string
	var base int
	if err := db.QueryRow(context.Background(), `SELECT state, attempt_base FROM jobs WHERE run_id = $1`, run).Scan(&jobState, &base); err != nil {
		t.Fatal(err)
	}
	if runState(t, db, run) != jobs.RunFailed || jobState != "failed" || base != 0 {
		t.Fatalf("run %s job %s attempt_base %d after a failed hook, want failed/failed/0", runState(t, db, run), jobState, base)
	}
	var seen int64
	got, err := w.Queue.ResumeTx(context.Background(), run, func(_ pgx.Tx, resumed int64) error { seen = resumed; return nil })
	if err != nil || got != run || seen != run {
		t.Fatalf("resume returned %d (hook saw %d), err %v; want run %d", got, seen, err, run)
	}
	if runState(t, db, run) != jobs.RunRunning {
		t.Fatalf("run is %s, want running", runState(t, db, run))
	}
}
