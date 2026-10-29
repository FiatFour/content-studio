package jobs

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Plan is the ordered stage list for each run kind. It comes from the content profile.
type Plan struct {
	Profile  string
	Full     []string
	Rerender []string
	// Resources maps a stage to the limited resource it holds while running (for example "gpu").
	// A stage not listed needs none. Limits live in the resource_limits table.
	Resources map[string]string
}

func (p Plan) resource(stage string) string { return p.Resources[stage] }

func (p Plan) stages(kind string) []string {
	if kind == "rerender" {
		return p.Rerender
	}
	return p.Full
}

type Job struct {
	ID          int64
	RunID       int64
	EpisodeID   int64
	ProjectID   int64
	RunKind     string
	ParentRunID *int64
	Stage       string
	Seq         int
	State       State
	Attempt     int
	AttemptBase int
	LeaseOwner  string
	// IdempotencyKey is set by the worker for a stage that can hash its inputs (see Keyed). Complete
	// records it with the completion, so the same key later finds this execution's artifacts.
	IdempotencyKey string
	InputHash      string
}

type Queue struct {
	DB          *pgxpool.Pool
	Plan        Plan
	MaxAttempts int
	// Owner names this worker on the leases it takes. It must be unique per live worker process.
	Owner string
	// LeaseTTL is how long a claimed job stays this worker's without a heartbeat. Zero means DefaultLeaseTTL.
	LeaseTTL time.Duration
	// Delay and Jitter replace the retry backoff and its random source. They exist for tests;
	// production leaves both nil.
	Delay  func(se *StageError, attemptsUsed int) time.Duration
	Jitter func() float64
}

// DefaultLeaseTTL is long enough that a busy but healthy worker never misses a heartbeat (they come
// every third of it) and short enough that a dead worker's job is picked up within a minute.
const DefaultLeaseTTL = 60 * time.Second

func (q *Queue) leaseTTL() time.Duration {
	if q.LeaseTTL > 0 {
		return q.LeaseTTL
	}
	return DefaultLeaseTTL
}

func (q *Queue) owner() string {
	if q.Owner != "" {
		return q.Owner
	}
	return "worker"
}

var ErrNoJob = errors.New("no job ready")

// ErrLeaseLost means the job is no longer this worker's: its lease expired and it was recovered
// (and maybe claimed by another worker). The holder must stop and write nothing.
var ErrLeaseLost = errors.New("job lease lost")

// ConflictError means the request is valid but the run is in the wrong state for it. Its message
// names only the state, so the API can show it as is.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

func conflictf(format string, args ...any) error {
	return &ConflictError{Msg: fmt.Sprintf(format, args...)}
}

// CreateRun starts a run for an episode and queues its first stage. A "full" run has no parent; a
// "rerender" run has the run whose inputs it reuses. Rewrite runs are created only by the queue itself
// (startRewrite), because they need feedback; the database rejects any other combination.
func (q *Queue) CreateRun(ctx context.Context, episodeID int64, kind string, parentRunID *int64) (int64, error) {
	stages := q.Plan.stages(kind)
	if len(stages) == 0 {
		return 0, fmt.Errorf("unknown run kind %q", kind)
	}
	var runID int64
	err := pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, parent_run_id, state)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, episodeID, q.Plan.Profile, kind, parentRunID, RunRunning).Scan(&runID); err != nil {
			return err
		}
		return insertJob(ctx, tx, runID, stages[0], 0, q.Plan.resource(stages[0]))
	})
	return runID, err
}

