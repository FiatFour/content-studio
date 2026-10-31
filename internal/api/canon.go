package api

// Owner-only writes. Every handler in this file changes the story bible (characters, episode
// summaries, in-story dates) and is reachable only through the owner's HTTP API. No pipeline stage,
// worker or model output calls these handlers or writes these tables (canon safety, non-negotiable 3).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/audit"
	"github.com/fiat/content-studio/internal/canon"
	"github.com/fiat/content-studio/internal/pipeline"
)

// The limits mirror the CHECK constraints in migrations/0005_canon.sql.
var (
	slugRe = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	loraRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,100}$`)
)

const (
	maxThreads      = 10
	maxThreadLength = 200
)

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// projectNames are the names given to a profile's project when it is created on first use.
var projectNames = map[string]string{
	pipeline.FictionalSeriesID: "Life Sim Sitcom",
	pipeline.KnowledgeShortsID: "Knowledge Shorts",
	pipeline.HistoryMysteryID:  "History & Mystery",
}

// ensureProject returns the single project of a profile, creating it on first use.
func ensureProject(ctx context.Context, q querier, profile string) (int64, error) {
	name, ok := projectNames[profile]
	if !ok {
		return 0, fmt.Errorf("no project name for profile %q", profile)
	}
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM projects WHERE profile = $1 ORDER BY id LIMIT 1`, profile).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = q.QueryRow(ctx, `INSERT INTO projects (name, profile) VALUES ($1, $2) RETURNING id`, name, profile).Scan(&id)
	}
	return id, err
}

// parseDate accepts an empty string as "no date" (nil) and otherwise YYYY-MM-DD.
func parseDate(field, v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	d, err := time.Parse(time.DateOnly, v)
	if err != nil || d.Year() < 1000 {
		return nil, fail(http.StatusBadRequest, field+" must be a date like 2025-03-31")
	}
	return &d, nil
}

func fmtDate(d *time.Time) *string {
	if d == nil {
		return nil
	}
	s := d.Format(time.DateOnly)
	return &s
}

func runeBetween(s string, lo, hi int) bool {
	n := utf8.RuneCountInString(s)
	return n >= lo && n <= hi
}

type characterJSON struct {
	ID           int64   `json:"id"`
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Appearance   string  `json:"appearance"`
	Personality  string  `json:"personality"`
	Voice        string  `json:"voice"`
	ArrivesOn    *string `json:"arrives_on"`
	Status       string  `json:"status"`
	StatusSince  *string `json:"status_since"`
	LoraName     string  `json:"lora_name"`
	TriggerToken string  `json:"trigger_token"`
}

func toCharacterJSON(c canon.Character) characterJSON {
	return characterJSON{c.ID, c.Slug, c.Name, c.Appearance, c.Personality, c.Voice, fmtDate(c.ArrivesOn),
		c.Status, fmtDate(c.StatusSince), c.LoraName, c.TriggerToken}
}

type characterReq struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Appearance   string `json:"appearance"`
	Personality  string `json:"personality"`
	Voice        string `json:"voice"`
	ArrivesOn    string `json:"arrives_on"`
	Status       string `json:"status"`
	StatusSince  string `json:"status_since"`
	LoraName     string `json:"lora_name"`
	TriggerToken string `json:"trigger_token"`
}

