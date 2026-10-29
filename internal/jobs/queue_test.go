package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

var plan = jobs.Plan{Profile: "fictional_series", Full: []string{"a", "b", "c"}, Rerender: []string{"c"}}

type stageFunc func(ctx context.Context, j *jobs.Job) error

func (f stageFunc) Run(ctx context.Context, j *jobs.Job, _ func(int, int)) (func(pgx.Tx) error, error) {
	return nil, f(ctx, j)
}

func ok() stageFunc { return func(context.Context, *jobs.Job) error { return nil } }

func newWorker(db *pgxpool.Pool, stages map[string]jobs.Stage) *jobs.Worker {
	return &jobs.Worker{
		Queue:  &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3},
		Stages: stages,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func drain(t *testing.T, w *jobs.Worker) {
	t.Helper()
	for i := 0; i < 20; i++ {
		ran, err := w.RunOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("queue did not drain")
}

func runState(t *testing.T, db *pgxpool.Pool, runID int64) jobs.RunState {
	t.Helper()
	var s jobs.RunState
	if err := db.QueryRow(context.Background(), `SELECT state FROM workflow_runs WHERE id = $1`, runID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func jobStates(t *testing.T, db *pgxpool.Pool, runID int64) map[string]string {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT stage, state || coalesce(':' || error_class, '') FROM jobs WHERE run_id = $1`, runID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for rows.Next() {
		var s, st string
		if err := rows.Scan(&s, &st); err != nil {
			t.Fatal(err)
		}
		out[s] = st
	}
	return out
}

func TestRunCompletesEveryStageInOrder(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	var order []string
	rec := func(name string) stageFunc {
		return func(context.Context, *jobs.Job) error { order = append(order, name); return nil }
	}
	w := newWorker(db, map[string]jobs.Stage{"a": rec("a"), "b": rec("b"), "c": rec("c")})
	runID, err := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if got := runState(t, db, runID); got != jobs.RunReadyForReview {
		t.Fatalf("run state %s", got)
	}
	if len(order) != 3 || order[0] != "a" || order[2] != "c" {
		t.Fatalf("order %v", order)
	}
}

func TestNonRetryableFailureFailsRunWithReason(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	bad := stageFunc(func(context.Context, *jobs.Job) error {
		return jobs.Errorf(jobs.ErrInvalidInput, nil, "script names an unknown speaker")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": ok(), "b": bad, "c": ok()})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if got := runState(t, db, runID); got != jobs.RunFailed {
		t.Fatalf("run state %s", got)
	}
	st := jobStates(t, db, runID)
	if st["b"] != "failed:invalid_input" || st["c"] != "" {
		t.Fatalf("states %v", st)
	}
}

func TestRetryableFailureRetriesThenSucceeds(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	calls := 0
	flaky := stageFunc(func(context.Context, *jobs.Job) error {
		calls++
		if calls < 3 {
			return jobs.Errorf(jobs.ErrValidationFailed, nil, "model output did not match schema")
		}
		return nil
	})
	w := newWorker(db, map[string]jobs.Stage{"a": flaky, "b": ok(), "c": ok()})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if calls != 3 || runState(t, db, runID) != jobs.RunReadyForReview {
		t.Fatalf("calls=%d state=%s", calls, runState(t, db, runID))
	}
}

func TestRetriesStopAtAttemptLimit(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	calls := 0
	never := stageFunc(func(context.Context, *jobs.Job) error {
		calls++
		return jobs.Errorf(jobs.ErrValidationFailed, nil, "model output did not match schema")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": never})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if calls != 3 || runState(t, db, runID) != jobs.RunFailed {
		t.Fatalf("calls=%d state=%s", calls, runState(t, db, runID))
	}
}

func TestCrashedWorkerResumesFromLastCompletedStage(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	counts := map[string]int{}
	count := func(name string) stageFunc {
		return func(context.Context, *jobs.Job) error { counts[name]++; return nil }
	}
	stages := map[string]jobs.Stage{"a": count("a"), "b": count("b"), "c": count("c")}
	w := newWorker(db, stages)
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	if _, err := w.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: stage b is claimed and the process dies before it finishes.
	if _, err := w.Queue.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := newWorker(db, stages)
	expireLeases(t, db)
	if n, err := restarted.Queue.RecoverExpired(context.Background()); err != nil || n != 1 {
		t.Fatalf("recovered %d, err %v", n, err)
	}
	drain(t, restarted)
	if counts["a"] != 1 || counts["b"] != 1 || counts["c"] != 1 {
		t.Fatalf("stage runs %v: completed stage a must not rerun", counts)
	}
	if runState(t, db, runID) != jobs.RunReadyForReview {
		t.Fatalf("state %s", runState(t, db, runID))
	}
}

func TestCrashAtAttemptLimitFailsWithReason(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 1}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	if _, err := q.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	expireLeases(t, db)
	if _, err := q.RecoverExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := jobStates(t, db, runID); st["a"] != "failed:worker_restart" || runState(t, db, runID) != jobs.RunFailed {
		t.Fatalf("states %v", st)
	}
}

func TestCancelQueuedRun(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	if err := q.RequestCancel(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if runState(t, db, runID) != jobs.RunCancelled {
		t.Fatalf("state %s", runState(t, db, runID))
	}
	if _, err := q.Claim(context.Background()); err != jobs.ErrNoJob {
		t.Fatalf("cancelled run still claimable: %v", err)
	}
}

func TestCancelRunningStageStopsIt(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	var w *jobs.Worker
	var runID int64
	slow := stageFunc(func(ctx context.Context, j *jobs.Job) error {
		if err := w.Queue.RequestCancel(context.Background(), j.RunID); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	w = newWorker(db, map[string]jobs.Stage{"a": slow})
	runID, _ = w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if st := jobStates(t, db, runID); st["a"] != "cancelled:cancelled" || runState(t, db, runID) != jobs.RunCancelled {
		t.Fatalf("states %v run %s", st, runState(t, db, runID))
	}
}

func TestManualRetryGetsAFullAttemptBudget(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	calls := 0
	never := stageFunc(func(context.Context, *jobs.Job) error {
		calls++
		return jobs.Errorf(jobs.ErrValidationFailed, nil, "bad output")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": never})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if calls != 3 {
		t.Fatalf("first round: %d calls", calls)
	}
	if _, err := w.Queue.Resume(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if calls != 6 || runState(t, db, runID) != jobs.RunFailed {
		t.Fatalf("after manual retry: %d calls, state %s (want 6 and failed)", calls, runState(t, db, runID))
	}
}

func TestWrongLengthDialogueRestartsTheRunWithFeedbackAndStopsAfterTwo(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	tooShort := stageFunc(func(context.Context, *jobs.Job) error {
		return jobs.Errorf(jobs.ErrScriptDuration, nil, "the dialogue had 480 Thai characters; write about 750")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": tooShort})
	first, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)

	rows, err := db.Query(context.Background(), `SELECT id, state, kind, coalesce(feedback, ''), coalesce(parent_run_id, 0) FROM workflow_runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	type run struct {
		id, of                int64
		state, kind, feedback string
	}
	var runs []run
	for rows.Next() {
		var r run
		if err := rows.Scan(&r.id, &r.state, &r.kind, &r.feedback, &r.of); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
	}
	if len(runs) != 1+jobs.MaxAutoRewrites {
		t.Fatalf("%d runs, want the original plus %d rewrites: %+v", len(runs), jobs.MaxAutoRewrites, runs)
	}
	if runs[0].id != first || runs[0].kind != "full" || runs[0].feedback != "" || runs[0].of != 0 {
		t.Fatalf("original run changed: %+v", runs[0])
	}
	for i, r := range runs {
		if r.state != "failed" {
			t.Errorf("run %d is %s, want failed (no worker progress is possible and the last one must wait for a person)", r.id, r.state)
		}
		if i > 0 && (r.kind != "rewrite" || r.feedback == "" || r.of != runs[i-1].id) {
			t.Errorf("rewrite run %+v is not linked to its predecessor with feedback", r)
		}
	}
}

func TestOtherFailuresNeverStartARewrite(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	bad := stageFunc(func(context.Context, *jobs.Job) error {
		return jobs.Errorf(jobs.ErrInvalidInput, nil, "script names an unknown speaker")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": bad})
	w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM workflow_runs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d runs, err %v", n, err)
	}
}

func TestTheDatabaseRefusesContradictoryRunOrigins(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3}
	full, _ := q.CreateRun(context.Background(), ep, "full", nil)
	for name, run := range map[string]func() error{
		"rerender without a parent": func() error { _, err := q.CreateRun(context.Background(), ep, "rerender", nil); return err },
		"full with a parent":        func() error { _, err := q.CreateRun(context.Background(), ep, "full", &full); return err },
		"rewrite through CreateRun": func() error { _, err := q.CreateRun(context.Background(), ep, "rewrite", &full); return err },
	} {
		if err := run(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestResumeRewritesInsteadOfRetryingWhenTheScriptIsTheProblem(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	calls := 0
	tooShort := stageFunc(func(context.Context, *jobs.Job) error {
		calls++
		return jobs.Errorf(jobs.ErrScriptDuration, nil, "write about 750 Thai characters")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": tooShort})
	run, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	var last int64
	if err := db.QueryRow(context.Background(), `SELECT max(id) FROM workflow_runs`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	got, err := w.Queue.Resume(context.Background(), last)
	if err != nil || got == last {
		t.Fatalf("resume of a wrong-length run returned %d (err %v); want a new run, not %d again", got, err, last)
	}
	if _, err := w.Queue.Resume(context.Background(), last); err == nil {
		t.Fatal("the same failed run was rewritten twice")
	}
	var kind, fb string
	if err := db.QueryRow(context.Background(), `SELECT kind, feedback FROM workflow_runs WHERE id = $1`, got).Scan(&kind, &fb); err != nil || kind != "rewrite" || fb == "" {
		t.Fatalf("new run kind=%q feedback=%q err=%v", kind, fb, err)
	}
	if runState(t, db, last) != jobs.RunFailed {
		t.Fatalf("the replaced run is %s, want failed", runState(t, db, last))
	}
	if st := jobStates(t, db, got); st["a"] != "queued" || len(st) != 1 {
		t.Fatalf("the new run's jobs are %v, want only a queued first stage", st)
	}
	if calls != 1+jobs.MaxAutoRewrites {
		t.Fatalf("stage ran %d times; Resume must not have run it again itself", calls)
	}
	_ = run
}

func TestResumeKeepsTheRunWhenTheStageCanSimplyRunAgain(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	bad := stageFunc(func(context.Context, *jobs.Job) error {
		return jobs.Errorf(jobs.ErrValidationFailed, nil, "bad output")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": bad})
	run, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if got, err := w.Queue.Resume(context.Background(), run); err != nil || got != run {
		t.Fatalf("resume returned %d, err %v; want the same run %d", got, err, run)
	}
	var jobState string
	if err := db.QueryRow(context.Background(), `SELECT state FROM jobs WHERE run_id = $1`, run).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if runState(t, db, run) != jobs.RunRunning || jobState != "queued" {
		t.Fatalf("after resume the run is %s and its job %s, want running and queued", runState(t, db, run), jobState)
	}
	var base, count int
	if err := db.QueryRow(context.Background(), `SELECT attempt_base, attempt_count FROM jobs WHERE run_id = $1`, run).Scan(&base, &count); err != nil || base != count || count != 3 {
		t.Fatalf("attempt_base=%d attempt_count=%d err=%v; a manual retry must start a fresh budget from 3 attempts", base, count, err)
	}
	var ce *jobs.ConflictError
	if _, err := w.Queue.Resume(context.Background(), run); !errors.As(err, &ce) {
		t.Fatalf("resuming a running run returned %v, want a ConflictError", err)
	}
}

func TestConcurrentResumeOfAWrongLengthRunStartsExactlyOneRewrite(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	tooShort := stageFunc(func(context.Context, *jobs.Job) error {
		return jobs.Errorf(jobs.ErrScriptDuration, nil, "write about 750 Thai characters")
	})
	w := newWorker(db, map[string]jobs.Stage{"a": tooShort})
	w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	var last int64
	if err := db.QueryRow(context.Background(), `SELECT max(id) FROM workflow_runs`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var ok, conflicts atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.Queue.Resume(context.Background(), last)
			var ce *jobs.ConflictError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &ce):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || conflicts.Load() != 3 {
		t.Fatalf("%d resumes succeeded and %d conflicted, want 1 and 3", ok.Load(), conflicts.Load())
	}
}

func TestACancelledRunIsNotRewrittenAfterAWrongLengthFailure(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	j, err := q.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The user cancels after the worker looked at the flag but before Fail commits.
	if _, err := db.Exec(context.Background(), `UPDATE workflow_runs SET cancel_requested = true WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := q.Fail(context.Background(), j, jobs.Errorf(jobs.ErrScriptDuration, nil, "too short"), 0); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM workflow_runs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d runs after a cancelled run failed, want 1 (no rewrite); err %v", n, err)
	}
}
