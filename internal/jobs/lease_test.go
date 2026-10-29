package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// expireLeases makes every held lease already expired, as if its worker died a TTL ago.
func expireLeases(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `UPDATE jobs SET lease_expires_at = now() - interval '1 second' WHERE state = 'running'`); err != nil {
		t.Fatal(err)
	}
}

func workerNamed(db *pgxpool.Pool, owner string, ttl time.Duration, maxAttempts int, stages map[string]jobs.Stage) *jobs.Worker {
	w := newWorker(db, stages)
	w.Queue = &jobs.Queue{DB: db, Plan: plan, MaxAttempts: maxAttempts, Owner: owner, LeaseTTL: ttl, Delay: noDelay}
	return w
}

func jobRow(t *testing.T, db *pgxpool.Pool, runID int64, stage string) (state string, owner *string, attempts int, class *string) {
	t.Helper()
	err := db.QueryRow(context.Background(), `SELECT state, lease_owner, attempt_count, error_class FROM jobs WHERE run_id = $1 AND stage = $2`, runID, stage).
		Scan(&state, &owner, &attempts, &class)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestClaimTakesALeaseAndHeartbeatRenewsIt(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3, Owner: "w1", LeaseTTL: time.Minute}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	j, err := q.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, owner, _, _ := jobRow(t, db, runID, "a"); owner == nil || *owner != "w1" {
		t.Fatalf("lease owner %v", owner)
	}
	// Pretend the lease is nearly out, then heartbeat: it must be a full TTL again.
	if _, err := db.Exec(context.Background(), `UPDATE jobs SET lease_expires_at = now() + interval '1 second' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := q.Heartbeat(context.Background(), j); err != nil || !ok {
		t.Fatalf("heartbeat ok=%v err=%v", ok, err)
	}
	var secs float64
	if err := db.QueryRow(context.Background(), `SELECT extract(epoch FROM lease_expires_at - now()) FROM jobs WHERE id = $1`, j.ID).Scan(&secs); err != nil {
		t.Fatal(err)
	}
	if secs < 50 {
		t.Fatalf("lease renewed to only %.1fs", secs)
	}
	// Completing releases the lease.
	if err := q.Complete(context.Background(), j, nil); err != nil {
		t.Fatal(err)
	}
	if _, owner, _, _ := jobRow(t, db, runID, "a"); owner != nil {
		t.Fatalf("lease kept after completion: %v", *owner)
	}
}

func TestLiveLeaseIsNotRecovered(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3, Owner: "w1", LeaseTTL: time.Minute}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	if _, err := q.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	other := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3, Owner: "w2"}
	if n, err := other.RecoverExpired(context.Background()); err != nil || n != 0 {
		t.Fatalf("recovered %d, err %v", n, err)
	}
	if st, owner, _, _ := jobRow(t, db, runID, "a"); st != "running" || *owner != "w1" {
		t.Fatalf("job %s owned by %v", st, owner)
	}
}

// The worker dies mid-job (its stage never returns, no heartbeat is sent). After the lease
// expires a second worker takes the job over and finishes the run; the first worker's late result is refused.
func TestWorkerDiesMidJobAndAnotherTakesOver(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	var calls atomic.Int32
	stages := map[string]jobs.Stage{"a": ok(), "b": stageFunc(func(context.Context, *jobs.Job) error { calls.Add(1); return nil }), "c": ok()}
	dead := workerNamed(db, "dead", time.Minute, 3, stages)
	runID, _ := dead.Queue.CreateRun(context.Background(), ep, "full", nil)
	if _, err := dead.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	ghost, err := dead.Queue.Claim(context.Background()) // stage b: claimed, then the process dies
	if err != nil {
		t.Fatal(err)
	}

	alive := workerNamed(db, "alive", time.Minute, 3, stages)
	if ran, err := alive.RunOne(context.Background()); err != nil || ran {
		t.Fatalf("took the job before its lease expired: ran=%v err=%v", ran, err)
	}

	expireLeases(t, db)
	drain(t, alive)
	if got := runState(t, db, runID); got != jobs.RunReadyForReview {
		t.Fatalf("run state %s", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("stage b ran %d times", calls.Load())
	}
	if _, _, attempts, _ := jobRow(t, db, runID, "b"); attempts != 2 {
		t.Fatalf("attempts %d, want 2 (dead worker's and the takeover)", attempts)
	}
	// The dead worker comes back and tries to record a result for its old claim.
	if err := dead.Queue.Complete(context.Background(), ghost, nil); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("late Complete: %v", err)
	}
	if err := dead.Queue.Fail(context.Background(), ghost, jobs.Errorf(jobs.ErrInvalidInput, nil, "late")); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("late Fail: %v", err)
	}
	if got := runState(t, db, runID); got != jobs.RunReadyForReview {
		t.Fatalf("late writes changed run state to %s", got)
	}
}

func TestExpiredLeaseOverAttemptLimitFailsWithReason(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 2, Owner: "w1"}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	for i := 0; i < 2; i++ {
		if _, err := q.Claim(context.Background()); err != nil {
			t.Fatal(err)
		}
		expireLeases(t, db)
		if n, err := q.RecoverExpired(context.Background()); err != nil || n != 1 {
			t.Fatalf("round %d: recovered %d err %v", i, n, err)
		}
		if i == 0 {
			if st, _, _, _ := jobRow(t, db, runID, "a"); st != "queued" {
				t.Fatalf("first expiry: state %s, want queued", st)
			}
		}
	}
	st, owner, _, class := jobRow(t, db, runID, "a")
	if st != "failed" || owner != nil || class == nil || *class != "worker_restart" || runState(t, db, runID) != jobs.RunFailed {
		t.Fatalf("job %s class %v run %s", st, class, runState(t, db, runID))
	}
	var msg, outcome string
	if err := db.QueryRow(context.Background(), `SELECT j.error_message, a.outcome FROM jobs j JOIN job_attempts a ON a.job_id = j.id AND a.n = 2 WHERE j.run_id = $1`, runID).Scan(&msg, &outcome); err != nil {
		t.Fatal(err)
	}
	if msg == "" || outcome != "failed" {
		t.Fatalf("reason %q outcome %q", msg, outcome)
	}
}

func TestExpiredLeaseOfCancelledRunCancelsIt(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3, Owner: "w1"}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	if _, err := q.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := q.RequestCancel(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	expireLeases(t, db)
	if _, err := q.RecoverExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := jobRow(t, db, runID, "a"); st != "cancelled" || runState(t, db, runID) != jobs.RunCancelled {
		t.Fatalf("job %s run %s", st, runState(t, db, runID))
	}
}

// A healthy worker with a short lease keeps it by heartbeat across several lease lengths.
func TestHeartbeatKeepsLeaseDuringLongStage(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	long := stageFunc(func(ctx context.Context, _ *jobs.Job) error {
		select {
		case <-time.After(1500 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	w := workerNamed(db, "w1", 400*time.Millisecond, 3, map[string]jobs.Stage{"a": long, "b": ok(), "c": ok()})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	done := make(chan error, 1)
	go func() { _, err := w.RunOne(context.Background()); done <- err }()
	other := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3, Owner: "w2"}
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n, err := other.RecoverExpired(context.Background()); err != nil || n != 0 {
			t.Fatalf("recovered a live job: n=%d err=%v", n, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st, _, attempts, _ := jobRow(t, db, runID, "a"); st != "completed" || attempts != 1 {
		t.Fatalf("job %s attempts %d", st, attempts)
	}
}

// A stalled worker whose lease was recovered must stop its stage and record nothing.
func TestWorkerStopsStageWhenLeaseIsLost(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	started := make(chan struct{})
	block := stageFunc(func(ctx context.Context, _ *jobs.Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	w := workerNamed(db, "stalled", 300*time.Millisecond, 3, map[string]jobs.Stage{"a": block})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	done := make(chan error, 1)
	go func() { _, err := w.RunOne(context.Background()); done <- err }()
	<-started
	// Another worker declares it dead (lease forced to the past) and recovers the job.
	expireLeases(t, db)
	other := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3, Owner: "w2"}
	if n, err := other.RecoverExpired(context.Background()); err != nil || n != 1 {
		t.Fatalf("recovered %d err %v", n, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stage was not stopped after the lease was lost")
	}
	if st, owner, attempts, _ := jobRow(t, db, runID, "a"); st != "queued" || owner != nil || attempts != 1 {
		t.Fatalf("job %s owner %v attempts %d: stalled worker overwrote the recovery", st, owner, attempts)
	}
}
