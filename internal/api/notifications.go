package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/notify"
)

// The webhook URL is a secret and never leaves the backend: these handlers expose only whether it
// is configured, its last 4 characters, toggles and recorded delivery results.

type deliveryJSON struct {
	Event      string    `json:"event"`
	OK         bool      `json:"ok"`
	HTTPStatus *int      `json:"http_status"`
	Error      string    `json:"error"`
	At         time.Time `json:"at"`
}

func (s *Server) getNotifications(c echo.Context) error {
	ctx := c.Request().Context()
	toggles, err := s.Notify.Toggles(ctx)
	if err != nil {
		return err
	}
	recent, err := s.Notify.Recent(ctx, 20)
	if err != nil {
		return err
	}
	events := make([]map[string]any, 0, len(toggles))
	for _, t := range toggles {
		events = append(events, map[string]any{"event": t.Event, "enabled": t.Enabled, "default": t.Default, "implemented": t.Implemented})
	}
	deliveries := make([]deliveryJSON, 0, len(recent))
	for _, d := range recent {
		deliveries = append(deliveries, deliveryJSON{d.Event, d.OK, d.HTTPStatus, d.Error, d.At})
	}
	resp := map[string]any{"configured": s.Notify.Configured(), "events": events, "recent_deliveries": deliveries}
	if s.Notify.Configured() {
		resp["suffix"] = s.Notify.Suffix
	}
	if len(deliveries) > 0 {
		resp["last_delivery"] = deliveries[0]
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *Server) putNotificationEvent(c echo.Context) error {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	if req.Enabled == nil {
		return fail(http.StatusBadRequest, "enabled is required")
	}
	ev := notify.Event(c.Param("event"))
	if err := s.Notify.SetEnabled(c.Request().Context(), ev, *req.Enabled); err != nil {
		if errors.Is(err, notify.ErrUnknownEvent) {
			return fail(http.StatusNotFound, "unknown notification event")
		}
		return err
	}
	s.Log.Info("notification setting changed", "event", ev, "enabled", *req.Enabled)
	return c.JSON(http.StatusOK, map[string]any{"event": ev, "enabled": *req.Enabled})
}

// testNotification posts a labelled test message and returns the real HTTP result.
func (s *Server) testNotification(c echo.Context) error {
	r, err := s.Notify.SendTest(c.Request().Context())
	if errors.Is(err, notify.ErrNotConfigured) {
		return fail(http.StatusConflict, "not configured")
	}
	if err != nil {
		return err
	}
	s.Log.Info("notification test", "ok", r.OK, "http_status", r.HTTPStatus, "error", r.Error)
	var code *int
	if r.HTTPStatus != 0 {
		code = &r.HTTPStatus
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": r.OK, "http_status": code, "error": r.Error})
}

// notifyApproval queues the "version approved / exported" message. It is best effort: a
// notification problem is logged and never fails the decision or export.
func (s *Server) notifyVersion(c echo.Context, artifactID int64, state, eventID string) {
	if s.Notify == nil {
		return
	}
	ctx := c.Request().Context()
	var episodeID int64
	var version int
	if err := s.DB.QueryRow(ctx, `SELECT episode_id, version FROM artifacts WHERE id = $1`, artifactID).Scan(&episodeID, &version); err != nil {
		s.Log.Warn("notification skipped", "err", err)
		return
	}
	if err := notify.Enqueue(ctx, s.DB, notify.VersionApproved, eventID, episodeID, version, state, ""); err != nil {
		s.Log.Warn("notification not queued", "err", err)
	}
}