// validate trims the text fields in place and returns the parsed dates. A status of alive stores no
// status_since, so a character who was marked deceased and is set back to alive carries no stale date.
func (r *characterReq) validate(needSlug bool) (arrives, since *time.Time, err error) {
	r.Slug, r.Name, r.Appearance = strings.TrimSpace(r.Slug), strings.TrimSpace(r.Name), strings.TrimSpace(r.Appearance)
	r.Personality, r.Voice = strings.TrimSpace(r.Personality), strings.TrimSpace(r.Voice)
	r.LoraName, r.TriggerToken = strings.TrimSpace(r.LoraName), strings.TrimSpace(r.TriggerToken)
	if r.Status == "" {
		r.Status = canon.StatusAlive
	}
	switch {
	case needSlug && !slugRe.MatchString(r.Slug):
		return nil, nil, fail(http.StatusBadRequest, "id must be 1 to 16 characters: a lowercase letter, then lowercase letters or digits")
	case !runeBetween(r.Name, 1, 60):
		return nil, nil, fail(http.StatusBadRequest, "name must be 1 to 60 characters")
	case !runeBetween(r.Appearance, 5, 600):
		return nil, nil, fail(http.StatusBadRequest, "appearance must be 5 to 600 characters")
	case !runeBetween(r.Personality, 0, 600):
		return nil, nil, fail(http.StatusBadRequest, "personality must be at most 600 characters")
	case !runeBetween(r.Voice, 5, 200):
		return nil, nil, fail(http.StatusBadRequest, "voice must be 5 to 200 characters")
	case !loraRe.MatchString(r.LoraName):
		return nil, nil, fail(http.StatusBadRequest, "lora_name may only contain letters, digits, underscore, dot and dash (at most 100)")
	case !runeBetween(r.TriggerToken, 0, 60):
		return nil, nil, fail(http.StatusBadRequest, "trigger_token must be at most 60 characters")
	case r.Status != canon.StatusAlive && r.Status != canon.StatusDeceased && r.Status != canon.StatusMovedAway:
		return nil, nil, fail(http.StatusBadRequest, "status must be alive, deceased or moved_away")
	}
	if arrives, err = parseDate("arrives_on", r.ArrivesOn); err != nil {
		return nil, nil, err
	}
	if since, err = parseDate("status_since", r.StatusSince); err != nil {
		return nil, nil, err
	}
	if r.Status == canon.StatusAlive {
		since = nil
	} else if since == nil {
		return nil, nil, fail(http.StatusBadRequest, "status_since is required when the character is not alive")
	}
	return arrives, since, nil
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func (s *Server) listCharacters(c echo.Context) error {
	ctx := c.Request().Context()
	out := []characterJSON{}
	var projectID int64
	err := s.DB.QueryRow(ctx, `SELECT id FROM projects WHERE profile = $1 ORDER BY id LIMIT 1`, pipeline.FictionalSeriesID).Scan(&projectID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		chars, err := canon.Characters(ctx, s.DB, projectID)
		if err != nil {
			return err
		}
		for _, ch := range chars {
			out = append(out, toCharacterJSON(ch))
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"characters": out})
}

func (s *Server) createCharacter(c echo.Context) error {
	var req characterReq
	if err := bind(c, &req); err != nil {
		return err
	}
	arrives, since, err := req.validate(true)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var id int64
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		projectID, err := ensureProject(ctx, tx, pipeline.FictionalSeriesID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO characters (project_id, slug, name, appearance, personality, voice, arrives_on, status, status_since, lora_name, trigger_token)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			projectID, req.Slug, req.Name, req.Appearance, req.Personality, req.Voice, arrives, req.Status, since, req.LoraName, req.TriggerToken).Scan(&id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: actor(c), Action: "canon.character.create", TargetType: "character", TargetID: id,
			Detail: map[string]any{"slug": req.Slug, "status": req.Status}})
	})
	if isUniqueViolation(err) {
		return fail(http.StatusConflict, "a character with this id already exists")
	}
	if err != nil {
		return err
	}
	s.Log.Info("character created", "character_id", id, "slug", req.Slug)
	return c.JSON(http.StatusCreated, map[string]int64{"id": id})
}

