package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/audit"
	"github.com/fiat/content-studio/internal/providers"
)

// Resource limits (resource_limits table, migration 0007). The queue reads max_concurrent on every
// claim, so a change takes effect on the next claim with no restart. Raising a limit lets waiting
// jobs run on that claim; lowering it never stops a running job, it only blocks new claims.
const (
	// maxResourceLimit is the largest value the API accepts for any resource.
	maxResourceLimit = 4
	// safeResourceLimit is the largest value accepted without confirm_unsafe. On the 8 GB GPU a
	// second job running next to the first can run out of VRAM and take the machine down, so
	// anything above 1 needs an explicit confirmation in the request.
	safeResourceLimit = 1
)

type resourceJSON struct {
	Name          string `json:"name"`
	MaxConcurrent int    `json:"max_concurrent"`
	Running       int    `json:"running"`
	Waiting       int    `json:"waiting"`
	SafeMax       int    `json:"safe_max"`
	Max           int    `json:"max"`
}

func (s *Server) listResources(c echo.Context) error {
	rows, err := s.DB.Query(c.Request().Context(), `SELECT l.resource, l.max_concurrent,
		(SELECT count(*) FROM jobs j WHERE j.resource = l.resource AND j.state = 'running'),
		(SELECT count(*) FROM jobs j WHERE j.resource = l.resource AND j.state = 'waiting_for_resource')
		FROM resource_limits l ORDER BY l.resource`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []resourceJSON{}
	for rows.Next() {
		r := resourceJSON{SafeMax: safeResourceLimit, Max: maxResourceLimit}
		if err := rows.Scan(&r.Name, &r.MaxConcurrent, &r.Running, &r.Waiting); err != nil {
			return err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"resources": out})
}

func (s *Server) putResource(c echo.Context) error {
	var req struct {
		MaxConcurrent *int `json:"max_concurrent"`
		ConfirmUnsafe bool `json:"confirm_unsafe"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	if req.MaxConcurrent == nil {
		return fail(http.StatusBadRequest, "max_concurrent is required")
	}
	n := *req.MaxConcurrent
	if n < 1 || n > maxResourceLimit {
		return fail(http.StatusBadRequest, "max_concurrent must be an integer from 1 to 4")
	}
	if n > safeResourceLimit && !req.ConfirmUnsafe {
		return fail(http.StatusBadRequest, "values above 1 can exhaust GPU memory; send confirm_unsafe: true to confirm")
	}
	name := c.Param("name")
	ctx := c.Request().Context()
	var from int
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// Lock the row and read the old value; a name with no row is unknown (no limit is invented here).
		if err := tx.QueryRow(ctx, `SELECT max_concurrent FROM resource_limits WHERE resource = $1 FOR UPDATE`, name).Scan(&from); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE resource_limits SET max_concurrent = $2 WHERE resource = $1`, name, n); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: actor(c), Action: "resource_limit.update", TargetType: "resource_limit",
			Detail: map[string]any{"resource": name, "from": from, "to": n, "confirmed_unsafe": n > safeResourceLimit}})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(http.StatusNotFound, "unknown resource")
	}
	if err != nil {
		return err
	}
	s.Log.Info("resource limit changed", "resource", name, "from", from, "to", n)
	return c.JSON(http.StatusOK, map[string]any{"name": name, "max_concurrent": n})
}

// listPolicies is read-only. Every task runs under LOCAL_ONLY; nothing in the product changes that
// yet (no budget, issue #19), so there is deliberately no write route.
func (s *Server) listPolicies(c echo.Context) error {
	type taskJSON struct {
		Task       string `json:"task"`
		Capability string `json:"capability"`
		Policy     string `json:"policy"`
	}
	tasks := []taskJSON{}
	for _, t := range providers.Tasks() {
		tasks = append(tasks, taskJSON{string(t.Task), string(t.Capability), string(providers.DefaultPolicy)})
	}
	return c.JSON(http.StatusOK, map[string]any{"tasks": tasks, "cloud_enabled": s.CloudEnabled, "changeable": false})
}
