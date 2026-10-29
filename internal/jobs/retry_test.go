package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// noDelay makes retries due immediately, so tests drain the queue without waiting.
func noDelay(*jobs.StageError, int) time.Duration { return 0 }

// Every class, run through the real queue with an attempt bound of 3: retryable classes run exactly
// 3 times then fail with their class; the rest run once. insufficient_local_resources passes through
// waiting_for_resource, rate_limited through retry_scheduled.
func TestEveryErrorClassRetriesOrStopsAsTheTableSays(t *testing.T) {
	for _, class := range jobs.AllErrorClasses() {
		t.Run(string(class), func(t *testing.T) {
			db := storetest.New(t)
			_, ep := storetest.Episode(t, db)
			calls := 0
			stage := stageFunc(func(context.Context, *jobs.Job) error {
				calls++
				return &jobs.StageError{Class: class, Message: "boom"}
			})
			w := newWorker(db, map[string]jobs.Stage{"a": stage, "b": ok(), "c": ok()})
			runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
			drain(t, w)

			wantCalls := 1
			if class.Retries() {
				wantCalls = 3
			}
			if class == jobs.ErrScriptDuration {
				// Not a stage retry: the run fails and the queue starts at most MaxAutoRewrites new runs (unchanged M1a behaviour).
				wantCalls = 1 + jobs.MaxAutoRewrites
			}
			if calls != wantCalls {
				t.Fatalf("stage ran %d times, want %d", calls, wantCalls)
			}
			st := jobStates(t, db, runID)["a"]
			switch class {
			case jobs.ErrCancelled:
				if st != "cancelled:cancelled" || runState(t, db, runID) != jobs.RunCancelled {
					t.Fatalf("job %s run %s", st, runState(t, db, runID))
				}
			default:
				if st != "failed:"+string(class) || runState(t, db, runID) != jobs.RunFailed {
					t.Fatalf("job %s run %s", st, runState(t, db, runID))
				}
			}
			// The attempts table shows how each try ended.
			var attempts int
			if err := db.QueryRow(context.Background(), `SELECT count(*) FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.run_id = $1 AND j.stage = 'a'`, runID).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			wantAttempts := wantCalls
			if class == jobs.ErrScriptDuration {
				wantAttempts = 1 // the rewrite runs are other jobs
			}
			if attempts != wantAttempts {
				t.Fatalf("recorded %d attempts, want %d", attempts, wantAttempts)
			}
		})
	}
}

func TestRetryScheduledGetsBackoffRunAfter(t *testing.T) {
	cases := []struct {
		class  jobs.ErrorClass
		after  time.Duration
		jitter float64
		want   time.Duration
		state  string
	}{
		{jobs.ErrTransientNetwork, 0, 0, time.Second, "retry_scheduled"},
		{jobs.ErrTransientNetwork, 0, 1, 2 * time.Second, "retry_scheduled"},
		{jobs.ErrProviderOutage, 0, 0, 15 * time.Second, "retry_scheduled"},
		{jobs.ErrRateLimited, 2 * time.Minute, 0, 2 * time.Minute, "retry_scheduled"},
		{jobs.ErrRateLimited, 0, 0, 2500 * time.Millisecond, "retry_scheduled"},
		{jobs.ErrInsufficientLocal, 0, 0, 7500 * time.Millisecond, "waiting_for_resource"},
	}
	for _, c := range cases {
		db := storetest.New(t)
		_, ep := storetest.Episode(t, db)
		jit := c.jitter
		q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3, Jitter: func() float64 { return jit }}
		runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
		j, err := q.Claim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := q.Fail(context.Background(), j, &jobs.StageError{Class: c.class, Message: "x", RetryAfter: c.after}); err != nil {
			t.Fatal(err)
		}
		var state string
		var secs float64
		var reason *string
		if err := db.QueryRow(context.Background(), `SELECT state, extract(epoch FROM run_after - updated_at), wait_reason FROM jobs WHERE run_id = $1 AND stage = 'a'`, runID).
			Scan(&state, &secs, &reason); err != nil {
			t.Fatal(err)
		}
		if state != c.state || time.Duration(secs*float64(time.Second)) != c.want {
			t.Errorf("%s: state %s delay %.3fs, want %s %v", c.class, state, secs, c.state, c.want)
		}
		if c.state == "waiting_for_resource" && (reason == nil || *reason == "") {
			t.Errorf("%s: waiting job has no visible reason", c.class)
		}
		// Not claimable before run_after.
		if _, err := q.Claim(context.Background()); err != jobs.ErrNoJob {
			t.Errorf("%s: job claimed before its backoff elapsed: %v", c.class, err)
		}
	}
}

func TestDelayGrowsWithEachFailure(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 4, Jitter: func() float64 { return 1 }}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	var got []time.Duration
	for i := 0; i < 3; i++ {
		// Make the previous retry due, claim it, fail it again.
		if _, err := db.Exec(context.Background(), `UPDATE jobs SET run_after = now() WHERE state = 'retry_scheduled'`); err != nil {
			t.Fatal(err)
		}
		j, err := q.Claim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := q.Fail(context.Background(), j, jobs.Errorf(jobs.ErrTransientNetwork, nil, "x")); err != nil {
			t.Fatal(err)
		}
		var secs float64
		if err := db.QueryRow(context.Background(), `SELECT extract(epoch FROM run_after - updated_at) FROM jobs WHERE run_id = $1 AND stage = 'a'`, runID).Scan(&secs); err != nil {
			t.Fatal(err)
		}
		got = append(got, time.Duration(secs*float64(time.Second)))
	}
	if got[0] != 2*time.Second || got[1] != 4*time.Second || got[2] != 8*time.Second {
		t.Fatalf("delays %v, want 2s 4s 8s", got)
	}
}

// uncertain_outcome must never be retried by the queue, however many attempts remain.
func TestUncertainOutcomeIsNeverRetriedAutomatically(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 10, Delay: noDelay}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	j, _ := q.Claim(context.Background())
	if err := q.Fail(context.Background(), j, jobs.Errorf(jobs.ErrUncertainOutcome, nil, "request may have been accepted")); err != nil {
		t.Fatal(err)
	}
	if st := jobStates(t, db, runID)["a"]; st != "failed:uncertain_outcome" {
		t.Fatalf("job %s", st)
	}
	if _, err := q.Claim(context.Background()); err != jobs.ErrNoJob {
		t.Fatalf("uncertain job was claimed again: %v", err)
	}
}

func TestWaitingForResourceRetryWakesOnlyAfterRunAfter(t *testing.T) {
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: plan, MaxAttempts: 3, Jitter: func() float64 { return 0 }}
	runID, _ := q.CreateRun(context.Background(), ep, "full", nil)
	j, _ := q.Claim(context.Background())
	if err := q.Fail(context.Background(), j, jobs.Errorf(jobs.ErrInsufficientLocal, nil, "vram busy")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(context.Background()); err != jobs.ErrNoJob {
		t.Fatalf("woke before backoff: %v", err)
	}
	if _, err := db.Exec(context.Background(), `UPDATE jobs SET run_after = now() - interval '1 second' WHERE run_id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	if j2, err := q.Claim(context.Background()); err != nil || j2.Attempt != 2 {
		t.Fatalf("after backoff: %+v %v", j2, err)
	}
}
