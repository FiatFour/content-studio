package api

// Canon proposal endpoints. They sit under the same default-deny session middleware, rate limit and
// Origin guard as every other /api route. Creating a proposal writes nothing to canon; only approve
// does, and only for the signed-in owner (the actor comes from the session, never from the body).

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/canon"
	"github.com/fiat/content-studio/internal/pipeline"
)

// canonErr maps the canon package's sanitized errors to HTTP statuses.
func canonErr(err error) error {
	switch {
	case errors.Is(err, canon.ErrInvalid):
		return fail(http.StatusBadRequest, err.Error())
	case errors.Is(err, canon.ErrDecided):
		return fail(http.StatusConflict, "this proposal was already decided")
	case errors.Is(err, canon.ErrConflict):
		return fail(http.StatusConflict, err.Error())
	}
	return err
}

// seriesProjectID returns the series project, or 0 when none exists yet.
func (s *Server) seriesProjectID(c echo.Context) (int64, error) {
	var id int64
	err := s.DB.QueryRow(c.Request().Context(), `SELECT id FROM projects WHERE profile = $1 ORDER BY id LIMIT 1`, pipeline.FictionalSeries.Profile).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s *Server) createCanonProposal(c echo.Context) error {
	var req struct {
		Kind      string          `json:"kind"`
		Payload   json.RawMessage `json:"payload"`
		Rationale string          `json:"rationale"`
		EpisodeID int64           `json:"episode_id"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	ctx := c.Request().Context()
	projectID, err := ensureProject(ctx, s.DB)
	if err != nil {
		return err
	}
	id, err := canon.Create(ctx, s.DB, canon.NewProposal{ProjectID: projectID, Kind: req.Kind, Payload: req.Payload,
		Rationale: req.Rationale, ProposedBy: actor(c), EpisodeID: req.EpisodeID})
	if err != nil {
		return canonErr(err)
	}
	return c.JSON(http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) listCanonProposals(c echo.Context) error {
	limit := 50
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return fail(http.StatusBadRequest, "limit must be 1 to 200")
		}
		limit = n
	}
	out := []canon.Proposal{}
	projectID, err := s.seriesProjectID(c)
	if err != nil {
		return err
	}
	if projectID != 0 {
		if out, err = canon.List(c.Request().Context(), s.DB, projectID, c.QueryParam("status"), limit); err != nil {
			return canonErr(err)
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"proposals": out})
}

func (s *Server) getCanonProposal(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	projectID, err := s.seriesProjectID(c)
	if err != nil {
		return err
	}
	p, err := canon.Get(c.Request().Context(), s.DB, projectID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

// getCanonImpact returns the advisory deterministic checks for one proposal. Read-only: it never
// approves, rejects or blocks anything.
func (s *Server) getCanonImpact(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	projectID, err := s.seriesProjectID(c)
	if err != nil {
		return err
	}
	rep, err := canon.CheckProposal(c.Request().Context(), s.DB, projectID, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, rep)
}

func (s *Server) decideCanonProposal(approve bool) echo.HandlerFunc {
	return func(c echo.Context) error {
		id, err := idParam(c)
		if err != nil {
			return err
		}
		var req struct {
			Reason string `json:"reason"`
		}
		if err := bind(c, &req); err != nil {
			return err
		}
		projectID, err := s.seriesProjectID(c)
		if err != nil {
			return err
		}
		ctx := c.Request().Context()
		if approve {
			err = canon.Approve(ctx, s.DB, projectID, id, actor(c), req.Reason)
		} else {
			err = canon.Reject(ctx, s.DB, projectID, id, actor(c), req.Reason)
		}
		if err != nil {
			return canonErr(err)
		}
		s.Log.Info("canon proposal decided", "proposal_id", id, "approved", approve)
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}
