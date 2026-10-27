// Package review records human decisions on an exact video version and exports only approved bytes.
package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/artifacts"
)

type Decision string

const (
	Approved         Decision = "approved"
	Rejected         Decision = "rejected"
	ChangesRequested Decision = "changes_requested"
)

// ErrRefused marks a request the rules forbid. Its message is safe to show.
type ErrRefused struct{ Reason string }

func (e *ErrRefused) Error() string { return e.Reason }

func refuse(format string, args ...any) error {
	return &ErrRefused{Reason: fmt.Sprintf(format, args...)}
}

type Service struct {
	DB        *pgxpool.Pool
	Store     *artifacts.Store
	ExportDir string
}

type Approval struct {
	ID         int64
	ArtifactID int64
	SHA256     string
	Decision   Decision
	Notes      string
	Reviewer   string
}

// Decide records an explicit decision on one video version. The sha256 the reviewer saw must
// match both the record and the file on disk, so a decision can never land on different bytes.
func (s *Service) Decide(ctx context.Context, artifactID int64, seenSHA256 string, d Decision, notes, reviewer string) (Approval, error) {
	switch d {
	case Approved:
	case Rejected, ChangesRequested:
		if notes == "" {
			return Approval{}, refuse("notes are required to %s", map[Decision]string{Rejected: "reject", ChangesRequested: "request changes"}[d])
		}
	default:
		return Approval{}, refuse("unknown decision %q", d)
	}
	var a Approval
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var typ, status, sha, path string
		err := tx.QueryRow(ctx, `SELECT type, validation_status, sha256, storage_path FROM artifacts WHERE id = $1 FOR UPDATE`, artifactID).Scan(&typ, &status, &sha, &path)
		if errors.Is(err, pgx.ErrNoRows) {
			return refuse("artifact %d does not exist", artifactID)
		}
		if err != nil {
			return err
		}
		if typ != string(artifacts.Video) {
			return refuse("only videos can be reviewed")
		}
		if seenSHA256 != sha {
			return refuse("this decision was made on a different version (sha256 mismatch)")
		}
		if d == Approved && status != "qa_passed" {
			return refuse("this version has not passed QA (status %s)", status)
		}
		onDisk, err := s.Store.Hash(path)
		if err != nil || onDisk != sha {
			return refuse("the stored file no longer matches this version's sha256")
		}
		a = Approval{ArtifactID: artifactID, SHA256: sha, Decision: d, Notes: notes, Reviewer: reviewer}
		return tx.QueryRow(ctx, `INSERT INTO approvals (artifact_id, sha256, decision, notes, reviewer) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			artifactID, sha, d, notes, reviewer).Scan(&a.ID)
	})
	return a, err
}

// Latest returns the most recent decision on a version, or nil if it has none (unapproved).
func (s *Service) Latest(ctx context.Context, artifactID int64) (*Approval, error) {
	var a Approval
	err := s.DB.QueryRow(ctx, `SELECT id, artifact_id, sha256, decision, notes, reviewer FROM approvals WHERE artifact_id = $1 ORDER BY id DESC LIMIT 1`, artifactID).
		Scan(&a.ID, &a.ArtifactID, &a.SHA256, &a.Decision, &a.Notes, &a.Reviewer)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

type Export struct {
	ID     int64
	Path   string
	SHA256 string
}

// Export copies the approved bytes of one version and verifies the copy. It never renders or
// substitutes another version, and refuses if anything does not match the approved hash.
func (s *Service) Export(ctx context.Context, artifactID int64) (Export, error) {
	ap, err := s.Latest(ctx, artifactID)
	if err != nil {
		return Export{}, err
	}
	if ap == nil || ap.Decision != Approved {
		return Export{}, refuse("this version is not approved")
	}
	var episodeID int64
	var version int
	var sha, rel string
	if err := s.DB.QueryRow(ctx, `SELECT episode_id, version, sha256, storage_path FROM artifacts WHERE id = $1`, artifactID).Scan(&episodeID, &version, &sha, &rel); err != nil {
		return Export{}, err
	}
	if sha != ap.SHA256 {
		return Export{}, refuse("approval and artifact hashes differ")
	}
	src, err := s.Store.Abs(rel)
	if err != nil {
		return Export{}, err
	}
	if got, err := artifacts.HashFile(src); err != nil || got != ap.SHA256 {
		return Export{}, refuse("the stored file no longer matches the approved sha256; export refused")
	}
	if err := os.MkdirAll(s.ExportDir, 0o755); err != nil {
		return Export{}, err
	}
	name := fmt.Sprintf("episode-%d-v%d-%s.mp4", episodeID, version, sha[:12])
	dst := filepath.Join(s.ExportDir, name)
	if err := copyNew(src, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Export{}, refuse("%s was already exported", name)
		}
		return Export{}, err
	}
	got, err := artifacts.HashFile(dst)
	if err != nil || got != ap.SHA256 {
		os.Remove(dst)
		return Export{}, refuse("exported copy does not match the approved sha256; removed it")
	}
	e := Export{Path: dst, SHA256: got}
	err = s.DB.QueryRow(ctx, `INSERT INTO exports (approval_id, artifact_id, export_path, sha256) VALUES ($1, $2, $3, $4) RETURNING id`,
		ap.ID, artifactID, dst, got).Scan(&e.ID)
	return e, err
}

func copyNew(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
