package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/auth/authtest"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// multiServer is the production wiring: one queue with the plan of every runnable profile.
func multiServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plans: pipeline.RunnablePlans(), MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Auth = authtest.Service(t, db)
	ts := httptest.NewServer(authtest.AutoLogin(s.Echo(), authtest.Session(t, s.Auth)))
	t.Cleanup(ts.Close)
	return ts, db
}

func count(t *testing.T, db *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMultiProfileServerCreatesAShortsEpisodeWithoutARun(t *testing.T) {
	ts, db := multiServer(t)
	for _, secs := range []int{29, 61} {
		code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("knowledge_shorts", secs))
		if code != 400 || body["error"] != "target length must be 30 to 60 seconds" {
			t.Fatalf("%d s: %d %v", secs, code, body)
		}
	}
	if code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("no_such_profile", 45)); code != 400 || body["error"] != "unknown profile" {
		t.Fatalf("unknown profile: %d %v", code, body)
	}
	code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("knowledge_shorts", 45))
	if code != 201 || body["run_id"] != nil || body["episode_id"] == nil {
		t.Fatalf("create shorts: %d %v", code, body)
	}
	ep := int64(body["episode_id"].(float64))
	// Nothing is queued: a Shorts episode waits for its sources.
	if n := count(t, db, `SELECT count(*) FROM workflow_runs`) + count(t, db, `SELECT count(*) FROM jobs`); n != 0 {
		t.Fatalf("%d runs and jobs exist before any run was started", n)
	}
	// Starting it with no source is refused with a message that says what to do, and creates no job.
	code, body = call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/run", ep), "", "")
	msg, _ := body["error"].(string)
	if code != 409 || !strings.Contains(msg, "at least one source") {
		t.Fatalf("run without sources: %d %v", code, body)
	}
	if n := count(t, db, `SELECT count(*) FROM workflow_runs`); n != 0 {
		t.Fatalf("a run was created without sources")
	}
	// With a source, the run starts under its own profile and queues the Shorts script stage first.
	if code, body := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/sources", ep), "", srcBody("Sea facts", "", "น้ำทะเลมีความเค็ม")); code != 201 {
		t.Fatalf("add source: %d %v", code, body)
	}
	code, body = call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/run", ep), "", "")
	if code != 201 || body["run_id"] == nil {
		t.Fatalf("run with a source: %d %v", code, body)
	}
	run := int64(body["run_id"].(float64))
	var profile, kind, stage, resource string
	if err := db.QueryRow(context.Background(), `SELECT r.profile, r.kind, j.stage, coalesce(j.resource, '') FROM workflow_runs r JOIN jobs j ON j.run_id = r.id WHERE r.id = $1`, run).
		Scan(&profile, &kind, &stage, &resource); err != nil {
		t.Fatal(err)
	}
	if profile != "knowledge_shorts" || kind != "full" || stage != "script" || resource != "gpu" {
		t.Fatalf("run %s %s first stage %s resource %q", profile, kind, stage, resource)
	}
	// A second start is refused: retry or re-render is the path from here.
	if code, _ := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/run", ep), "", ""); code != 409 {
		t.Fatalf("second run start: %d", code)
	}
	// The episode page lists the Shorts stages for this run.
	code, got := call(t, ts, "GET", fmt.Sprintf("/api/episodes/%d", ep), "", "")
	runs, _ := got["runs"].([]any)
	if code != 200 || len(runs) != 1 {
		t.Fatalf("get episode: %d %v", code, got)
	}
	stages, _ := runs[0].(map[string]any)["stages"].([]any)
	if fmt.Sprint(stages) != "[script tts images subtitles render qa]" {
		t.Fatalf("stages %v", stages)
	}
}

func TestMultiProfileServerKeepsFictionalSeriesBehaviour(t *testing.T) {
	ts, db := multiServer(t)
	code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("", 60))
	if code != 201 || body["run_id"] == nil {
		t.Fatalf("create fictional: %d %v", code, body)
	}
	run := int64(body["run_id"].(float64))
	var profile string
	if err := db.QueryRow(context.Background(), `SELECT profile FROM workflow_runs WHERE id = $1`, run).Scan(&profile); err != nil || profile != "fictional_series" {
		t.Fatalf("profile %q %v", profile, err)
	}
	if code, _ := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/run", int64(body["episode_id"].(float64))), "", ""); code != 409 {
		t.Fatalf("starting a second run on a Fictional Series episode: %d", code)
	}
	if code, _ := call(t, ts, "POST", "/api/episodes", "", episodeBody("fictional_series", 45)); code != 400 {
		t.Fatalf("45 s accepted for fictional_series: %d", code)
	}
}

func TestMultiProfileServerRerendersAShortsEpisode(t *testing.T) {
	ts, db := multiServer(t)
	ctx := context.Background()
	ep := ksEpisode(t, db)
	var run int64
	if err := db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'knowledge_shorts', 'full', 'ready_for_review') RETURNING id`, ep).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state) VALUES ($1, 'subtitles', 3, 'completed')`, run); err != nil {
		t.Fatal(err)
	}
	code, body := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/rerender", ep), "", "")
	if code != 201 {
		t.Fatalf("rerender: %d %v", code, body)
	}
	re := int64(body["run_id"].(float64))
	var profile, kind, stage string
	if err := db.QueryRow(ctx, `SELECT r.profile, r.kind, j.stage FROM workflow_runs r JOIN jobs j ON j.run_id = r.id WHERE r.id = $1`, re).Scan(&profile, &kind, &stage); err != nil {
		t.Fatal(err)
	}
	if profile != "knowledge_shorts" || kind != "rerender" || stage != "render" {
		t.Fatalf("rerender run %s %s first stage %s", profile, kind, stage)
	}
}

func TestMultiProfileTowerListsTheShortsStagesOfAShortsRun(t *testing.T) {
	ts, db := multiServer(t)
	ep := ksEpisode(t, db)
	if code, body := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/sources", ep), "", srcBody("Sea facts", "", "น้ำทะเลมีความเค็ม")); code != 201 {
		t.Fatalf("add source: %d %v", code, body)
	}
	if code, body := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/run", ep), "", ""); code != 201 {
		t.Fatalf("start: %d %v", code, body)
	}
	if _, err := db.Exec(context.Background(), `UPDATE jobs SET state = 'running', started_at = now()`); err != nil {
		t.Fatal(err)
	}
	b, _ := fetchTower(t, ts.URL)
	if b.Tower.ActiveEpisode == nil {
		t.Fatal("no active episode on the tower")
	}
	var names []string
	for _, p := range b.Tower.ActiveEpisode.Pipeline {
		names = append(names, p.Stage)
	}
	if got := strings.Join(names, ","); got != "script,tts,images,subtitles,render,qa,review,export" {
		t.Fatalf("tower pipeline %s", got)
	}
}
