// Package api is the HTTP API the web UI uses. It binds to localhost only and has no authentication in M1a.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/audit"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/notify"
	"github.com/fiat/content-studio/internal/review"
)

const owner = audit.Owner

type Server struct {
	DB          *pgxpool.Pool
	Queue       *jobs.Queue
	Store       *artifacts.Store
	Review      *review.Service
	StorageRoot string
	WebDir      string
	Log         *slog.Logger
	Notify      *notify.Service // nil disables the /api/notifications routes
}

func (s *Server) Echo() *echo.Echo {
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.HTTPErrorHandler = s.errorHandler
	e.Use(s.logRequests)

	a := e.Group("/api")
	a.GET("/health", s.health)
	a.GET("/system", s.system)
	a.GET("/settings", s.getSettings)
	a.GET("/episodes", s.listEpisodes)
	a.GET("/episodes/:id", s.getEpisode)
	a.GET("/runs/:id/artifacts", s.runArtifacts)
	a.GET("/artifacts/:id/content", s.artifactContent)
	a.GET("/videos/:id/review", s.videoReview)
	a.GET("/characters", s.listCharacters)
	a.GET("/audit", s.listAudit)

	a.PUT("/settings", s.putSettings)
	a.POST("/episodes", s.createEpisode)
	a.POST("/characters", s.createCharacter)
	a.PUT("/characters/:id", s.updateCharacter)
	a.PUT("/episodes/:id/summary", s.putSummary)
	a.PUT("/episodes/:id/in-story-date", s.putInStoryDate)
	a.POST("/episodes/:id/rerender", s.rerender)
	a.POST("/runs/:id/cancel", s.cancelRun)
	a.POST("/runs/:id/retry", s.retryRun)
	a.POST("/videos/:id/decision", s.decide)
	a.POST("/videos/:id/export", s.export)

	if s.Notify != nil {
		a.GET("/notifications", s.getNotifications)
		a.PUT("/notifications/events/:event", s.putNotificationEvent)
		a.POST("/notifications/test", s.testNotification)
	}

	if s.WebDir != "" {
		e.GET("/*", s.spa)
	}
	return e
}

// apiError is an error whose message is safe to send to the client.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func fail(status int, msg string) error { return &apiError{status, msg} }

// errorHandler never sends stack traces, paths or internal error text to the client.
func (s *Server) errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	status, msg := http.StatusInternalServerError, "internal error"
	var ae *apiError
	var he *echo.HTTPError
	var refused *review.ErrRefused
	var conflict *jobs.ConflictError
	switch {
	case errors.As(err, &conflict):
		status, msg = http.StatusConflict, conflict.Msg
	case errors.As(err, &ae):
		status, msg = ae.status, ae.msg
	case errors.As(err, &refused):
		status, msg = http.StatusConflict, refused.Reason
	case errors.Is(err, pgx.ErrNoRows):
		status, msg = http.StatusNotFound, "not found"
	case errors.As(err, &he):
		status = he.Code
		msg = http.StatusText(he.Code)
	default:
		s.Log.Error("request failed", "method", c.Request().Method, "path", c.Path(), "err", err)
	}
	if err := c.JSON(status, map[string]string{"error": msg}); err != nil {
		s.Log.Error("write error response", "err", err)
	}
}

func (s *Server) logRequests(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		err := next(c)
		if c.Request().Method != http.MethodGet {
			s.Log.Info("request", "method", c.Request().Method, "path", c.Path(), "status", c.Response().Status)
		}
		return err
	}
}

func idParam(c echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fail(http.StatusBadRequest, "invalid id")
	}
	return id, nil
}

func bind(c echo.Context, v any) error {
	if err := c.Bind(v); err != nil {
		return fail(http.StatusBadRequest, "invalid request body")
	}
	return nil
}

func (s *Server) health(c echo.Context) error {
	if err := s.DB.Ping(c.Request().Context()); err != nil {
		return fail(http.StatusServiceUnavailable, "database unreachable")
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getSettings(c echo.Context) error {
	lang := "th"
	err := s.DB.QueryRow(c.Request().Context(), `SELECT ui_lang FROM user_settings WHERE user_id = $1`, owner).Scan(&lang)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"ui_lang": lang, "user": owner})
}

