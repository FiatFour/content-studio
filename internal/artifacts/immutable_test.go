package artifacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

func TestArtifactRowsAreImmutableExceptValidationStatus(t *testing.T) {
	db := storetest.New(t)
	project, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: jobs.Plan{Full: []string{"a"}}, MaxAttempts: 1}
	ctx := context.Background()
	runID, err := q.CreateRun(ctx, ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err := q.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := artifacts.Artifact{ProjectID: project, EpisodeID: ep, RunID: runID, JobID: j.ID, Type: artifacts.Video, StoragePath: "x.mp4", SHA256: "abc", SizeBytes: 3}
	if err := q.Complete(ctx, j, func(tx pgx.Tx) error { return artifacts.Insert(ctx, tx, &a) }); err != nil {
		t.Fatal(err)
	}
	// The QA stage's update is the one allowed change.
	if _, err := db.Exec(ctx, `UPDATE artifacts SET validation_status = 'qa_passed' WHERE id = $1`, a.ID); err != nil {
		t.Fatalf("validation_status update refused: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE artifacts SET sha256 = 'other' WHERE id = $1`,
		`UPDATE artifacts SET storage_path = 'y.mp4' WHERE id = $1`,
		`UPDATE artifacts SET size_bytes = 1 WHERE id = $1`,
		`UPDATE artifacts SET meta = '{"a":1}' WHERE id = $1`,
		`UPDATE artifacts SET version = 99 WHERE id = $1`,
		`UPDATE artifacts SET validation_status = 'qa_failed', sha256 = 'other' WHERE id = $1`,
		`DELETE FROM artifacts WHERE id = $1`,
	} {
		if _, err := db.Exec(ctx, stmt, a.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("%s: err %v, want an immutability refusal", stmt, err)
		}
	}
	if _, err := db.Exec(ctx, `TRUNCATE artifacts CASCADE`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("TRUNCATE: err %v, want an immutability refusal", err)
	}
	var sha, status string
	if err := db.QueryRow(ctx, `SELECT sha256, validation_status FROM artifacts WHERE id = $1`, a.ID).Scan(&sha, &status); err != nil || sha != "abc" || status != "qa_passed" {
		t.Fatalf("row after refused changes: sha %q status %q err %v", sha, status, err)
	}
}