func insertJob(ctx context.Context, tx pgx.Tx, runID int64, stage string, seq int, resource string) error {
	_, err := tx.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state, resource) VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT (run_id, stage) DO NOTHING`, runID, stage, seq, Queued, resource)
	return err
}

// claimScanLimit bounds how many queued jobs one Claim looks at (and row-locks) while skipping
// jobs that are blocked on a resource.
const claimScanLimit = 20

// Claim promotes due retries, wakes waiting jobs whose resource has a free slot, then takes the
// oldest queued job it can run and moves it to running. A queued job whose resource has no free
// slot is moved to waiting_for_resource (with its reason) and the next job is tried, so one busy
// resource never blocks jobs that need none. The slot count and the claim are decided under a
// per-resource advisory lock, so two workers can never both take the last slot.
func (q *Queue) Claim(ctx context.Context) (*Job, error) {
	var j Job
	claimed := false
	err := pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, updated_at = now() WHERE state = $2 AND run_after <= now()`, Queued, RetryScheduled); err != nil {
			return err
		}
		if err := Transition(WaitingForResource, Queued); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs j SET state = $1, wait_reason = NULL, run_after = NULL, updated_at = now() WHERE j.state = $2
			AND (j.run_after IS NULL OR j.run_after <= now())
			AND (SELECT count(*) FROM jobs r WHERE r.state = $3 AND r.resource = j.resource)
				< COALESCE((SELECT max_concurrent FROM resource_limits WHERE resource = j.resource), 1)`,
			Queued, WaitingForResource, Running); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT j.id, j.run_id, r.episode_id, e.project_id, r.kind, r.parent_run_id, j.stage, j.seq, j.state, j.attempt_count, j.attempt_base, coalesce(j.resource, '')
			FROM jobs j JOIN workflow_runs r ON r.id = j.run_id JOIN episodes e ON e.id = r.episode_id
			WHERE j.state = $1 ORDER BY j.id LIMIT $2 FOR UPDATE OF j SKIP LOCKED`, Queued, claimScanLimit)
		if err != nil {
			return err
		}
		type candidate struct {
			Job
			resource string
		}
		var cands []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.ID, &c.RunID, &c.EpisodeID, &c.ProjectID, &c.RunKind, &c.ParentRunID, &c.Stage, &c.Seq, &c.State, &c.Attempt, &c.AttemptBase, &c.resource); err != nil {
				rows.Close()
				return err
			}
			cands = append(cands, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range cands {
			if c.resource != "" {
				free, used, limit, err := resourceFree(ctx, tx, c.resource)
				if err != nil {
					return err
				}
				if !free {
					if err := Transition(c.State, WaitingForResource); err != nil {
						return err
					}
					reason := fmt.Sprintf("waiting for resource %q: %d of %d slots in use", c.resource, used, limit)
					if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, wait_reason = $2, updated_at = now() WHERE id = $3`, WaitingForResource, reason, c.ID); err != nil {
						return err
					}
					continue
				}
			}
			j = c.Job
			if err := Transition(j.State, Running); err != nil {
				return err
			}
			j.State, j.Attempt, j.LeaseOwner = Running, j.Attempt+1, q.owner()
			if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, attempt_count = $2, started_at = now(), updated_at = now(),
				error_class = NULL, error_message = NULL, wait_reason = NULL, progress_done = 0,
				lease_owner = $4, lease_expires_at = now() + $5::interval, heartbeat_at = now() WHERE id = $3`,
				j.State, j.Attempt, j.ID, j.LeaseOwner, leaseInterval(q.leaseTTL())); err != nil {
				return err
			}
			claimed = true
			_, err = tx.Exec(ctx, `INSERT INTO job_attempts (job_id, n) VALUES ($1, $2)`, j.ID, j.Attempt)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, ErrNoJob
	}
	return &j, nil
}

// resourceFree reports whether the resource has a free slot, taking its advisory lock first so the
// count stays true until the caller's transaction ends. A resource with no resource_limits row
// gets one slot, the safe default.
func resourceFree(ctx context.Context, tx pgx.Tx, resource string) (free bool, used, limit int, err error) {
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('resource:' || $1::text, 0))`, resource); err != nil {
		return false, 0, 0, err
	}
	err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM jobs WHERE state = $2 AND resource = $1),
		COALESCE((SELECT max_concurrent FROM resource_limits WHERE resource = $1), 1)`, resource, Running).Scan(&used, &limit)
	return used < limit, used, limit, err
}

func leaseInterval(d time.Duration) string { return fmt.Sprintf("%d milliseconds", d.Milliseconds()) }

