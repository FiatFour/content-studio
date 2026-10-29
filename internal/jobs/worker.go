package jobs

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

// Stage does one stage's work for a claimed job. It writes its files, then returns persist,
// which records the stage's artifact rows inside the transaction that completes the job.
type Stage interface {
	Run(ctx context.Context, j *Job, progress func(done, total int)) (persist func(pgx.Tx) error, err error)
}

// Keyed is implemented by a stage that can say what its inputs are. InputHash must change whenever
// anything the stage reads changes (upstream artifacts, episode text, settings, model) and stay
// the same otherwise. The worker turns it into the idempotency key run ID + stage + input hash.
type Keyed interface {
	InputHash(ctx context.Context, j *Job) (string, error)
}

type Worker struct {
	Queue        *Queue
	Stages       map[string]Stage
	Log          *slog.Logger
	PollInterval time.Duration
}

// Run processes jobs one at a time until ctx ends. One worker means one GPU-heavy stage at a time.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		ran, err := w.RunOne(ctx)
		if err != nil && ctx.Err() == nil {
			w.Log.Error("worker step failed", "err", err)
		}
		if !ran {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(w.PollInterval):
			}
		}
	}
}

// RunOne claims and runs a single job. It reports whether a job was found.
func (w *Worker) RunOne(ctx context.Context) (bool, error) {
	n, err := w.Queue.RecoverExpired(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		w.Log.Warn("recovered jobs with expired leases", "count", n)
	}
	j, err := w.Queue.Claim(ctx)
	if errors.Is(err, ErrNoJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	log := w.Log.With("job_id", j.ID, "run_id", j.RunID, "stage", j.Stage, "attempt", j.Attempt)
	log.Info("job state", "state", Running)
	stage, ok := w.Stages[j.Stage]
	if !ok {
		return true, w.Queue.Fail(ctx, j, Errorf(ErrUnsupported, nil, "no implementation for stage %s", j.Stage))
	}

	if keyed, ok := stage.(Keyed); ok {
		done, err := w.reuseIfCompleted(ctx, keyed, j, log)
		if err != nil || done {
			return true, err
		}
	}

	stageCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.watchCancel(stageCtx, cancel, j.RunID)
	var leaseLost atomic.Bool
	go w.heartbeat(stageCtx, cancel, j, &leaseLost, log)

	start := time.Now()
	persist, err := stage.Run(stageCtx, j, func(done, total int) {
		if perr := w.Queue.SetProgress(ctx, j.ID, done, total); perr != nil {
			log.Warn("progress update failed", "err", perr)
		}
	})
	if ctx.Err() != nil {
		// The worker itself is shutting down: leave the job running; its lease expires and another worker recovers it.
		return true, ctx.Err()
	}
	if leaseLost.Load() {
		log.Warn("job lease lost; stopped without recording a result")
		return true, nil
	}
	if err == nil {
		if err := w.Queue.Complete(ctx, j, persist); err != nil {
			return true, w.leaseErr(err, log)
		}
		log.Info("job state", "state", Completed, "duration_s", time.Since(start).Seconds())
		return true, nil
	}
	se := classify(err)
	if c, _ := w.Queue.CancelRequested(ctx, j.RunID); c {
		se = &StageError{Class: ErrCancelled, Message: "cancelled by user"}
	}
	log.Warn("job failed", "error_class", se.Class, "err", err, "duration_s", time.Since(start).Seconds())
	return true, w.leaseErr(w.Queue.Fail(ctx, j, se), log)
}

// reuseIfCompleted sets the job's idempotency key and, when that key already completed, completes
// the job from the earlier execution's artifacts without running the stage. A stage whose input
// cannot be hashed runs without a key; its own validation then reports what is wrong.
func (w *Worker) reuseIfCompleted(ctx context.Context, k Keyed, j *Job, log *slog.Logger) (bool, error) {
	hash, err := k.InputHash(ctx, j)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		log.Warn("stage input could not be hashed; running without an idempotency key", "err", err)
		return false, nil
	}
	j.InputHash, j.IdempotencyKey = hash, IdempotencyKey(j.RunID, j.Stage, hash)
	orig, found, err := w.Queue.CompletedExecution(ctx, j.IdempotencyKey)
	if err != nil || !found {
		return false, err
	}
	if err := w.leaseErr(w.Queue.CompleteReused(ctx, j), log); err != nil {
		return false, err
	}
	log.Info("stage skipped: same input already completed", "idempotency_key", j.IdempotencyKey, "artifacts_of_job", orig)
	return true, nil
}

// leaseErr turns ErrLeaseLost into a logged non-error: another worker owns the job now and its
// result is the one that counts.
func (w *Worker) leaseErr(err error, log *slog.Logger) error {
	if errors.Is(err, ErrLeaseLost) {
		log.Warn("job lease lost; result discarded")
		return nil
	}
	return err
}

// heartbeat renews the lease every third of its length. If the lease is gone, or could not be
// renewed for a whole lease length, it stops the stage: another worker may be running the job.
func (w *Worker) heartbeat(ctx context.Context, cancel context.CancelFunc, j *Job, lost *atomic.Bool, log *slog.Logger) {
	ttl := w.Queue.leaseTTL()
	t := time.NewTicker(ttl / 3)
	defer t.Stop()
	lastOK := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hbCtx, stop := context.WithTimeout(ctx, ttl/3)
			ok, err := w.Queue.Heartbeat(hbCtx, j)
			stop()
			switch {
			case err == nil && ok:
				lastOK = time.Now()
				continue
			case err != nil:
				log.Warn("heartbeat failed", "err", err)
				if time.Since(lastOK) < ttl {
					continue
				}
			}
			lost.Store(true)
			cancel()
			return
		}
	}
}

// classify turns a stage error into a StageError. A write that failed because the disk or the
// user's quota is full (ENOSPC, EDQUOT, found with errors.Is through any wrapping, never by text)
// is insufficient_local_resources unless the stage already chose a class other than internal, so the
// job waits and retries instead of failing. The stored message is fixed: the wrapped error keeps
// the path and errno text for the log only. Read-only (EROFS) and permission (EACCES, EPERM)
// failures are not classified this way: waiting does not fix them, a person must, so they stay
// internal and fail the run.
func classify(err error) *StageError {
	var se *StageError
	if errors.As(err, &se) {
		if se.Class == ErrInternal && diskFull(err) {
			return &StageError{Class: ErrInsufficientLocal, Message: diskFullMessage, Err: err}
		}
		return se
	}
	if diskFull(err) {
		return &StageError{Class: ErrInsufficientLocal, Message: diskFullMessage, Err: err}
	}
	return &StageError{Class: ErrInternal, Message: "internal error in stage", Err: err}
}

func (w *Worker) watchCancel(ctx context.Context, cancel context.CancelFunc, runID int64) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c, err := w.Queue.CancelRequested(ctx, runID); err == nil && c {
				cancel()
				return
			}
		}
	}
}

const diskFullMessage = "local storage is full or over quota"

func diskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
