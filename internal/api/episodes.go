package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
)

type episodeSummary struct {
	ID              int64      `json:"id"`
	Number          int        `json:"number"`
	Title           string     `json:"title"`
	TargetDurationS int        `json:"target_duration_s"`
	CreatedAt       time.Time  `json:"created_at"`
	Run             *runDetail `json:"latest_run"`
}

type jobDetail struct {
	ID            int64      `json:"id"`
	Stage         string     `json:"stage"`
	State         string     `json:"state"`
	Attempt       int        `json:"attempt"`
	ProgressDone  int        `json:"progress_done"`
	ProgressTotal int        `json:"progress_total"`
	Provider      *string    `json:"provider"`
	Model         *string    `json:"model"`
	ErrorClass    *string    `json:"error_class"`
	ErrorMessage  *string    `json:"error_message"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type runDetail struct {
	ID          int64       `json:"id"`
	Kind        string      `json:"kind"`
	ParentRunID *int64      `json:"parent_run_id"`
	Feedback    *string     `json:"feedback"`
	State       string      `json:"state"`
	CreatedAt   time.Time   `json:"created_at"`
	FinishedAt  *time.Time  `json:"finished_at"`
	Stages      []string    `json:"stages"`
	Jobs        []jobDetail `json:"jobs"`
}

func (s *Server) loadRuns(ctx context.Context, episodeID int64, limit int) ([]runDetail, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, kind, parent_run_id, feedback, state, created_at, finished_at FROM workflow_runs
		WHERE episode_id = $1 ORDER BY id DESC LIMIT $2`, episodeID, limit)
	if err != nil {
		return nil, err
	}
	runs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (runDetail, error) {
		var d runDetail
		err := r.Scan(&d.ID, &d.Kind, &d.ParentRunID, &d.Feedback, &d.State, &d.CreatedAt, &d.FinishedAt)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	for i := range runs {
		runs[i].Stages = pipeline.FictionalSeries.Full
		if runs[i].Kind == "rerender" {
			runs[i].Stages = pipeline.FictionalSeries.Rerender
		}
		rows, err := s.DB.Query(ctx, `SELECT id, stage, state, attempt_count, progress_done, progress_total, provider, model, error_class, error_message,
			started_at, finished_at, updated_at FROM jobs WHERE run_id = $1 ORDER BY seq`, runs[i].ID)
		if err != nil {
			return nil, err
		}
		runs[i].Jobs, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (jobDetail, error) {
			var j jobDetail
			err := r.Scan(&j.ID, &j.Stage, &j.State, &j.Attempt, &j.ProgressDone, &j.ProgressTotal, &j.Provider, &j.Model, &j.ErrorClass, &j.ErrorMessage,
				&j.StartedAt, &j.FinishedAt, &j.UpdatedAt)
			j.ErrorMessage = scrubPaths(j.ErrorMessage)
			return j, err
		})
		if err != nil {
			return nil, err
		}
	}
	return runs, nil
}

