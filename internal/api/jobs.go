package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/jobs"
)

// jobInspection is the read-only detail of one job for the episode page. It adds the lease, the retry
// schedule, the recorded execution and a computed "can it safely resume" to the list fields. The lease
// owner is left out: it names a worker process, not something the owner acts on.
type jobInspection struct {
	jobDetail
	RunID          int64                 `json:"run_id"`
	MaxAttempts    int                   `json:"max_attempts"`
	RunAfter       *time.Time            `json:"run_after"`
	LeaseExpiresAt *time.Time            `json:"lease_expires_at"`
	HeartbeatAt    *time.Time            `json:"heartbeat_at"`
	LeaseExpired   *bool                 `json:"lease_expired"` // nil unless the job is running with a lease
	Execution      *execInfo             `json:"execution"`     // nil when no completed execution is recorded
	Resume         jobs.ResumeAssessment `json:"resume"`
}

type execInfo struct {
	InputHash string    `json:"input_hash"` // first 12 hex characters
	Attempt   int       `json:"attempt"`
	At        time.Time `json:"at"`
}

// getJob answers GET /api/jobs/:id. Everything comes from jobs and stage_executions rows.
func (s *Server) getJob(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var j jobInspection
	var seq int
	var expired *bool
	if err := s.DB.QueryRow(ctx, `SELECT id, run_id, seq, stage, state, attempt_count, progress_done, progress_total, provider, model,
			error_class, error_message, wait_reason, started_at, finished_at, updated_at, run_after, lease_expires_at, heartbeat_at,
			CASE WHEN state = 'running' AND lease_expires_at IS NOT NULL THEN lease_expires_at < now() END
		FROM jobs WHERE id = $1`, id).
		Scan(&j.ID, &j.RunID, &seq, &j.Stage, &j.State, &j.Attempt, &j.ProgressDone, &j.ProgressTotal, &j.Provider, &j.Model,
			&j.ErrorClass, &j.ErrorMessage, &j.WaitReason, &j.StartedAt, &j.FinishedAt, &j.UpdatedAt, &j.RunAfter, &j.LeaseExpiresAt, &j.HeartbeatAt, &expired); err != nil {
		return err
	}
	j.ErrorMessage = scrubPaths(j.ErrorMessage)
	j.LeaseExpired = expired
	j.MaxAttempts = s.Queue.MaxAttempts

	var hash string
	var ex execInfo
	err = s.DB.QueryRow(ctx, `SELECT input_hash, attempt, created_at FROM stage_executions WHERE run_id = $1 AND stage = $2
		ORDER BY created_at DESC LIMIT 1`, j.RunID, j.Stage).Scan(&hash, &ex.Attempt, &ex.At)
	switch {
	case err == nil:
		ex.InputHash = hash[:min(12, len(hash))]
		j.Execution = &ex
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return err
	}

	in := jobs.ResumeInput{State: jobs.State(j.State), ErrorClass: j.ErrorClass}
	if in.State == jobs.Failed {
		if err := s.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE jb.state = 'completed'),
				count(*) FILTER (WHERE jb.state = 'completed' AND EXISTS (SELECT 1 FROM stage_executions se WHERE se.run_id = jb.run_id AND se.stage = jb.stage)),
				EXISTS (SELECT 1 FROM workflow_runs WHERE parent_run_id = $1 AND kind = 'rewrite')
			FROM jobs jb WHERE jb.run_id = $1 AND jb.seq < $2`, j.RunID, seq).
			Scan(&in.UpstreamTotal, &in.UpstreamCompleted, &in.UpstreamRecorded, &in.AlreadyRewritten); err != nil {
			return err
		}
	}
	j.Resume = jobs.AssessResume(in)
	return c.JSON(http.StatusOK, j)
}