// lockRunning re-reads the job under a row lock so a state change is checked against the real current
// state. It also fences the caller: unless the job is still running under this very claim (same
// attempt, same lease owner), the lease was lost and the caller gets ErrLeaseLost.
func lockRunning(ctx context.Context, tx pgx.Tx, j *Job) (State, error) {
	var s State
	var attempt int
	var owner *string
	err := tx.QueryRow(ctx, `SELECT state, attempt_count, lease_owner FROM jobs WHERE id = $1 FOR UPDATE`, j.ID).Scan(&s, &attempt, &owner)
	if err != nil {
		return s, err
	}
	if s != Running || attempt != j.Attempt || owner == nil || *owner != j.LeaseOwner {
		return s, ErrLeaseLost
	}
	return s, nil
}

// Heartbeat renews the job's lease. It reports false when the lease is no longer this worker's.
func (q *Queue) Heartbeat(ctx context.Context, j *Job) (bool, error) {
	tag, err := q.DB.Exec(ctx, `UPDATE jobs SET lease_expires_at = now() + $4::interval, heartbeat_at = now()
		WHERE id = $1 AND state = $2 AND attempt_count = $3 AND lease_owner = $5`,
		j.ID, Running, j.Attempt, leaseInterval(q.leaseTTL()), j.LeaseOwner)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func finishAttempt(ctx context.Context, tx pgx.Tx, j *Job, outcome State, class ErrorClass, msg string) error {
	_, err := tx.Exec(ctx, `UPDATE job_attempts SET finished_at = now(), outcome = $1, error_class = NULLIF($2, ''), error_message = NULLIF($3, '')
		WHERE job_id = $4 AND n = $5`, outcome, string(class), msg, j.ID, j.Attempt)
	return err
}

// Complete marks the job completed in the same transaction as persist (the stage's artifact rows),
// then queues the next stage or marks the run ready for review after the last one.
func (q *Queue) Complete(ctx context.Context, j *Job, persist func(pgx.Tx) error) error {
	return q.complete(ctx, j, persist, false)
}

// CompleteReused completes a job whose idempotency key already has a completed execution: no
// artifacts are stored (the earlier execution's are the output) and no new key is recorded.
func (q *Queue) CompleteReused(ctx context.Context, j *Job) error {
	return q.complete(ctx, j, nil, true)
}

func (q *Queue) complete(ctx context.Context, j *Job, persist func(pgx.Tx) error, reused bool) error {
	return pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		cur, err := lockRunning(ctx, tx, j)
		if err != nil {
			return err
		}
		if err := Transition(cur, Completed); err != nil {
			return err
		}
		if persist != nil {
			if err := persist(tx); err != nil {
				return err
			}
		}
		if j.IdempotencyKey != "" && !reused {
			if _, err := tx.Exec(ctx, `INSERT INTO stage_executions (idempotency_key, run_id, stage, input_hash, job_id, attempt)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (idempotency_key) DO UPDATE SET job_id = EXCLUDED.job_id, attempt = EXCLUDED.attempt, created_at = now()`, j.IdempotencyKey, j.RunID, j.Stage, j.InputHash, j.ID, j.Attempt); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, finished_at = now(), updated_at = now(), progress_done = progress_total,
			lease_owner = NULL, lease_expires_at = NULL WHERE id = $2`, Completed, j.ID); err != nil {
			return err
		}
		if err := finishAttempt(ctx, tx, j, Completed, "", ""); err != nil {
			return err
		}
		stages := q.Plan.stages(j.RunKind)
		if j.Seq+1 < len(stages) {
			return insertJob(ctx, tx, j.RunID, stages[j.Seq+1], j.Seq+1, q.Plan.resource(stages[j.Seq+1]))
		}
		_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = now() WHERE id = $2`, RunReadyForReview, j.RunID)
		return err
	})
}

// IdempotencyKey names one execution of one stage on one input: run ID + stage + input hash.
func IdempotencyKey(runID int64, stage, inputHash string) string {
	return fmt.Sprintf("%d:%s:%s", runID, stage, inputHash)
}

// CompletedExecution reports whether the key has a completed execution whose artifacts are still
// stored, and which job made them. A key whose job left no artifact rows is not reusable.
func (q *Queue) CompletedExecution(ctx context.Context, key string) (jobID int64, found bool, err error) {
	err = q.DB.QueryRow(ctx, `SELECT e.job_id FROM stage_executions e
		WHERE e.idempotency_key = $1 AND EXISTS (SELECT 1 FROM artifacts a WHERE a.job_id = e.job_id)`, key).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return jobID, err == nil, err
}

