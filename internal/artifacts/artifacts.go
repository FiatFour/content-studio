// Package artifacts stores immutable, versioned, hashed stage outputs under one storage root.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Type names the kind of artifact. Item keys separate several artifacts of one type in a run,
// such as one image per shot ("shot-003").
type Type string

const (
	Script      Type = "script"
	Image       Type = "image"
	VoiceRef    Type = "voice_ref"
	LineAudio   Type = "line_audio"
	Dialogue    Type = "dialogue"
	Subtitles   Type = "subtitles"
	RenderSpec  Type = "render_spec"
	Video       Type = "video"
	QAReport    Type = "qa_report"
	ScriptInput Type = "script_prompt"
	Timeline    Type = "timeline"
	// Source is owner-pasted text for a Knowledge Shorts episode (D15). It belongs to an episode but to
	// no run or job: the owner creates it, not a stage.
	Source Type = "source"
)

var extensions = map[Type]string{
	Script: "json", Image: "png", VoiceRef: "wav", LineAudio: "wav", Dialogue: "wav",
	Subtitles: "srt", RenderSpec: "json", Video: "mp4", QAReport: "json", ScriptInput: "json", Timeline: "json", Source: "txt",
}

type Artifact struct {
	ID          int64
	ProjectID   int64
	EpisodeID   int64
	RunID       int64
	JobID       int64
	Type        Type
	ItemKey     string
	Version     int
	StoragePath string
	SHA256      string
	SizeBytes   int64
	Provider    string
	Model       string
	Meta        map[string]any
	License     map[string]any
	Parents     []int64
}

// Store writes files under Root. Paths are built only from IDs and fixed names, never from input.
type Store struct {
	Root string
	// WrapWriter, when set, wraps the temp-file writer. Production leaves it nil; tests use it to
	// make a write fail the way a full disk does, deterministically.
	WrapWriter func(io.Writer) io.Writer
}

// File is a written but not yet recorded artifact file.
type File struct {
	RelPath string
	SHA256  string
	Size    int64
}

// Loc places a job attempt's files. Each attempt gets its own directory, so a rerun after a
// crash never collides with a half-written file from the attempt before.
type Loc struct {
	EpisodeID, RunID, JobID int64
	Attempt                 int
	// Sub, when set, places files in episodes/<id>/<Sub> instead of a job attempt directory. It is
	// for artifacts that belong to no run (sources) and must be built from fixed names and ids only.
	Sub string
}

func (l Loc) Dir() string {
	if l.Sub != "" {
		return fmt.Sprintf("episodes/%d/%s", l.EpisodeID, l.Sub)
	}
	return fmt.Sprintf("episodes/%d/runs/%d/jobs/%d/a%d", l.EpisodeID, l.RunID, l.JobID, l.Attempt)
}

// Write streams content to a temp file, fsyncs it, and renames it into place. An existing
// path is never overwritten.
func (s *Store) Write(loc Loc, typ Type, itemKey string, write func(io.Writer) error) (File, error) {
	ext, ok := extensions[typ]
	if !ok {
		return File{}, fmt.Errorf("unknown artifact type %q", typ)
	}
	name := string(typ)
	if itemKey != "" {
		if !validKey(itemKey) {
			return File{}, fmt.Errorf("invalid item key %q", itemKey)
		}
		name += "-" + itemKey
	}
	rel := filepath.Join(loc.Dir(), name+"."+ext)
	abs, err := s.Abs(rel)
	if err != nil {
		return File{}, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return File{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".tmp-*")
	if err != nil {
		return File{}, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	var dst io.Writer = tmp
	if s.WrapWriter != nil {
		dst = s.WrapWriter(tmp)
	}
	cw := &countWriter{w: io.MultiWriter(dst, h)}
	if err := write(cw); err != nil {
		tmp.Close()
		return File{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return File{}, err
	}
	if err := tmp.Close(); err != nil {
		return File{}, err
	}
	if _, err := os.Stat(abs); err == nil {
		return File{}, fmt.Errorf("artifact file %s already exists", rel)
	}
	if err := os.Rename(tmp.Name(), abs); err != nil {
		return File{}, err
	}
	return File{RelPath: rel, SHA256: hex.EncodeToString(h.Sum(nil)), Size: cw.n}, nil
}

// WriteFile moves an already-produced file (e.g. FFmpeg output) into the store by copying it.
func (s *Store) WriteFile(loc Loc, typ Type, itemKey, src string) (File, error) {
	f, err := os.Open(src)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	return s.Write(loc, typ, itemKey, func(w io.Writer) error {
		_, err := io.Copy(w, f)
		return err
	})
}

// Abs resolves a stored relative path and refuses anything that escapes the root.
func (s *Store) Abs(rel string) (string, error) {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	abs := filepath.Join(root, rel)
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", errors.New("path escapes storage root")
	}
	return abs, nil
}

// Hash re-reads a stored file and returns its sha256.
func (s *Store) Hash(rel string) (string, error) {
	abs, err := s.Abs(rel)
	if err != nil {
		return "", err
	}
	return HashFile(abs)
}

func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validKey(k string) bool {
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return k != ""
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Insert records an artifact inside tx. The version is the next one for this episode, type and item.
func Insert(ctx context.Context, tx pgx.Tx, a *Artifact) error {
	meta, err := json.Marshal(orEmpty(a.Meta))
	if err != nil {
		return err
	}
	lic, err := json.Marshal(orEmpty(a.License))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, a.EpisodeID); err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `INSERT INTO artifacts (project_id, episode_id, run_id, job_id, type, item_key, version, storage_path, sha256,
			size_bytes, validation_status, provider, model, meta, license)
		VALUES ($1, $2, $3, $4, $5, $6,
			(SELECT coalesce(max(version), 0) + 1 FROM artifacts WHERE episode_id = $2 AND type = $5 AND item_key = $6),
			$7, $8, $9, 'valid', $10, $11, $12, $13)
		RETURNING id, version`,
		a.ProjectID, a.EpisodeID, nullID(a.RunID), nullID(a.JobID), a.Type, a.ItemKey, a.StoragePath, a.SHA256, a.SizeBytes,
		a.Provider, a.Model, meta, lic).Scan(&a.ID, &a.Version)
	if err != nil {
		return err
	}
	for _, p := range a.Parents {
		if _, err := tx.Exec(ctx, `INSERT INTO artifact_parents (artifact_id, parent_id) VALUES ($1, $2)`, a.ID, p); err != nil {
			return err
		}
	}
	return nil
}

// nullID stores 0 as NULL. Only sources have no run or job; the table's CHECK refuses NULL for any other type.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// Querier is satisfied by both a pool and a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ForRun lists a run's artifacts of one type, ordered by item key.
func ForRun(ctx context.Context, db Querier, runID int64, typ Type) ([]Artifact, error) {
	rows, err := db.Query(ctx, `SELECT id, project_id, episode_id, run_id, job_id, type, item_key, version, storage_path, sha256, size_bytes,
		provider, model, meta FROM artifacts WHERE run_id = $1 AND type = $2 ORDER BY item_key, version`, runID, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.EpisodeID, &a.RunID, &a.JobID, &a.Type, &a.ItemKey, &a.Version, &a.StoragePath,
			&a.SHA256, &a.SizeBytes, &a.Provider, &a.Model, &a.Meta); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
