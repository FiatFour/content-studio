package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// keyedStage is a fake provider-backed stage: every Run is one "provider call" and stores one artifact.
type keyedStage struct {
	project, episode int64
	calls            int
	hash             string
	hashErr          error
}

func (k *keyedStage) InputHash(context.Context, *jobs.Job) (string, error) { return k.hash, k.hashErr }

func (k *keyedStage) Run(_ context.Context, j *jobs.Job, _ func(int, int)) (func(pgx.Tx) error, error) {
	k.calls++
	n := k.calls
	return func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO artifacts (project_id, episode_id, run_id, job_id, type, item_key, version, storage_path, sha256, size_bytes, validation_status)
			VALUES ($1, $2, $3, $4, 'script', '', $5, 'p', 'h', 1, 'valid')`, k.project, k.episode, j.RunID, j.ID, n)
		return err
	}, nil
}

func countRows(t *testing.T, db *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// requeueCompleted puts a completed job back in the queue by hand, as an operator or a duplicate
// enqueue would (the transition table has no completed -> queued edge on purpose).
func requeueCompleted(t *testing.T, db *pgxpool.Pool, runID int64, stage string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `UPDATE jobs SET state = 'queued', finished_at = NULL WHERE run_id = $1 AND stage = $2 AND state = 'completed'`, runID, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE workflow_runs SET state = 'running', finished_at = NULL WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
}

func TestReenqueuedCompletedStageReusesArtifactsAndSkipsTheProvider(t *testing.T) {
	db := storetest.New(t)
	project, ep := storetest.Episode(t, db)
	k := &keyedStage{project: project, episode: ep, hash: "input-v1"}
	w := newWorker(db, map[string]jobs.Stage{"a": k, "b": ok(), "c": ok()})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if k.calls != 1 || runState(t, db, runID) != jobs.RunReadyForReview {
		t.Fatalf("first pass: calls=%d run=%s", k.calls, runState(t, db, runID))
	}
	key := jobs.IdempotencyKey(runID, "a", "input-v1")
	if n := countRows(t, db, `SELECT count(*) FROM stage_executions WHERE idempotency_key = $1`, key); n != 1 {
		t.Fatalf("recorded executions for %q: %d", key, n)
	}

	for _, stage := range plan.Full {
		requeueCompleted(t, db, runID, stage)
	}
	drain(t, w)

	if k.calls != 1 {
		t.Fatalf("provider called again: %d calls", k.calls)
	}
	if n := countRows(t, db, `SELECT count(*) FROM artifacts WHERE run_id = $1`, runID); n != 1 {
		t.Fatalf("%d artifacts, want the original 1", n)
	}
	if st := jobStates(t, db, runID); st["a"] != "completed" || st["b"] != "completed" || st["c"] != "completed" {
		t.Fatalf("job states %v", st)
	}
	if got := runState(t, db, runID); got != jobs.RunReadyForReview {
		t.Fatalf("run %s", got)
	}
	if n := countRows(t, db, `SELECT count(*) FROM job_attempts a JOIN jobs j ON j.id = a.job_id WHERE j.run_id = $1 AND j.stage = 'a'`, runID); n != 2 {
		t.Fatalf("attempts %d: the reused completion is still an attempt on record", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM stage_executions WHERE run_id = $1`, runID); n != 1 {
		t.Fatalf("reuse must not record a second key: %d rows", n)
	}
}

func TestChangedInputRunsTheStageAgain(t *testing.T) {
	db := storetest.New(t)
	project, ep := storetest.Episode(t, db)
	k := &keyedStage{project: project, episode: ep, hash: "input-v1"}
	w := newWorker(db, map[string]jobs.Stage{"a": k, "b": ok(), "c": ok()})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)

	k.hash = "input-v2"
	requeueCompleted(t, db, runID, "a")
	drain(t, w)
	if k.calls != 2 {
		t.Fatalf("a different input must run the stage: %d calls", k.calls)
	}
	if n := countRows(t, db, `SELECT count(*) FROM stage_executions WHERE run_id = $1 AND stage = 'a'`, runID); n != 2 {
		t.Fatalf("executions recorded: %d", n)
	}
}

func TestKeysAreScopedToTheRun(t *testing.T) {
	db := storetest.New(t)
	project, ep := storetest.Episode(t, db)
	k := &keyedStage{project: project, episode: ep, hash: "same-input"}
	w := newWorker(db, map[string]jobs.Stage{"a": k, "b": ok(), "c": ok()})
	w.Queue.CreateRun(context.Background(), ep, "full", nil)
	w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if k.calls != 2 {
		t.Fatalf("two runs with identical input share nothing: %d calls", k.calls)
	}
}

func TestUnhashableInputRunsWithoutAKey(t *testing.T) {
	db := storetest.New(t)
	project, ep := storetest.Episode(t, db)
	k := &keyedStage{project: project, episode: ep, hashErr: errors.New("input missing")}
	w := newWorker(db, map[string]jobs.Stage{"a": k, "b": ok(), "c": ok()})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	if k.calls != 1 || runState(t, db, runID) != jobs.RunReadyForReview {
		t.Fatalf("calls=%d run=%s", k.calls, runState(t, db, runID))
	}
	if n := countRows(t, db, `SELECT count(*) FROM stage_executions`); n != 0 {
		t.Fatalf("a key was recorded without an input hash")
	}
}

// A completed key whose artifacts are gone is not reusable: the stage must run again.
func TestKeyWithoutArtifactsIsNotReused(t *testing.T) {
	db := storetest.New(t)
	project, ep := storetest.Episode(t, db)
	k := &keyedStage{project: project, episode: ep, hash: "input-v1"}
	w := newWorker(db, map[string]jobs.Stage{"a": k, "b": ok(), "c": ok()})
	runID, _ := w.Queue.CreateRun(context.Background(), ep, "full", nil)
	drain(t, w)
	// Artifacts are immutable, so simulate "nothing stored" by pointing a's key at job b, which stored none.
	if _, err := db.Exec(context.Background(), `UPDATE stage_executions SET job_id = (SELECT id FROM jobs WHERE run_id = $1 AND stage = 'b')
		WHERE run_id = $1 AND stage = 'a'`, runID); err != nil {
		t.Fatal(err)
	}
	requeueCompleted(t, db, runID, "a")
	drain(t, w)
	if k.calls != 2 {
		t.Fatalf("stage was skipped although its artifacts are gone: %d calls", k.calls)
	}
}