// retryDelay is the wait before the next attempt after the attemptsUsed-th failure.
func (q *Queue) retryDelay(se *StageError, attemptsUsed int) time.Duration {
	if q.Delay != nil {
		return q.Delay(se, attemptsUsed)
	}
	jitter := rand.Float64()
	if q.Jitter != nil {
		jitter = q.Jitter()
	}
	return RetryDelay(se.Class, attemptsUsed, se.RetryAfter, jitter)
}

// Fail records a stage failure and applies the error class's remedy. A retryable class under the
// attempt bound goes to retry_scheduled (or waiting_for_resource for insufficient local resources)
// with run_after set by exponential backoff and jitter; at the bound, or for any class that does
// not retry (including uncertain_outcome), the job and its run fail with the sanitized reason.
func (q *Queue) Fail(ctx context.Context, j *Job, se *StageError) error {
	remedy := se.Class.Remedy()
	return pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		cur, err := lockRunning(ctx, tx, j)
		if err != nil {
			return err
		}
		used := j.Attempt - j.AttemptBase
		to := Failed
		switch {
		case remedy == CancelRun:
			to = Cancelled
		case remedy == RetryStage && used < q.MaxAttempts:
			to = RetryScheduled
		case remedy == WaitResource && used < q.MaxAttempts:
			to = WaitingForResource
		}
		if err := Transition(cur, to); err != nil {
			return err
		}
		delay := time.Duration(0)
		waitReason := ""
		if to == RetryScheduled || to == WaitingForResource {
			delay = q.retryDelay(se, used)
		}
		if to == WaitingForResource {
			waitReason = fmt.Sprintf("not enough local resources (attempt %d of %d); retrying after backoff", used, q.MaxAttempts)
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, error_class = $2, error_message = $3, updated_at = now(),
			lease_owner = NULL, lease_expires_at = NULL, wait_reason = NULLIF($6, ''),
			run_after = CASE WHEN $1 IN ('retry_scheduled', 'waiting_for_resource') THEN now() + $4::interval END,
			finished_at = CASE WHEN $1 IN ('retry_scheduled', 'waiting_for_resource') THEN NULL ELSE now() END WHERE id = $5`,
			to, se.Class, se.Message, leaseInterval(delay), j.ID, waitReason); err != nil {
			return err
		}
		if err := finishAttempt(ctx, tx, j, to, se.Class, se.Message); err != nil {
			return err
		}
		if to == RetryScheduled || to == WaitingForResource {
			return nil
		}
		runState := RunFailed
		if to == Cancelled {
			runState = RunCancelled
		}
		if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = now() WHERE id = $2`, runState, j.RunID); err != nil {
			return err
		}
		if to == Failed && remedy == RewriteScript {
			return q.autoRewrite(ctx, tx, j.RunID, se.Message)
		}
		return nil
	})
}

// autoRewrite starts the next rewrite unless the user cancelled the run or the chain has reached
// MaxAutoRewrites. A failure to find out is returned, so the whole Fail rolls back and the worker's
// next start recovers the job instead of the failure being recorded without its rewrite.
func (q *Queue) autoRewrite(ctx context.Context, tx pgx.Tx, runID int64, feedback string) error {
	var cancelled bool
	if err := tx.QueryRow(ctx, `SELECT cancel_requested FROM workflow_runs WHERE id = $1`, runID).Scan(&cancelled); err != nil {
		return err
	}
	if cancelled {
		return nil
	}
	depth, err := rewriteDepth(ctx, tx, runID)
	if err != nil {
		return err
	}
	if depth >= MaxAutoRewrites {
		return nil
	}
	_, err = q.startRewrite(ctx, tx, runID, feedback)
	return err
}

// MaxAutoRewrites bounds how many rewrites in a row the queue starts by itself because the spoken
// dialogue came out the wrong length. After that the last run stays failed for a person to decide.
const MaxAutoRewrites = 2

