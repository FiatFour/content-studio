package jobs_test

import (
	"context"
	"io"
	"log/slog"
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
	if n, err := restarted.Queue.RecoverInterrupted(context.Background()); err != nil || n != 1 {
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
	if _, err := q.RecoverInterrupted(context.Background()); err != nil {
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
	if err := w.Queue.RetryFailed(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if calls != 6 || runState(t, db, runID) != jobs.RunFailed {
		t.Fatalf("after manual retry: %d calls, state %s (want 6 and failed)", calls, runState(t, db, runID))
	}
}
