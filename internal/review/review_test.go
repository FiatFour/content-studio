package review_test

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

type fixture struct {
	svc   *review.Service
	store *artifacts.Store
}

// video stores a fake video version with the given QA status and returns its artifact.
func (f *fixture) video(t *testing.T, q *jobs.Queue, ep, project int64, content, status string) artifacts.Artifact {
	t.Helper()
	ctx := context.Background()
	runID, err := q.CreateRun(ctx, ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err := q.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file, err := f.store.Write(artifacts.Loc{EpisodeID: ep, RunID: runID, JobID: j.ID, Attempt: 1}, artifacts.Video, "",
		func(w io.Writer) error { _, err := io.WriteString(w, content); return err })
	if err != nil {
		t.Fatal(err)
	}
	a := artifacts.Artifact{ProjectID: project, EpisodeID: ep, RunID: runID, JobID: j.ID, Type: artifacts.Video, StoragePath: file.RelPath, SHA256: file.SHA256, SizeBytes: file.Size}
	err = q.Complete(ctx, j, func(tx pgx.Tx) error {
		if err := artifacts.Insert(ctx, tx, &a); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE artifacts SET validation_status = $1 WHERE id = $2`, status, a.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func setup(t *testing.T) (*fixture, *jobs.Queue, int64, int64) {
	db := storetest.New(t)
	project, ep := storetest.Episode(t, db)
	store := &artifacts.Store{Root: t.TempDir()}
	f := &fixture{svc: &review.Service{DB: db, Store: store, ExportDir: t.TempDir()}, store: store}
	return f, &jobs.Queue{DB: db, Plan: jobs.Plan{Full: []string{"render"}}, MaxAttempts: 1}, ep, project
}

func refused(err error) bool {
	var r *review.ErrRefused
	return errors.As(err, &r)
}

func TestApproveThenExportCopiesExactBytes(t *testing.T) {
	f, q, ep, project := setup(t)
	ctx := context.Background()
	v := f.video(t, q, ep, project, "video-v1", "qa_passed")
	if _, err := f.svc.Export(ctx, v.ID, "owner"); !refused(err) {
		t.Fatalf("export before approval: %v", err)
	}
	if _, err := f.svc.Decide(ctx, v.ID, v.SHA256, review.Approved, "", "owner"); err != nil {
		t.Fatal(err)
	}
	e, err := f.svc.Export(ctx, v.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if e.SHA256 != v.SHA256 {
		t.Fatalf("exported %s, approved %s", e.SHA256, v.SHA256)
	}
	if got, _ := artifacts.HashFile(e.Path); got != v.SHA256 {
		t.Fatal("exported file hash differs")
	}
}

func TestNewVersionStartsUnapproved(t *testing.T) {
	f, q, ep, project := setup(t)
	ctx := context.Background()
	v1 := f.video(t, q, ep, project, "video-v1", "qa_passed")
	if _, err := f.svc.Decide(ctx, v1.ID, v1.SHA256, review.Approved, "", "owner"); err != nil {
		t.Fatal(err)
	}
	v2 := f.video(t, q, ep, project, "video-v2", "qa_passed")
	if v2.Version != 2 {
		t.Fatalf("version %d", v2.Version)
	}
	if ap, _ := f.svc.Latest(ctx, v2.ID); ap != nil {
		t.Fatal("approval carried over to the new version")
	}
	if _, err := f.svc.Export(ctx, v2.ID, "owner"); !refused(err) {
		t.Fatalf("unapproved v2 exported: %v", err)
	}
	if _, err := f.svc.Decide(ctx, v2.ID, v1.SHA256, review.Approved, "", "owner"); !refused(err) {
		t.Fatalf("approval with the old version's hash accepted: %v", err)
	}
}

func TestApprovalRefusedWithoutQAOrWithTamperedFile(t *testing.T) {
	f, q, ep, project := setup(t)
	ctx := context.Background()
	failed := f.video(t, q, ep, project, "bad", "qa_failed")
	if _, err := f.svc.Decide(ctx, failed.ID, failed.SHA256, review.Approved, "", "owner"); !refused(err) {
		t.Fatalf("QA-failed version approved: %v", err)
	}
	if _, err := f.svc.Decide(ctx, failed.ID, failed.SHA256, review.Rejected, "", "owner"); !refused(err) {
		t.Fatalf("reject without notes accepted: %v", err)
	}
	ok := f.video(t, q, ep, project, "good", "qa_passed")
	if _, err := f.svc.Decide(ctx, ok.ID, ok.SHA256, review.Approved, "", "owner"); err != nil {
		t.Fatal(err)
	}
	path, _ := f.store.Abs(ok.StoragePath)
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Export(ctx, ok.ID, "owner"); !refused(err) {
		t.Fatalf("tampered file exported: %v", err)
	}
}