// rewriteDepth counts the consecutive rewrite runs that end at runID, following parents while the
// run is itself a rewrite. It is derived from the chain, so it cannot drift from the runs.
func rewriteDepth(ctx context.Context, tx pgx.Tx, runID int64) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `WITH RECURSIVE chain AS (
			SELECT id, parent_run_id, kind FROM workflow_runs WHERE id = $1
			UNION ALL
			SELECT r.id, r.parent_run_id, r.kind FROM workflow_runs r JOIN chain c ON r.id = c.parent_run_id WHERE c.kind = 'rewrite')
		SELECT count(*) FROM chain WHERE kind = 'rewrite'`, runID).Scan(&n)
	return n, err
}

// startRewrite queues one new run for the same episode whose scriptwriter sees feedback.
// It runs in the same transaction that failed the old run, so a crash cannot leave one without the other.
func (q *Queue) startRewrite(ctx context.Context, tx pgx.Tx, runID int64, feedback string) (int64, error) {
	var newID int64
	if err := tx.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, parent_run_id, state, feedback)
		SELECT episode_id, profile, 'rewrite', id, $2, $3 FROM workflow_runs WHERE id = $1 RETURNING id`,
		runID, RunRunning, feedback).Scan(&newID); err != nil {
		return 0, err
	}
	first := q.Plan.stages("rewrite")[0]
	return newID, insertJob(ctx, tx, newID, first, 0, q.Plan.resource(first))
}

// RecoverExpired takes back every running job whose lease has expired, because its worker died or
// stalled. Each goes back to queued with its attempt recorded, or to failed (and its run with it)
// once the attempt limit is used up. A run whose cancel was requested is cancelled instead. The
// stage's work dir is discarded by the stage itself on rerun. Any worker may call this; the row
// locks make concurrent callers split the work. It returns how many jobs it recovered.
func (q *Queue) RecoverExpired(ctx context.Context) (int, error) {
	n := 0
	for {
		found, err := q.recoverOneExpired(ctx)
		if err != nil {
			return n, err
		}
		if !found {
			return n, nil
		}
		n++
	}
}

func (q *Queue) recoverOneExpired(ctx context.Context) (bool, error) {
	found := false
	err := pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		j := Job{State: Running}
		var cancelRequested bool
		err := tx.QueryRow(ctx, `SELECT j.id, j.run_id, j.attempt_count, j.attempt_base, r.cancel_requested FROM jobs j
			JOIN workflow_runs r ON r.id = j.run_id
			WHERE j.state = $1 AND j.lease_expires_at < now() ORDER BY j.id LIMIT 1 FOR UPDATE OF j SKIP LOCKED`, Running).
			Scan(&j.ID, &j.RunID, &j.Attempt, &j.AttemptBase, &cancelRequested)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		class, msg := ErrWorkerRestart, "worker lost its lease while this stage was running"
		to := Queued
		switch {
		case cancelRequested:
			to, class, msg = Cancelled, ErrCancelled, "cancelled by user"
		case j.Attempt-j.AttemptBase >= q.MaxAttempts:
			to, msg = Failed, msg+"; attempt limit reached"
		}
		if err := Transition(j.State, to); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, error_class = $2, error_message = $3, updated_at = now(),
			lease_owner = NULL, lease_expires_at = NULL,
			finished_at = CASE WHEN $1 = 'queued' THEN NULL ELSE now() END WHERE id = $4`, to, class, msg, j.ID); err != nil {
			return err
		}
		if err := finishAttempt(ctx, tx, &j, to, class, msg); err != nil {
			return err
		}
		switch to {
		case Failed:
			_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = now() WHERE id = $2`, RunFailed, j.RunID)
		case Cancelled:
			_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = now() WHERE id = $2`, RunCancelled, j.RunID)
		}
		return err
	})
	return found, err
}

// RequestCancel marks a run for cancellation. Jobs not yet running are cancelled at once;
// a running job sees the flag through CancelRequested and stops itself.
func (q *Queue) RequestCancel(ctx context.Context, runID int64) error {
	return q.RequestCancelTx(ctx, runID, nil)
}

