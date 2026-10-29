// Command seedfixture creates one approved episode with a tiny real video in a throwaway
// database, using the app's own store, artifact and review code. It exists only for
// scripts/backup/restore-test.sh --fixture and refuses to touch any database that is not
// named studio_rt_src_*.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store"
)

var fixtureDB = regexp.MustCompile(`^studio_rt_src_[a-z0-9_]+$`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seedfixture:", err)
		os.Exit(1)
	}
}

func run() error {
	dbURL, root, video := os.Getenv("STUDIO_DATABASE_URL"), os.Getenv("STUDIO_STORAGE_ROOT"), os.Getenv("FIXTURE_VIDEO")
	u, err := url.Parse(dbURL)
	if err != nil {
		return fmt.Errorf("STUDIO_DATABASE_URL is not a valid URL")
	}
	if name := strings.TrimPrefix(u.Path, "/"); !fixtureDB.MatchString(name) {
		return fmt.Errorf("refusing to seed database %q: only studio_rt_src_* is allowed", name)
	}
	if root == "" || video == "" {
		return fmt.Errorf("STUDIO_STORAGE_ROOT and FIXTURE_VIDEO are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	st := &artifacts.Store{Root: root}
	var projectID, episodeID, runID, jobID int64
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO projects (name, profile) VALUES ('restore-test fixture', 'fictional_series') RETURNING id`).Scan(&projectID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s) VALUES ($1, 1, 'Fixture episode', 'restore-test fixture', 60) RETURNING id`, projectID).Scan(&episodeID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'fictional_series', 'full', 'ready_for_review') RETURNING id`, episodeID).Scan(&runID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO jobs (run_id, stage, seq, state, attempt_count) VALUES ($1, 'qa', 1, 'completed', 1) RETURNING id`, runID).Scan(&jobID)
	})
	if err != nil {
		return err
	}
	f, err := st.WriteFile(artifacts.Loc{EpisodeID: episodeID, RunID: runID, JobID: jobID, Attempt: 1}, artifacts.Video, "", video)
	if err != nil {
		return err
	}
	a := &artifacts.Artifact{ProjectID: projectID, EpisodeID: episodeID, RunID: runID, JobID: jobID, Type: artifacts.Video,
		StoragePath: f.RelPath, SHA256: f.SHA256, SizeBytes: f.Size, Provider: "fixture", Model: "fixture"}
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := artifacts.Insert(ctx, tx, a); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE artifacts SET validation_status = 'qa_passed' WHERE id = $1`, a.ID)
		return err
	})
	if err != nil {
		return err
	}
	svc := &review.Service{DB: db, Store: st}
	if _, err := svc.Decide(ctx, a.ID, a.SHA256, review.Approved, "restore-test fixture", "owner"); err != nil {
		return err
	}
	fmt.Printf("seeded artifact_id=%d sha256=%s\n", a.ID, a.SHA256)
	return nil
}
