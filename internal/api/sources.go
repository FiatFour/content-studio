package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/audit"
	"github.com/fiat/content-studio/internal/pipeline"
)

// Knowledge Shorts sources (D15). The owner pastes text; the app stores it as an immutable, hashed
// artifact of type "source" and never fetches, searches or dereferences anything. A source is a series
// of versions sharing one item key ("s1", "s2", ...) that later claims cite by source id.
const (
	MaxSourceBytes       = 200 << 10 // one source's text, after line-ending normalization
	MaxSourcesPerEpisode = 20
	MaxSourceTitleRunes  = 120
	MaxSourceCiteRunes   = 500
)

var sourceIDRe = regexp.MustCompile(`^s[1-9][0-9]{0,3}$`)

type sourceRequest struct {
	Title    string `json:"title"`
	Citation string `json:"citation"`
	Text     string `json:"text"`
}

// clean validates one request. Text keeps newline and tab; any other control character is refused.
// CRLF is normalized to LF first, so the stored bytes (and hash) do not depend on the browser.
func (r *sourceRequest) clean() error {
	r.Title = strings.TrimSpace(r.Title)
	r.Citation = strings.TrimSpace(r.Citation)
	r.Text = strings.ReplaceAll(r.Text, "\r\n", "\n")
	switch {
	case r.Title == "" || utf8.RuneCountInString(r.Title) > MaxSourceTitleRunes:
		return fail(http.StatusBadRequest, fmt.Sprintf("title must be 1 to %d characters", MaxSourceTitleRunes))
	case hasControl(r.Title, false):
		return fail(http.StatusBadRequest, "title must not contain control characters")
	case utf8.RuneCountInString(r.Citation) > MaxSourceCiteRunes:
		return fail(http.StatusBadRequest, fmt.Sprintf("citation note must be at most %d characters", MaxSourceCiteRunes))
	case hasControl(r.Citation, true):
		return fail(http.StatusBadRequest, "citation note must not contain control characters")
	case strings.TrimSpace(r.Text) == "":
		return fail(http.StatusBadRequest, "source text is required")
	case len(r.Text) > MaxSourceBytes:
		return fail(http.StatusRequestEntityTooLarge, fmt.Sprintf("source text must be at most %d bytes", MaxSourceBytes))
	case !utf8.ValidString(r.Text):
		return fail(http.StatusBadRequest, "source text must be valid UTF-8")
	case hasControl(r.Text, true):
		return fail(http.StatusBadRequest, "source text must not contain control characters other than newline and tab")
	}
	return nil
}

func hasControl(s string, allowNewlineTab bool) bool {
	for _, r := range s {
		if allowNewlineTab && (r == '\n' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) || r == '￾' || r == '￿' {
			return true
		}
	}
	return false
}

