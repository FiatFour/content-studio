package notify

import (
	"context"
	"fmt"
)

// FailedWindowMinutes is the flood-collapse window: one "job failed" message per episode per window.
const FailedWindowMinutes = 10

// FailedJob is what the queue knows when a job reaches terminal failed. Reason must be the
// sanitized stage error message; it is cleaned again before it is stored.
type FailedJob struct {
	EpisodeID int64
	JobID     int64
	Attempt   int
	Stage     string
	Class     string
	Reason    string
}

// EnqueueJobFailed queues the "job failed" message in the caller's transaction, unless one was
// already queued for the same episode in the last 10 minutes. A suppressed failure is counted and
// added to the next message that goes out. Nothing is recorded while the event is toggled off.
// It never calls Discord. Callers run it in a savepoint and only log its error.
func EnqueueJobFailed(ctx context.Context, db DBTX, f FailedJob) error {
	info, _ := lookup(JobFailed)
	var on bool
	if err := db.QueryRow(ctx, `SELECT COALESCE((SELECT enabled FROM notification_settings WHERE event = $1), $2)`,
		string(JobFailed), info.Default).Scan(&on); err != nil {
		return fmt.Errorf("job failed notification setting: %w", err)
	}
	if !on {
		return nil
	}
	// The sentinel window start makes the first failure of an episode always go out.
	if _, err := db.Exec(ctx, `INSERT INTO job_failed_collapse (episode_id, window_start) VALUES ($1, now() - interval '1 day')
		ON CONFLICT (episode_id) DO NOTHING`, f.EpisodeID); err != nil {
		return fmt.Errorf("job failed collapse: %w", err)
	}
	var expired bool
	var suppressed int
	if err := db.QueryRow(ctx, `SELECT window_start <= now() - make_interval(mins => $2), suppressed FROM job_failed_collapse
		WHERE episode_id = $1 FOR UPDATE`, f.EpisodeID, FailedWindowMinutes).Scan(&expired, &suppressed); err != nil {
		return fmt.Errorf("job failed collapse: %w", err)
	}
	if !expired {
		if _, err := db.Exec(ctx, `UPDATE job_failed_collapse SET suppressed = suppressed + 1 WHERE episode_id = $1`, f.EpisodeID); err != nil {
			return fmt.Errorf("job failed collapse: %w", err)
		}
		return nil
	}
	if _, err := db.Exec(ctx, `UPDATE job_failed_collapse SET window_start = now(), suppressed = 0 WHERE episode_id = $1`, f.EpisodeID); err != nil {
		return fmt.Errorf("job failed collapse: %w", err)
	}
	reason := fmt.Sprintf("stage %s failed (%s): %s", clean(f.Stage, 30), clean(f.Class, 40), f.Reason)
	if suppressed > 0 {
		// First, so the 200-character cut never drops it.
		reason = fmt.Sprintf("%d more failure(s) since the last message were not reported. %s", suppressed, reason)
	}
	eventID := fmt.Sprintf("job_failed:job:%d:attempt:%d", f.JobID, f.Attempt)
	return Enqueue(ctx, db, JobFailed, eventID, f.EpisodeID, 0, "failed", reason)
}
