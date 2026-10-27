package artifacts_test

import (
	"context"
	"io"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

func TestWriteHashesAndNeverOverwrites(t *testing.T) {
	s := &artifacts.Store{Root: t.TempDir()}
	loc := artifacts.Loc{EpisodeID: 1, RunID: 2, JobID: 3, Attempt: 1}
	write := func(w io.Writer) error { _, err := io.WriteString(w, "hello"); return err }
	f, err := s.Write(loc, artifacts.Script, "", write)
	if err != nil {
		t.Fatal(err)
	}
	if f.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" || f.Size != 5 {
		t.Fatalf("got %+v", f)
	}
	if got, _ := s.Hash(f.RelPath); got != f.SHA256 {
		t.Fatalf("rehash %s", got)
	}
	if _, err := s.Write(loc, artifacts.Script, "", write); err == nil {
		t.Fatal("second write to the same path must fail")
	}
	loc.Attempt = 2
	if _, err := s.Write(loc, artifacts.Script, "", write); err != nil {
		t.Fatalf("a new attempt gets its own directory: %v", err)
	}
}

func TestPathsStayInsideRoot(t *testing.T) {
	s := &artifacts.Store{Root: t.TempDir()}
	if _, err := s.Abs("../../etc/passwd"); err == nil {
		t.Fatal("escaping path accepted")
	}
	if _, err := s.Write(artifacts.Loc{}, artifacts.Image, "../x", func(io.Writer) error { return nil }); err == nil {
		t.Fatal("unsafe item key accepted")
	}
}

func TestVersionsCountPerEpisodeTypeAndItem(t *testing.T) {
	db := storetest.New(t)
	project, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: jobs.Plan{Full: []string{"a"}}, MaxAttempts: 1}
	ctx := context.Background()
	var versions []int
	for range 2 {
		runID, err := q.CreateRun(ctx, ep, "full", nil)
		if err != nil {
			t.Fatal(err)
		}
		j, err := q.Claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = q.Complete(ctx, j, func(tx pgx.Tx) error {
			a := &artifacts.Artifact{ProjectID: project, EpisodeID: ep, RunID: runID, JobID: j.ID, Type: artifacts.Video,
				StoragePath: "x", SHA256: "y"}
			if err := artifacts.Insert(ctx, tx, a); err != nil {
				return err
			}
			versions = append(versions, a.Version)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("versions %v", versions)
	}
}