// updateCharacter replaces every field except the slug. The slug is how scripts and summaries refer
// to the character, so it is fixed once created; sending a different one is rejected.
func (s *Server) updateCharacter(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var req characterReq
	if err := bind(c, &req); err != nil {
		return err
	}
	arrives, since, err := req.validate(false)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var slug string
	if err := s.DB.QueryRow(ctx, `SELECT slug FROM characters WHERE id = $1`, id).Scan(&slug); err != nil {
		return err
	}
	if req.Slug != "" && req.Slug != slug {
		return fail(http.StatusBadRequest, "the id of a character cannot be changed")
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE characters SET name=$2, appearance=$3, personality=$4, voice=$5, arrives_on=$6, status=$7,
			status_since=$8, lora_name=$9, trigger_token=$10 WHERE id=$1`,
			id, req.Name, req.Appearance, req.Personality, req.Voice, arrives, req.Status, since, req.LoraName, req.TriggerToken); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: actor(c), Action: "canon.character.update", TargetType: "character", TargetID: id,
			Detail: map[string]any{"slug": slug, "status": req.Status}})
	})
	if err != nil {
		return err
	}
	s.Log.Info("character updated", "character_id", id, "slug", slug)
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

type summaryJSON struct {
	Summary           string    `json:"summary"`
	CharactersPresent []string  `json:"characters_present"`
	OpenThreads       []string  `json:"open_threads"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// loadSummary returns nil when the episode has no summary yet.
func (s *Server) loadSummary(ctx context.Context, episodeID int64) (*summaryJSON, error) {
	var m summaryJSON
	err := s.DB.QueryRow(ctx, `SELECT summary, characters_present, open_threads, updated_at FROM episode_summaries WHERE episode_id = $1`, episodeID).
		Scan(&m.Summary, &m.CharactersPresent, &m.OpenThreads, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &m, err
}

func (s *Server) putSummary(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var req struct {
		Summary           string   `json:"summary"`
		CharactersPresent []string `json:"characters_present"`
		OpenThreads       []string `json:"open_threads"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	req.Summary = strings.TrimSpace(req.Summary)
	if !runeBetween(req.Summary, 1, 2000) {
		return fail(http.StatusBadRequest, "summary must be 1 to 2000 characters")
	}
	threads := []string{}
	for _, th := range req.OpenThreads {
		if th = strings.TrimSpace(th); th != "" {
			threads = append(threads, th)
		}
	}
	if len(threads) > maxThreads {
		return fail(http.StatusBadRequest, "at most 10 open threads")
	}
	for _, th := range threads {
		if utf8.RuneCountInString(th) > maxThreadLength {
			return fail(http.StatusBadRequest, "each open thread must be at most 200 characters")
		}
	}
	present, seen := []string{}, map[string]bool{}
	for _, slug := range req.CharactersPresent {
		if slug = strings.TrimSpace(slug); slug != "" && !seen[slug] {
			seen[slug] = true
			present = append(present, slug)
		}
	}
	ctx := c.Request().Context()
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var projectID int64
		if err := tx.QueryRow(ctx, `SELECT project_id FROM episodes WHERE id = $1`, id).Scan(&projectID); err != nil {
			return err
		}
		var known int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM characters WHERE project_id = $1 AND slug = ANY($2)`, projectID, present).Scan(&known); err != nil {
			return err
		}
		if known != len(present) {
			return fail(http.StatusBadRequest, "characters_present contains an id that is not a character of this series")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO episode_summaries (episode_id, summary, characters_present, open_threads) VALUES ($1,$2,$3,$4)
			ON CONFLICT (episode_id) DO UPDATE SET summary = EXCLUDED.summary, characters_present = EXCLUDED.characters_present,
			open_threads = EXCLUDED.open_threads, updated_at = now()`, id, req.Summary, present, threads); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: actor(c), Action: "canon.summary.put", TargetType: "episode", TargetID: id,
			Detail: map[string]any{"summary_chars": utf8.RuneCountInString(req.Summary), "open_threads": len(threads)}})
	})
	if err != nil {
		return err
	}
	s.Log.Info("episode summary saved", "episode_id", id)
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// putInStoryDate sets or clears (empty string) the episode's in-story date.
func (s *Server) putInStoryDate(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var req struct {
		InStoryDate string `json:"in_story_date"`
	}
	if err := bind(c, &req); err != nil {
		return err
	}
	d, err := parseDate("in_story_date", req.InStoryDate)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE episodes SET in_story_date = $2 WHERE id = $1`, id, d)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		detail := map[string]any{}
		if d != nil {
			detail["in_story_date"] = d.Format(time.DateOnly)
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: actor(c), Action: "canon.in_story_date.put", TargetType: "episode", TargetID: id, Detail: detail})
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}