// RequestCancelTx is RequestCancel with a hook that runs in the same transaction after the queue
// change (the API uses it to write the audit row). If the hook fails, the cancel rolls back too.
func (q *Queue) RequestCancelTx(ctx context.Context, runID int64, hook func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		var state RunState
		if err := tx.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id = $1 FOR UPDATE`, runID).Scan(&state); err != nil {
			return err
		}
		if state != RunRunning {
			return conflictf("run is %s, not running", state)
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET cancel_requested = true WHERE id = $1`, runID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, error_class = $2, error_message = 'cancelled by user', updated_at = now(), finished_at = now()
			WHERE run_id = $3 AND state IN ($4, $5, $6)`, Cancelled, ErrCancelled, runID, Queued, RetryScheduled, WaitingForResource)
		if err != nil {
			return err
		}
		var running int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE run_id = $1 AND state = $2`, runID, Running).Scan(&running); err != nil {
			return err
		}
		if tag.RowsAffected() > 0 && running == 0 {
			if _, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = now() WHERE id = $2`, RunCancelled, runID); err != nil {
				return err
			}
		}
		if hook != nil {
			return hook(tx)
		}
		return nil
	})
}

func (q *Queue) CancelRequested(ctx context.Context, runID int64) (bool, error) {
	var c bool
	err := q.DB.QueryRow(ctx, `SELECT cancel_requested FROM workflow_runs WHERE id = $1`, runID).Scan(&c)
	return c, err
}

func (q *Queue) SetProgress(ctx context.Context, jobID int64, done, total int) error {
	_, err := q.DB.Exec(ctx, `UPDATE jobs SET progress_done = $1, progress_total = $2, updated_at = now() WHERE id = $3`, done, total, jobID)
	return err
}

func (q *Queue) SetProvider(ctx context.Context, jobID int64, provider, model string) error {
	_, err := q.DB.Exec(ctx, `UPDATE jobs SET provider = $1, model = $2, updated_at = now() WHERE id = $3`, provider, model, jobID)
	return err
}

// Resume is the manual retry from the UI, the only way out of the failed state. It acts on the
// failed job's error class: a stage that can simply run again goes back in the queue and keeps the
// run (the returned id is runID); a script of the wrong length starts a rewrite run instead, because
// running the voice stage again on the same script would fail the same way (the returned id is the
// new run). A person chose this, so it is not limited by MaxAutoRewrites.
func (q *Queue) Resume(ctx context.Context, runID int64) (int64, error) {
	return q.ResumeTx(ctx, runID, nil)
}

// ResumeTx is Resume with a hook that runs in the same transaction after the queue change, given
// the id of the run that continues the work. If the hook fails, the retry rolls back too.
func (q *Queue) ResumeTx(ctx context.Context, runID int64, hook func(tx pgx.Tx, resumedRunID int64) error) (int64, error) {
	resumed := runID
	err := pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		var state RunState
		if err := tx.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id = $1 FOR UPDATE`, runID).Scan(&state); err != nil {
			return err
		}
		if state != RunFailed {
			return conflictf("run is %s, only failed runs can be retried", state)
		}
		var jobID int64
		var cur State
		var class, msg *string
		if err := tx.QueryRow(ctx, `SELECT id, state, error_class, error_message FROM jobs WHERE run_id = $1 AND state = $2 ORDER BY seq DESC LIMIT 1 FOR UPDATE`, runID, Failed).Scan(&jobID, &cur, &class, &msg); err != nil {
			return err
		}
		if class != nil && ErrorClass(*class).Remedy() == RewriteScript {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workflow_runs WHERE parent_run_id = $1 AND kind = 'rewrite')`, runID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return conflictf("run %d was already rewritten", runID)
			}
			var err error
			resumed, err = q.startRewrite(ctx, tx, runID, *msg)
			if err != nil {
				return err
			}
			return runResumeHook(tx, hook, resumed)
		}
		if err := Transition(cur, Queued); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, attempt_base = attempt_count, run_after = NULL, finished_at = NULL, updated_at = now() WHERE id = $2`, Queued, jobID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = NULL, cancel_requested = false WHERE id = $2`, RunRunning, runID); err != nil {
			return err
		}
		return runResumeHook(tx, hook, resumed)
	})
	return resumed, err
}

func runResumeHook(tx pgx.Tx, hook func(pgx.Tx, int64) error, resumed int64) error {
	if hook == nil {
		return nil
	}
	return hook(tx, resumed)
}