type sourceVersion struct {
	ID        int64     `json:"id"`
	Version   int       `json:"version"`
	SHA256    string    `json:"sha256"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type sourceItem struct {
	SourceID string `json:"source_id"`
	Title    string `json:"title"`
	Citation string `json:"citation"`
	sourceVersion
	Versions []sourceVersion `json:"versions"`
}

// sourceEpisode loads the episode's project id and refuses any profile but knowledge_shorts.
func sourceEpisode(c echo.Context, q querier, id int64) (int64, error) {
	var projectID int64
	var profile string
	if err := q.QueryRow(c.Request().Context(), `SELECT e.project_id, p.profile FROM episodes e JOIN projects p ON p.id = e.project_id WHERE e.id = $1`, id).
		Scan(&projectID, &profile); err != nil {
		return 0, err
	}
	if profile != pipeline.KnowledgeShortsID {
		return 0, fail(http.StatusConflict, "sources apply only to knowledge_shorts episodes")
	}
	return projectID, nil
}

func (s *Server) listSources(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	if _, err := sourceEpisode(c, s.DB, id); err != nil {
		return err
	}
	rows, err := s.DB.Query(c.Request().Context(), `SELECT id, item_key, version, sha256, size_bytes, created_at,
			coalesce(meta->>'title', ''), coalesce(meta->>'citation', '')
		FROM artifacts WHERE episode_id = $1 AND type = 'source' ORDER BY item_key, version`, id)
	if err != nil {
		return err
	}
	type row struct {
		key, title, cite string
		v                sourceVersion
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.v.ID, &x.key, &x.v.Version, &x.v.SHA256, &x.v.SizeBytes, &x.v.CreatedAt, &x.title, &x.cite)
		return x, err
	})
	if err != nil {
		return err
	}
	items := []sourceItem{}
	for _, x := range all { // ordered by key then version, so a key's last row is its latest version
		if n := len(items); n > 0 && items[n-1].SourceID == x.key {
			it := &items[n-1]
			it.Versions = append(it.Versions, x.v)
			it.Title, it.Citation, it.sourceVersion = x.title, x.cite, x.v
			continue
		}
		items = append(items, sourceItem{SourceID: x.key, Title: x.title, Citation: x.cite, sourceVersion: x.v, Versions: []sourceVersion{x.v}})
	}
	num := func(s string) int { n, _ := strconv.Atoi(s[1:]); return n } // "s2" before "s10"
	sort.Slice(items, func(i, j int) bool { return num(items[i].SourceID) < num(items[j].SourceID) })
	return c.JSON(http.StatusOK, map[string]any{"sources": items, "max_sources": MaxSourcesPerEpisode, "max_bytes": MaxSourceBytes})
}

func (s *Server) createSource(c echo.Context) error { return s.writeSource(c, "") }

func (s *Server) versionSource(c echo.Context) error {
	key := c.Param("source")
	if !sourceIDRe.MatchString(key) {
		return fail(http.StatusBadRequest, "invalid source id")
	}
	return s.writeSource(c, key)
}

// writeSource stores one new version; key is empty for a new source. The episode lock, the cap and
// version checks, the file, the artifact row and the audit row share one transaction. If any step
// fails the rows roll back and the file is removed.
func (s *Server) writeSource(c echo.Context, key string) (err error) {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var req sourceRequest
	if err := bind(c, &req); err != nil {
		return err
	}
	if err := req.clean(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	var a artifacts.Artifact
	var written string
	defer func() {
		if err != nil && written != "" {
			if abs, aerr := s.Store.Abs(written); aerr == nil {
				_ = os.Remove(abs)
			}
		}
	}()
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, id); err != nil { // the lock artifacts.Insert takes too
			return err
		}
		projectID, err := sourceEpisode(c, tx, id)
		if err != nil {
			return err
		}
		version, action := 1, "source.create"
		if key == "" {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(DISTINCT item_key) FROM artifacts WHERE episode_id = $1 AND type = 'source'`, id).Scan(&n); err != nil {
				return err
			}
			if n >= MaxSourcesPerEpisode {
				return fail(http.StatusConflict, fmt.Sprintf("an episode can have at most %d sources", MaxSourcesPerEpisode))
			}
			key = fmt.Sprintf("s%d", n+1)
		} else {
			action = "source.version"
			var prev struct {
				id          int64
				version     int
				sha         string
				title, cite string
			}
			if err := tx.QueryRow(ctx, `SELECT id, version, sha256, coalesce(meta->>'title', ''), coalesce(meta->>'citation', '') FROM artifacts
				WHERE episode_id = $1 AND type = 'source' AND item_key = $2 ORDER BY version DESC LIMIT 1`, id, key).
				Scan(&prev.id, &prev.version, &prev.sha, &prev.title, &prev.cite); err != nil {
				return err // pgx.ErrNoRows becomes 404
			}
			// An identical resubmission would add a version that differs from the last in nothing.
			sum := sha256.Sum256([]byte(req.Text))
			if prev.title == req.Title && prev.cite == req.Citation && prev.sha == hex.EncodeToString(sum[:]) {
				return fail(http.StatusConflict, "nothing changed since the latest version")
			}
			version = prev.version + 1
			a.Parents = []int64{prev.id}
		}
		f, err := s.Store.Write(artifacts.Loc{EpisodeID: id, Sub: fmt.Sprintf("sources/%s/v%d", key, version)}, artifacts.Source, key,
			func(w io.Writer) error { _, err := io.WriteString(w, req.Text); return err })
		if err != nil {
			return err
		}
		written = f.RelPath
		a.ProjectID, a.EpisodeID, a.Type, a.ItemKey = projectID, id, artifacts.Source, key
		a.StoragePath, a.SHA256, a.SizeBytes, a.Provider = f.RelPath, f.SHA256, f.Size, "owner"
		a.Meta = map[string]any{"title": req.Title, "citation": req.Citation}
		if err := artifacts.Insert(ctx, tx, &a); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Entry{Actor: actor(c), Action: action, TargetType: "source", TargetID: a.ID,
			Detail: map[string]any{"episode_id": id, "source_id": key, "version": a.Version, "sha_prefix": a.SHA256[:12], "size_bytes": a.SizeBytes}})
	})
	if err != nil {
		return err
	}
	s.Log.Info("source stored", "episode_id", id, "source_id", key, "version", a.Version, "sha256", a.SHA256)
	return c.JSON(http.StatusCreated, map[string]any{"id": a.ID, "source_id": key, "version": a.Version, "sha256": a.SHA256, "size_bytes": a.SizeBytes})
}