func (s *Server) listEpisodes(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := s.DB.Query(ctx, `SELECT id, number, title, target_duration_s, created_at FROM episodes ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return err
	}
	eps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (episodeSummary, error) {
		var e episodeSummary
		err := r.Scan(&e.ID, &e.Number, &e.Title, &e.TargetDurationS, &e.CreatedAt)
		return e, err
	})
	if err != nil {
		return err
	}
	for i := range eps {
		runs, err := s.loadRuns(ctx, eps[i].ID, 1)
		if err != nil {
			return err
		}
		if len(runs) > 0 {
			eps[i].Run = &runs[0]
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"episodes": eps})
}

type videoVersion struct {
	ID               int64     `json:"id"`
	RunID            int64     `json:"run_id"`
	Version          int       `json:"version"`
	SHA256           string    `json:"sha256"`
	SizeBytes        int64     `json:"size_bytes"`
	ValidationStatus string    `json:"validation_status"`
	CreatedAt        time.Time `json:"created_at"`
	Decision         *string   `json:"decision"`
	Exported         bool      `json:"exported"`
}

func (s *Server) videoVersions(ctx context.Context, episodeID int64) ([]videoVersion, error) {
	rows, err := s.DB.Query(ctx, `SELECT a.id, a.run_id, a.version, a.sha256, a.size_bytes, a.validation_status, a.created_at,
			(SELECT decision FROM approvals WHERE artifact_id = a.id ORDER BY id DESC LIMIT 1),
			EXISTS (SELECT 1 FROM exports WHERE artifact_id = a.id)
		FROM artifacts a WHERE a.episode_id = $1 AND a.type = 'video' ORDER BY a.version DESC`, episodeID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (videoVersion, error) {
		var v videoVersion
		err := r.Scan(&v.ID, &v.RunID, &v.Version, &v.SHA256, &v.SizeBytes, &v.ValidationStatus, &v.CreatedAt, &v.Decision, &v.Exported)
		return v, err
	})
}

func (s *Server) getEpisode(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var e struct {
		episodeSummary
		Idea        string  `json:"idea"`
		InStoryDate *string `json:"in_story_date"`
	}
	var inStory *time.Time
	if err := s.DB.QueryRow(ctx, `SELECT id, number, title, idea, target_duration_s, created_at, in_story_date FROM episodes WHERE id = $1`, id).
		Scan(&e.ID, &e.Number, &e.Title, &e.Idea, &e.TargetDurationS, &e.CreatedAt, &inStory); err != nil {
		return err
	}
	e.InStoryDate = fmtDate(inStory)
	summary, err := s.loadSummary(ctx, id)
	if err != nil {
		return err
	}
	runs, err := s.loadRuns(ctx, id, 20)
	if err != nil {
		return err
	}
	videos, err := s.videoVersions(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"episode": e, "runs": runs, "videos": videos, "summary": summary})
}

func (s *Server) createEpisode(c echo.Context) error {
	var req struct {
		Title           string `json:"title"`
		Idea            string `json:"idea"`
		TargetDurationS int    `json:"target_duration_s"`
		InStoryDate     string `json:"in_story_date"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	req.Title, req.Idea = strings.TrimSpace(req.Title), strings.TrimSpace(req.Idea)
	switch {
	case req.Title == "" || utf8.RuneCountInString(req.Title) > 120:
		return fail(http.StatusBadRequest, "title must be 1 to 120 characters")
	case utf8.RuneCountInString(req.Idea) < 10 || utf8.RuneCountInString(req.Idea) > 2000:
		return fail(http.StatusBadRequest, "idea must be 10 to 2000 characters")
	case req.TargetDurationS < 60 || req.TargetDurationS > 120:
		return fail(http.StatusBadRequest, "target length must be 60 to 120 seconds")
	}
	inStory, err := parseDate("in_story_date", req.InStoryDate)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var episodeID int64
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		projectID, err := ensureProject(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, projectID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s, in_story_date)
			VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM episodes WHERE project_id = $1), $2, $3, $4, $5) RETURNING id`,
			projectID, req.Title, req.Idea, req.TargetDurationS, inStory).Scan(&episodeID)
	})
	if err != nil {
		return err
	}
	runID, err := s.Queue.CreateRun(ctx, episodeID, "full", nil)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]int64{"episode_id": episodeID, "run_id": runID})
}

type artifactRow struct {
	ID               int64          `json:"id"`
	Type             string         `json:"type"`
	ItemKey          string         `json:"item_key"`
	Version          int            `json:"version"`
	SHA256           string         `json:"sha256"`
	ValidationStatus string         `json:"validation_status"`
	Provider         string         `json:"provider"`
	Model            string         `json:"model"`
	Meta             map[string]any `json:"meta"`
}

func (s *Server) runArtifacts(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(c.Request().Context(), `SELECT id, type, item_key, version, sha256, validation_status, provider, model, meta
		FROM artifacts WHERE run_id = $1 ORDER BY type, item_key`, id)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (artifactRow, error) {
		var a artifactRow
		err := r.Scan(&a.ID, &a.Type, &a.ItemKey, &a.Version, &a.SHA256, &a.ValidationStatus, &a.Provider, &a.Model, &a.Meta)
		return a, err
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"artifacts": list})
}

// videoReview gathers everything the review screen shows for one exact version.
func (s *Server) videoReview(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var v videoVersion
	var episodeID int64
	if err := s.DB.QueryRow(ctx, `SELECT id, run_id, episode_id, version, sha256, size_bytes, validation_status, created_at
		FROM artifacts WHERE id = $1 AND type = 'video'`, id).Scan(&v.ID, &v.RunID, &episodeID, &v.Version, &v.SHA256, &v.SizeBytes, &v.ValidationStatus, &v.CreatedAt); err != nil {
		return err
	}
	type qa struct {
		Rule         string `json:"rule"`
		RuleVersion  int    `json:"rule_version"`
		Severity     string `json:"severity"`
		Passed       bool   `json:"passed"`
		Reason       string `json:"reason"`
		SuggestedFix string `json:"suggested_fix"`
	}
	rows, err := s.DB.Query(ctx, `SELECT rule, rule_version, severity, passed, reason, suggested_fix FROM qa_results WHERE artifact_id = $1 ORDER BY id`, id)
	if err != nil {
		return err
	}
	qas, err := pgx.CollectRows(rows, pgx.RowToStructByPos[qa])
	if err != nil {
		return err
	}
	type decision struct {
		ID        int64     `json:"id"`
		SHA256    string    `json:"sha256"`
		Decision  string    `json:"decision"`
		Notes     string    `json:"notes"`
		Reviewer  string    `json:"reviewer"`
		CreatedAt time.Time `json:"created_at"`
	}
	rows, err = s.DB.Query(ctx, `SELECT id, sha256, decision, notes, reviewer, created_at FROM approvals WHERE artifact_id = $1 ORDER BY id DESC`, id)
	if err != nil {
		return err
	}
	decisions, err := pgx.CollectRows(rows, pgx.RowToStructByPos[decision])
	if err != nil {
		return err
	}
	type export struct {
		ID        int64     `json:"id"`
		Path      string    `json:"file_name"`
		SHA256    string    `json:"sha256"`
		CreatedAt time.Time `json:"created_at"`
	}
	rows, err = s.DB.Query(ctx, `SELECT id, export_path, sha256, created_at FROM exports WHERE artifact_id = $1 ORDER BY id DESC`, id)
	if err != nil {
		return err
	}
	exports, err := pgx.CollectRows(rows, pgx.RowToStructByPos[export])
	if err != nil {
		return err
	}
	for i := range exports {
		exports[i].Path = baseName(exports[i].Path)
	}
	// The script and subtitles this version was rendered from, found through its render spec's lineage.
	var scriptID, subsID *int64
	err = s.DB.QueryRow(ctx, `WITH RECURSIVE up(id) AS (SELECT $1::bigint UNION SELECT p.parent_id FROM artifact_parents p JOIN up ON p.artifact_id = up.id)
		SELECT (SELECT a.id FROM artifacts a JOIN up USING (id) WHERE a.type = 'script' ORDER BY a.id DESC LIMIT 1),
		       (SELECT a.id FROM artifacts a JOIN up USING (id) WHERE a.type = 'subtitles' ORDER BY a.id DESC LIMIT 1)`, id).Scan(&scriptID, &subsID)
	if err != nil {
		return err
	}
	versions, err := s.videoVersions(ctx, episodeID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"video": v, "episode_id": episodeID, "qa": qas, "decisions": decisions, "exports": exports,
		"script_artifact_id": scriptID, "subtitles_artifact_id": subsID, "versions": versions,
	})
}

func (s *Server) decide(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var req struct {
		Decision string `json:"decision"`
		Notes    string `json:"notes"`
		SHA256   string `json:"sha256"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	a, err := s.Review.Decide(c.Request().Context(), id, req.SHA256, review.Decision(req.Decision), strings.TrimSpace(req.Notes), owner)
	if err != nil {
		return err
	}
	s.Log.Info("review decision", "artifact_id", id, "decision", a.Decision, "sha256", a.SHA256, "reviewer", a.Reviewer)
	if a.Decision == review.Approved {
		s.notifyVersion(c, id, "approved", fmt.Sprintf("version_approved:approval:%d", a.ID))
	}
	return c.JSON(http.StatusCreated, map[string]any{"id": a.ID, "decision": a.Decision, "sha256": a.SHA256})
}

func (s *Server) export(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	e, err := s.Review.Export(c.Request().Context(), id, owner)
	if err != nil {
		return err
	}
	s.Log.Info("export", "artifact_id", id, "sha256", e.SHA256, "export_id", e.ID)
	s.notifyVersion(c, id, "exported", fmt.Sprintf("version_exported:export:%d", e.ID))
	return c.JSON(http.StatusCreated, map[string]any{"id": e.ID, "file_name": baseName(e.Path), "sha256": e.SHA256})
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