func (s *Server) putSettings(c echo.Context) error {
	var req struct {
		UILang string `json:"ui_lang"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	if req.UILang != "th" && req.UILang != "en" {
		return fail(http.StatusBadRequest, "ui_lang must be th or en")
	}
	ctx := c.Request().Context()
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO user_settings (user_id, ui_lang) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET ui_lang = EXCLUDED.ui_lang`, owner, req.UILang); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: owner, Action: "settings.update", TargetType: "settings", Detail: map[string]any{"ui_lang": req.UILang}})
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"ui_lang": req.UILang})
}

func (s *Server) cancelRun(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	err = s.Queue.RequestCancelTx(ctx, id, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Entry{Actor: owner, Action: "job.cancel", TargetType: "run", TargetID: id})
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) retryRun(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	runID, err := s.Queue.ResumeTx(ctx, id, func(tx pgx.Tx, resumed int64) error {
		return audit.Record(ctx, tx, audit.Entry{Actor: owner, Action: "job.retry", TargetType: "run", TargetID: id, Detail: map[string]any{"resumed_run_id": resumed}})
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "run_id": runID})
}

// rerender starts a render+QA run from the latest full or rewrite run whose subtitles stage completed.
func (s *Server) rerender(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var src int64
	err = s.DB.QueryRow(ctx, `SELECT r.id FROM workflow_runs r JOIN jobs j ON j.run_id = r.id
		WHERE r.episode_id = $1 AND r.kind IN ('full', 'rewrite') AND j.stage = 'subtitles' AND j.state = 'completed' ORDER BY r.id DESC LIMIT 1`, id).Scan(&src)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(http.StatusConflict, "no finished script, images and audio to re-render from")
	}
	if err != nil {
		return err
	}
	if busy, err := s.runActive(ctx, id); err != nil {
		return err
	} else if busy {
		return fail(http.StatusConflict, "this episode already has a run in progress")
	}
	runID, err := s.Queue.CreateRun(ctx, id, "rerender", &src)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]int64{"run_id": runID})
}

func (s *Server) runActive(ctx context.Context, episodeID int64) (bool, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM workflow_runs WHERE episode_id = $1 AND state = 'running'`, episodeID).Scan(&n)
	return n > 0, err
}

func (s *Server) artifactContent(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var rel, typ string
	if err := s.DB.QueryRow(c.Request().Context(), `SELECT storage_path, type FROM artifacts WHERE id = $1`, id).Scan(&rel, &typ); err != nil {
		return err
	}
	path, err := s.Store.Abs(rel)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fail(http.StatusNotFound, "artifact file is missing")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	ct := map[string]string{"json": "application/json", "srt": "text/plain; charset=utf-8", "png": "image/png", "wav": "audio/wav", "mp4": "video/mp4"}[strings.TrimPrefix(filepath.Ext(rel), ".")]
	c.Response().Header().Set("Content-Type", ct)
	c.Response().Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(c.Response(), c.Request(), filepath.Base(rel), st.ModTime(), f)
	return nil
}

func (s *Server) spa(c echo.Context) error {
	p := filepath.Clean("/" + c.Param("*"))
	full := filepath.Join(s.WebDir, p)
	if st, err := os.Stat(full); err == nil && !st.IsDir() {
		return c.File(full)
	}
	return c.File(filepath.Join(s.WebDir, "index.html"))
}

// listAudit is read-only, newest first. before is the id of the last row of the previous page.
func (s *Server) listAudit(c echo.Context) error {
	limit, before := 50, int64(0)
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return fail(http.StatusBadRequest, "limit must be 1 to 200")
		}
		limit = n
	}
	if v := c.QueryParam("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return fail(http.StatusBadRequest, "before must be a row id")
		}
		before = n
	}
	rows, err := audit.List(c.Request().Context(), s.DB, before, limit)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"entries": rows})
}
