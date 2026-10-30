package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

// Read-only views over the artifacts table. Nothing here approves, exports or renders anything.

const (
	historyMaxRows   = 1000
	compareMaxBytes  = 1 << 20 // larger JSON files are not diffed in memory
	compareMaxDiffs  = 500
	compareMaxString = 300 // a changed value is shortened to this many runes
)

type historyRow struct {
	ID               int64     `json:"id"`
	RunID            int64     `json:"run_id"`
	Type             string    `json:"type"`
	ItemKey          string    `json:"item_key"`
	Version          int       `json:"version"`
	SHA256           string    `json:"sha256"`
	SizeBytes        int64     `json:"size_bytes"`
	ValidationStatus string    `json:"validation_status"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	CreatedAt        time.Time `json:"created_at"`
	Parents          []int64   `json:"parents"`
	// Approval is the latest decision recorded against this exact version AND its current hash;
	// "none" when there is no such decision. A decision for any other hash never counts.
	Approval string `json:"approval"`
}

// artifactHistory lists every artifact version of an episode with lineage and approval status.
func (s *Server) artifactHistory(c echo.Context) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	var one int
	if err := s.DB.QueryRow(ctx, `SELECT 1 FROM episodes WHERE id = $1`, id).Scan(&one); err != nil {
		return err
	}
	typ := c.QueryParam("type")
	rows, err := s.DB.Query(ctx, `SELECT a.id, a.run_id, a.type, a.item_key, a.version, a.sha256, a.size_bytes, a.validation_status,
			a.provider, a.model, a.created_at,
			coalesce((SELECT array_agg(p.parent_id ORDER BY p.parent_id) FROM artifact_parents p WHERE p.artifact_id = a.id), '{}'),
			coalesce((SELECT ap.decision FROM approvals ap WHERE ap.artifact_id = a.id AND ap.sha256 = a.sha256 ORDER BY ap.id DESC LIMIT 1), 'none')
		FROM artifacts a WHERE a.episode_id = $1 AND ($2 = '' OR a.type = $2)
		ORDER BY a.id DESC LIMIT $3`, id, typ, historyMaxRows+1)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[historyRow])
	if err != nil {
		return err
	}
	truncated := len(list) > historyMaxRows
	if truncated {
		list = list[:historyMaxRows]
	}
	return c.JSON(http.StatusOK, map[string]any{"artifacts": list, "truncated": truncated})
}

type diffEntry struct {
	Path   string `json:"path"`
	Op     string `json:"op"` // added | removed | changed
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type compareSide struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	ItemKey   string    `json:"item_key"`
	Version   int       `json:"version"`
	SHA256    string    `json:"sha256"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
	Approval  string    `json:"approval"`
}

// compareArtifacts compares two versions of the same episode and type. JSON artifacts get a
// structured diff computed here; everything else is described so the page can show them side by
// side through the content endpoint.
func (s *Server) compareArtifacts(c echo.Context) error {
	ida, err := strconv.ParseInt(c.QueryParam("a"), 10, 64)
	idb, err2 := strconv.ParseInt(c.QueryParam("b"), 10, 64)
	if err != nil || err2 != nil || ida <= 0 || idb <= 0 {
		return fail(http.StatusBadRequest, "a and b must be artifact ids")
	}
	ctx := c.Request().Context()
	load := func(id int64) (compareSide, int64, string, error) {
		var x compareSide
		var ep int64
		var rel string
		err := s.DB.QueryRow(ctx, `SELECT a.id, a.type, a.item_key, a.version, a.sha256, a.size_bytes, a.created_at, a.episode_id, a.storage_path,
			coalesce((SELECT ap.decision FROM approvals ap WHERE ap.artifact_id = a.id AND ap.sha256 = a.sha256 ORDER BY ap.id DESC LIMIT 1), 'none')
			FROM artifacts a WHERE a.id = $1`, id).Scan(&x.ID, &x.Type, &x.ItemKey, &x.Version, &x.SHA256, &x.SizeBytes, &x.CreatedAt, &ep, &rel, &x.Approval)
		return x, ep, rel, err
	}
	a, epA, relA, err := load(ida)
	if err != nil {
		return err
	}
	b, epB, relB, err := load(idb)
	if err != nil {
		return err
	}
	if epA != epB || a.Type != b.Type {
		return fail(http.StatusBadRequest, "only two versions of the same episode and type can be compared")
	}
	out := map[string]any{"a": a, "b": b, "identical": a.SHA256 == b.SHA256}
	switch {
	case strings.HasSuffix(relA, ".json") && strings.HasSuffix(relB, ".json"):
		da, err := s.readJSON(relA)
		if err != nil {
			return err
		}
		db, err := s.readJSON(relB)
		if err != nil {
			return err
		}
		diffs := []diffEntry{}
		truncated := false
		diffJSON("", da, db, &diffs, &truncated)
		out["kind"], out["diffs"], out["truncated"] = "json", diffs, truncated
	case a.Type == "image":
		out["kind"] = "image"
	case a.Type == "video":
		out["kind"] = "video"
	case strings.HasSuffix(relA, ".wav"):
		out["kind"] = "audio"
	default:
		out["kind"] = "text"
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) readJSON(rel string) (any, error) {
	path, err := s.Store.Abs(rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fail(http.StatusNotFound, "artifact file is missing")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, compareMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > compareMaxBytes {
		return nil, fail(http.StatusRequestEntityTooLarge, "artifact is too large to compare")
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fail(http.StatusUnprocessableEntity, "artifact is not valid JSON")
	}
	return v, nil
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

func diffJSON(path string, a, b any, out *[]diffEntry, truncated *bool) {
	if *truncated {
		return
	}
	add := func(e diffEntry) {
		if len(*out) >= compareMaxDiffs {
			*truncated = true
			return
		}
		*out = append(*out, e)
	}
	switch av := a.(type) {
	case map[string]any:
		if bv, ok := b.(map[string]any); ok {
			keys := make([]string, 0, len(av)+len(bv))
			for k := range av {
				keys = append(keys, k)
			}
			for k := range bv {
				if _, dup := av[k]; !dup {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := path + "/" + pointerEscaper.Replace(k)
				x, inA := av[k]
				y, inB := bv[k]
				switch {
				case !inB:
					add(diffEntry{Path: p, Op: "removed", Before: show(x)})
				case !inA:
					add(diffEntry{Path: p, Op: "added", After: show(y)})
				default:
					diffJSON(p, x, y, out, truncated)
				}
			}
			return
		}
	case []any:
		if bv, ok := b.([]any); ok {
			for i := 0; i < len(av) || i < len(bv); i++ {
				p := path + "/" + strconv.Itoa(i)
				switch {
				case i >= len(bv):
					add(diffEntry{Path: p, Op: "removed", Before: show(av[i])})
				case i >= len(av):
					add(diffEntry{Path: p, Op: "added", After: show(bv[i])})
				default:
					diffJSON(p, av[i], bv[i], out, truncated)
				}
			}
			return
		}
	}
	if fmt.Sprintf("%T", a) != fmt.Sprintf("%T", b) || show(a) != show(b) {
		add(diffEntry{Path: path, Op: "changed", Before: show(a), After: show(b)})
	}
}

// show renders a value as JSON text, shortened. The client displays it as plain text only.
func show(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(raw)
	if utf8.RuneCountInString(s) > compareMaxString {
		s = string([]rune(s)[:compareMaxString]) + "…"
	}
	return s
}
