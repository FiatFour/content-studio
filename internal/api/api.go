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
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/review"
)

const owner = "owner"

type Server struct {
	DB          *pgxpool.Pool
	Queue       *jobs.Queue
	Store       *artifacts.Store
	Review      *review.Service
	StorageRoot string
	WebDir      string
	Log         *slog.Logger
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

	a.PUT("/settings", s.putSettings)
	a.POST("/episodes", s.createEpisode)
	a.POST("/episodes/:id/rerender", s.rerender)
	a.POST("/runs/:id/cancel", s.cancelRun)
	a.POST("/runs/:id/retry", s.retryRun)
	a.POST("/videos/:id/decision", s.decide)
	a.POST("/videos/:id/export", s.export)

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
	_, err := s.DB.Exec(c.Request().Context(), `INSERT INTO user_settings (user_id, ui_lang) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET ui_lang = EXCLUDED.ui_lang`, owner, req.UILang)
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
	if err := s.Queue.RequestCancel(c.Request().Context(), id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) retryRun(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	runID, err := s.Queue.Resume(c.Request().Context(), id)
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
