package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Plan is the ordered stage list for each run kind. It comes from the content profile.
type Plan struct {
	Profile  string
	Full     []string
	Rerender []string
}

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
}

type Queue struct {
	DB          *pgxpool.Pool
	Plan        Plan
	MaxAttempts int
}

var ErrNoJob = errors.New("no job ready")

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
		return insertJob(ctx, tx, runID, stages[0], 0)
	})
	return runID, err
}

func insertJob(ctx context.Context, tx pgx.Tx, runID int64, stage string, seq int) error {
	_, err := tx.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state) VALUES ($1, $2, $3, $4)`, runID, stage, seq, Queued)
	return err
}

// Claim promotes due retries, then takes the oldest queued job with FOR UPDATE SKIP LOCKED
// and moves it to running.
func (q *Queue) Claim(ctx context.Context) (*Job, error) {
	var j Job
	err := pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, updated_at = now() WHERE state = $2 AND run_after <= now()`, Queued, RetryScheduled); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT j.id, j.run_id, r.episode_id, e.project_id, r.kind, r.parent_run_id, j.stage, j.seq, j.state, j.attempt_count, j.attempt_base
			FROM jobs j JOIN workflow_runs r ON r.id = j.run_id JOIN episodes e ON e.id = r.episode_id
			WHERE j.state = $1 ORDER BY j.id LIMIT 1 FOR UPDATE OF j SKIP LOCKED`, Queued).
			Scan(&j.ID, &j.RunID, &j.EpisodeID, &j.ProjectID, &j.RunKind, &j.ParentRunID, &j.Stage, &j.Seq, &j.State, &j.Attempt, &j.AttemptBase)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoJob
		}
		if err != nil {
			return err
		}
		if err := Transition(j.State, Running); err != nil {
			return err
		}
		j.State, j.Attempt = Running, j.Attempt+1
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, attempt_count = $2, started_at = now(), updated_at = now(),
			error_class = NULL, error_message = NULL, progress_done = 0 WHERE id = $3`, j.State, j.Attempt, j.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO job_attempts (job_id, n) VALUES ($1, $2)`, j.ID, j.Attempt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// lockRunning re-reads the job under a row lock so a state change is checked against the real current state.
func lockRunning(ctx context.Context, tx pgx.Tx, jobID int64) (State, error) {
	var s State
	err := tx.QueryRow(ctx, `SELECT state FROM jobs WHERE id = $1 FOR UPDATE`, jobID).Scan(&s)
	return s, err
}

func finishAttempt(ctx context.Context, tx pgx.Tx, j *Job, outcome State, class ErrorClass, msg string) error {
	_, err := tx.Exec(ctx, `UPDATE job_attempts SET finished_at = now(), outcome = $1, error_class = NULLIF($2, ''), error_message = NULLIF($3, '')
		WHERE job_id = $4 AND n = $5`, outcome, string(class), msg, j.ID, j.Attempt)
	return err
}

// Complete marks the job completed in the same transaction as persist (the stage's artifact rows),
// then queues the next stage or marks the run ready for review after the last one.
func (q *Queue) Complete(ctx context.Context, j *Job, persist func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		cur, err := lockRunning(ctx, tx, j.ID)
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
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, finished_at = now(), updated_at = now(), progress_done = progress_total WHERE id = $2`, Completed, j.ID); err != nil {
			return err
		}
		if err := finishAttempt(ctx, tx, j, Completed, "", ""); err != nil {
			return err
		}
		stages := q.Plan.stages(j.RunKind)
		if j.Seq+1 < len(stages) {
			return insertJob(ctx, tx, j.RunID, stages[j.Seq+1], j.Seq+1)
		}
		_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = now() WHERE id = $2`, RunReadyForReview, j.RunID)
		return err
	})
}

// Fail records a stage failure. A retryable failure under the attempt limit is scheduled again;
// anything else fails the job and its run with the sanitized reason.
func (q *Queue) Fail(ctx context.Context, j *Job, se *StageError, delay time.Duration) error {
	remedy := se.Class.Remedy()
	return pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		cur, err := lockRunning(ctx, tx, j.ID)
		if err != nil {
			return err
		}
		to := Failed
		if remedy == CancelRun {
			to = Cancelled
		} else if remedy == RetryStage && j.Attempt-j.AttemptBase < q.MaxAttempts {
			to = RetryScheduled
		}
		if err := Transition(cur, to); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, error_class = $2, error_message = $3, updated_at = now(),
			run_after = CASE WHEN $1 = 'retry_scheduled' THEN now() + $4::interval END,
			finished_at = CASE WHEN $1 = 'retry_scheduled' THEN NULL ELSE now() END WHERE id = $5`,
			to, se.Class, se.Message, fmt.Sprintf("%d milliseconds", delay.Milliseconds()), j.ID); err != nil {
			return err
		}
		if err := finishAttempt(ctx, tx, j, to, se.Class, se.Message); err != nil {
			return err
		}
		if to == RetryScheduled {
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
	return newID, insertJob(ctx, tx, newID, q.Plan.stages("rewrite")[0], 0)
}

// RecoverInterrupted runs at worker start. A job left running by a crashed worker is rerun from
// scratch if it has attempts left, otherwise failed with the reason. Its stage work dir is
// discarded by the stage itself on rerun.
func (q *Queue) RecoverInterrupted(ctx context.Context) (int, error) {
	rows, err := q.DB.Query(ctx, `SELECT j.id, j.run_id, r.kind, j.stage, j.seq, j.attempt_count, j.attempt_base FROM jobs j
		JOIN workflow_runs r ON r.id = j.run_id WHERE j.state = $1`, Running)
	if err != nil {
		return 0, err
	}
	var stale []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.RunID, &j.RunKind, &j.Stage, &j.Seq, &j.Attempt, &j.AttemptBase); err != nil {
			rows.Close()
			return 0, err
		}
		stale = append(stale, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for i := range stale {
		se := &StageError{Class: ErrWorkerRestart, Message: "worker stopped while this stage was running"}
		if err := q.Fail(ctx, &stale[i], se, 0); err != nil {
			return i, err
		}
	}
	return len(stale), nil
}

// RequestCancel marks a run for cancellation. Jobs not yet running are cancelled at once;
// a running job sees the flag through CancelRequested and stops itself.
func (q *Queue) RequestCancel(ctx context.Context, runID int64) error {
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
			_, err = tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = now() WHERE id = $2`, RunCancelled, runID)
		}
		return err
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
			return err
		}
		if err := Transition(cur, Queued); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, attempt_base = attempt_count, run_after = NULL, finished_at = NULL, updated_at = now() WHERE id = $2`, Queued, jobID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE workflow_runs SET state = $1, finished_at = NULL, cancel_requested = false WHERE id = $2`, RunRunning, runID)
		return err
	})
	return resumed, err
}
