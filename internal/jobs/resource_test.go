package jobs_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// gpuPlan: stages a and b hold the "gpu" resource; c needs none.
var gpuPlan = jobs.Plan{Profile: "fictional_series", Full: []string{"a", "b", "c"}, Rerender: []string{"c"},
	Resources: map[string]string{"a": "gpu", "b": "gpu"}}

func gpuQueue(db *pgxpool.Pool, owner string) *jobs.Queue {
	return &jobs.Queue{DB: db, Plan: gpuPlan, MaxAttempts: 3, Owner: owner, LeaseTTL: time.Minute}
}

func jobWait(t *testing.T, db *pgxpool.Pool, runID int64, stage string) (state string, reason *string) {
	t.Helper()
	if err := db.QueryRow(context.Background(), `SELECT state, wait_reason FROM jobs WHERE run_id = $1 AND stage = $2`, runID, stage).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSecondGPUJobWaitsWithReasonThenRuns(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	w1, w2 := gpuQueue(db, "w1"), gpuQueue(db, "w2")
	run1, _ := w1.CreateRun(context.Background(), ep, "full", nil)
	run2, _ := w1.CreateRun(context.Background(), ep, "full", nil)

	j1, err := w1.Claim(context.Background())
	if err != nil || j1.RunID != run1 {
		t.Fatalf("first claim: %+v %v", j1, err)
	}
	if _, err := w2.Claim(context.Background()); err != jobs.ErrNoJob {
		t.Fatalf("second worker got the GPU while it was in use: %v", err)
	}
	st, reason := jobWait(t, db, run2, "a")
	if st != "waiting_for_resource" || reason == nil || *reason == "" {
		t.Fatalf("job state %s reason %v", st, reason)
	}
	t.Logf("wait reason: %s", *reason)

	if err := w1.Complete(context.Background(), j1, nil); err != nil {
		t.Fatal(err)
	}
	// Completing run1.a queued run1.b (also GPU) with a lower id than... run2.a is older, so it goes first.
	j2, err := w2.Claim(context.Background())
	if err != nil || j2.RunID != run2 || j2.Stage != "a" {
		t.Fatalf("after the GPU freed: %+v %v", j2, err)
	}
	if st, reason := jobWait(t, db, run2, "a"); st != "running" || reason != nil {
		t.Fatalf("state %s reason %v", st, reason)
	}
}

func TestResourceFreeJobsAreNotBlockedByWaitingOnes(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := gpuQueue(db, "w1")
	run1, _ := q.CreateRun(context.Background(), ep, "full", nil)
	run2, _ := q.CreateRun(context.Background(), ep, "full", nil)
	run3, _ := q.CreateRun(context.Background(), ep, "rerender", &run1) // stage c: no resource
	if j, err := q.Claim(context.Background()); err != nil || j.RunID != run1 {
		t.Fatalf("%+v %v", j, err)
	}
	j, err := gpuQueue(db, "w2").Claim(context.Background())
	if err != nil || j.RunID != run3 || j.Stage != "c" {
		t.Fatalf("CPU job was blocked behind a waiting GPU job: %+v %v", j, err)
	}
	if st, _ := jobWait(t, db, run2, "a"); st != "waiting_for_resource" {
		t.Fatalf("run2.a is %s", st)
	}
}

func TestResourceLimitIsConfigurable(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	if _, err := db.Exec(context.Background(), `UPDATE resource_limits SET max_concurrent = 2 WHERE resource = 'gpu'`); err != nil {
		t.Fatal(err)
	}
	q := gpuQueue(db, "w1")
	runs := make([]int64, 3)
	for i := range runs {
		runs[i], _ = q.CreateRun(context.Background(), ep, "full", nil)
	}
	for i := 0; i < 2; i++ {
		if _, err := q.Claim(context.Background()); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	if _, err := q.Claim(context.Background()); err != jobs.ErrNoJob {
		t.Fatalf("third GPU job ran with a limit of 2: %v", err)
	}
	if st, _ := jobWait(t, db, runs[2], "a"); st != "waiting_for_resource" {
		t.Fatalf("third job is %s", st)
	}
}

func TestUnlistedResourceDefaultsToOneSlot(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	p := jobs.Plan{Profile: "x", Full: []string{"a"}, Resources: map[string]string{"a": "mystery"}}
	q := &jobs.Queue{DB: db, Plan: p, MaxAttempts: 1}
	q.CreateRun(context.Background(), ep, "full", nil)
	q.CreateRun(context.Background(), ep, "full", nil)
	if _, err := q.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(context.Background()); err != jobs.ErrNoJob {
		t.Fatalf("resource with no limit row ran twice: %v", err)
	}
}

func TestWaitingJobCanBeCancelled(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := gpuQueue(db, "w1")
	q.CreateRun(context.Background(), ep, "full", nil)
	run2, _ := q.CreateRun(context.Background(), ep, "full", nil)
	if _, err := q.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(context.Background()); err != jobs.ErrNoJob {
		t.Fatal(err)
	}
	if err := q.RequestCancel(context.Background(), run2); err != nil {
		t.Fatal(err)
	}
	if st, _ := jobWait(t, db, run2, "a"); st != "cancelled" || runState(t, db, run2) != jobs.RunCancelled {
		t.Fatalf("job %s run %s", st, runState(t, db, run2))
	}
}

// Four workers race over six two-stage GPU runs. The number of stages running at once must never
// exceed one, and every run must still finish.
func TestCompetingWorkersNeverShareTheGPU(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	var inUse, peak, total atomic.Int32
	gpuStage := stageFunc(func(ctx context.Context, _ *jobs.Job) error {
		n := inUse.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		inUse.Add(-1)
		total.Add(1)
		return nil
	})
	stages := map[string]jobs.Stage{"a": gpuStage, "b": gpuStage, "c": ok()}
	const runs = 6
	seed := gpuQueue(db, "seed")
	ids := make([]int64, runs)
	for i := range ids {
		ids[i], _ = seed.CreateRun(context.Background(), ep, "full", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		w := newWorker(db, stages)
		w.Queue = gpuQueue(db, "w"+string(rune('0'+i)))
		wg.Add(1)
		go func() {
			defer wg.Done()
			idle := 0
			for ctx.Err() == nil && idle < 40 {
				ran, err := w.RunOne(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if ran {
					idle = 0
				} else {
					idle++
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Fatalf("peak concurrent GPU stages %d, want 1", peak.Load())
	}
	if total.Load() != 2*runs {
		t.Fatalf("GPU stages run %d, want %d", total.Load(), 2*runs)
	}
	for _, id := range ids {
		if got := runState(t, db, id); got != jobs.RunReadyForReview {
			t.Fatalf("run %d is %s", id, got)
		}
	}
}
