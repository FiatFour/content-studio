package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// With Plans set, each run follows its own profile's stage list and runs its own profile's stage code,
// even though the stage names are shared.
func TestPlansRunEachProfileWithItsOwnStagesAndImplementations(t *testing.T) {
	db := storetest.New(t)
	_, epA := storetest.Episode(t, db) // fictional_series
	var pid, epB int64
	if err := db.QueryRow(context.Background(), `INSERT INTO projects (name, profile) VALUES ('b', 'other') RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), `INSERT INTO episodes (project_id, number, title, idea, target_duration_s) VALUES ($1, 1, 't', 'i', 60) RETURNING id`, pid).Scan(&epB); err != nil {
		t.Fatal(err)
	}
	plans := map[string]jobs.Plan{
		"fictional_series": {Profile: "fictional_series", Full: []string{"a", "b"}, Resources: map[string]string{"a": "gpu"}},
		"other":            {Profile: "other", Full: []string{"a", "c"}},
	}
	var ran []string
	mark := func(s string) jobs.Stage {
		return stageFunc(func(context.Context, *jobs.Job) error { ran = append(ran, s); return nil })
	}
	w := &jobs.Worker{Queue: &jobs.Queue{DB: db, Plans: plans, MaxAttempts: 3, Delay: noDelay},
		ProfileStages: map[string]map[string]jobs.Stage{
			"fictional_series": {"a": mark("fs.a"), "b": mark("fs.b")},
			"other":            {"a": mark("other.a"), "c": mark("other.c")},
		}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	runA, err := w.Queue.CreateRun(context.Background(), epA, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	runB, err := w.Queue.CreateRun(context.Background(), epB, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, w)
	if runState(t, db, runA) != jobs.RunReadyForReview || runState(t, db, runB) != jobs.RunReadyForReview {
		t.Fatal("both runs should be ready for review")
	}
	want := map[string]bool{"fs.a": true, "fs.b": true, "other.a": true, "other.c": true}
	if len(ran) != 4 {
		t.Fatalf("ran %v", ran)
	}
	for _, r := range ran {
		delete(want, r)
	}
	if len(want) != 0 {
		t.Fatalf("not run: %v (ran %v)", want, ran)
	}
	var res string
	if err := db.QueryRow(context.Background(), `SELECT coalesce(resource, '') FROM jobs WHERE run_id = $1 AND stage = 'a'`, runB).Scan(&res); err != nil {
		t.Fatal(err)
	}
	if res != "" {
		t.Fatalf("profile other's stage a took resource %q from another profile's plan", res)
	}
}
