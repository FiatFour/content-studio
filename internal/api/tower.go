package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/tower"
)

const maxLogLine = 160

var (
	// secretish matches things that look like credentials: bearer tokens, key=value pairs with a
	// secret-sounding name, user:pass@ URLs, vendor key prefixes and long unbroken token strings.
	secretKV    = regexp.MustCompile(`(?i)\b(authorization|bearer|api[_-]?key|apikey|token|secret|password|passwd|webhook|cookie|session)\b(["']?\s*[:=]?\s*)(\S+)`)
	secretURL   = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*://)[^\s/@]*:[^\s/@]*@`)
	secretToken = regexp.MustCompile(`\b(?:sk|pk|ghp|gho|xox[a-z])[-_][A-Za-z0-9_\-]{8,}|\b[A-Za-z0-9_\-]{32,}\b`)
	spaces      = regexp.MustCompile(`\s+`)
)

// sanitizeLine makes stored stage text safe for the tower: absolute paths shortened, anything that
// looks like a credential redacted, whitespace collapsed to one line and the length capped.
func sanitizeLine(s string) string {
	p := scrubPaths(&s)
	out := secretURL.ReplaceAllString(*p, "${1}[redacted]@")
	out = secretKV.ReplaceAllString(out, "$1$2[redacted]")
	out = secretToken.ReplaceAllString(out, "[redacted]")
	out = strings.TrimSpace(spaces.ReplaceAllString(out, " "))
	if r := []rune(out); len(r) > maxLogLine {
		out = string(r[:maxLogLine-1]) + "…"
	}
	return out
}

// getTower answers GET /api/tower. Read-only; every value comes from jobs, workflow_runs, artifacts,
// approvals and exports rows. See internal/tower for the mapping and docs/spec/ui.md for its table.
func (s *Server) getTower(c echo.Context) error {
	ctx := c.Request().Context()
	in, err := s.loadTowerInput(ctx)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"generated_at": time.Now().UTC(), "tower": tower.Derive(in)})
}

func (s *Server) loadTowerInput(ctx context.Context) (tower.Input, error) {
	var in tower.Input
	if err := s.DB.QueryRow(ctx, `SELECT date_trunc('day', now())`).Scan(&in.TodayStart); err != nil {
		return in, err
	}

	rows, err := s.DB.Query(ctx, `SELECT a.id, a.episode_id, e.number, e.title, a.version, a.sha256, a.created_at FROM (
			SELECT DISTINCT ON (episode_id) id, episode_id, version, sha256, created_at, validation_status FROM artifacts
			WHERE type = $1 ORDER BY episode_id, id DESC) a
		JOIN episodes e ON e.id = a.episode_id
		WHERE a.validation_status = 'qa_passed' AND NOT EXISTS (SELECT 1 FROM approvals ap WHERE ap.artifact_id = a.id)`, artifacts.Video)
	if err != nil {
		return in, err
	}
	if in.Awaiting, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (tower.Clip, error) {
		var c tower.Clip
		err := r.Scan(&c.ArtifactID, &c.EpisodeID, &c.Number, &c.Title, &c.Version, &c.SHA256, &c.CreatedAt)
		return c, err
	}); err != nil {
		return in, err
	}

	rows, err = s.DB.Query(ctx, `SELECT r.id, r.episode_id, e.number, e.title, r.state, r.created_at, r.kind FROM (
			SELECT DISTINCT ON (episode_id) id, episode_id, state, created_at, kind FROM workflow_runs ORDER BY episode_id, id DESC) r
		JOIN episodes e ON e.id = r.episode_id
		WHERE r.state IN ('running', 'failed', 'ready_for_review') OR r.created_at >= $1
			OR EXISTS (SELECT 1 FROM jobs j WHERE j.run_id = r.id AND j.updated_at >= $1)`, in.TodayStart)
	if err != nil {
		return in, err
	}
	if in.Runs, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (tower.Run, error) {
		var t tower.Run
		var state, kind string
		err := r.Scan(&t.ID, &t.EpisodeID, &t.Number, &t.Title, &state, &t.CreatedAt, &kind)
		t.State = jobs.RunState(state)
		// Same choice as Plan.stages: a rerender has its own plan, every other kind runs the full one.
		t.Stages = s.Queue.Plan.Full
		if kind == "rerender" {
			t.Stages = s.Queue.Plan.Rerender
		}
		return t, err
	}); err != nil {
		return in, err
	}
	byRun := map[int64]int{}
	ids := make([]int64, len(in.Runs))
	for i, r := range in.Runs {
		byRun[r.ID], ids[i] = i, r.ID
	}
	jr, err := s.DB.Query(ctx, `SELECT run_id, stage, seq, state, progress_done, progress_total, provider, model,
			error_message, wait_reason, updated_at FROM jobs WHERE run_id = ANY($1) ORDER BY run_id, seq`, ids)
	if err != nil {
		return in, err
	}
	defer jr.Close()
	for jr.Next() {
		var runID int64
		var j tower.Job
		var state string
		var provider, model, errMsg, wait *string
		if err := jr.Scan(&runID, &j.Stage, &j.Seq, &state, &j.ProgressDone, &j.ProgressTotal, &provider, &model, &errMsg, &wait, &j.UpdatedAt); err != nil {
			return in, err
		}
		j.State = jobs.State(state)
		if provider != nil {
			j.Provider = sanitizeLine(*provider)
		}
		if model != nil {
			j.Model = sanitizeLine(*model)
		}
		switch {
		case j.State == jobs.Failed && errMsg != nil:
			j.LastLog = sanitizeLine(*errMsg)
		case wait != nil && (j.State == jobs.WaitingForResource):
			j.LastLog = sanitizeLine(*wait)
		}
		i := byRun[runID]
		in.Runs[i].Jobs = append(in.Runs[i].Jobs, j)
	}
	if err := jr.Err(); err != nil {
		return in, err
	}

	in.Completed = map[string]int{}
	cr, err := s.DB.Query(ctx, `SELECT stage, count(*) FROM jobs WHERE state = 'completed' GROUP BY stage`)
	if err != nil {
		return in, err
	}
	defer cr.Close()
	for cr.Next() {
		var stage string
		var n int
		if err := cr.Scan(&stage, &n); err != nil {
			return in, err
		}
		in.Completed[stage] = n
	}
	if err := cr.Err(); err != nil {
		return in, err
	}
	err = s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM approvals), (SELECT count(*) FROM exports),
			EXISTS (SELECT 1 FROM exports WHERE created_at >= $1)`, in.TodayStart).Scan(&in.Approvals, &in.Exports, &in.ExportedToday)
	return in, err
}
