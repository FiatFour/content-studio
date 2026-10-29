package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func server(t *testing.T) *httptest.Server {
	ts, _ := serverDB(t)
	return ts
}

func serverDB(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Auth = authtest.Service(t, db)
	ts := httptest.NewServer(authtest.AutoLogin(s.Echo(), authtest.Session(t, s.Auth)))
	t.Cleanup(ts.Close)
	return ts, db
}

func call(t *testing.T, ts *httptest.Server, method, path, auth, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestCreateEpisodeValidatesAndQueuesARun(t *testing.T) {
	ts := server(t)
	if code, body := call(t, ts, "POST", "/api/episodes", "", `{"title":"x","idea":"short","target_duration_s":60}`); code != 400 {
		t.Fatalf("short idea accepted: %d %v", code, body)
	}
	if code, _ := call(t, ts, "POST", "/api/episodes", "", `{"title":"x","idea":"long enough idea here","target_duration_s":30}`); code != 400 {
		t.Fatal("30 s target accepted")
	}
	code, body := call(t, ts, "POST", "/api/episodes", "", `{"title":"ยายกับมือถือ","idea":"ยายหัดใช้สมาร์ทโฟนครั้งแรก","target_duration_s":90}`)
	if code != 201 {
		t.Fatalf("%d %v", code, body)
	}
	code, list := call(t, ts, "GET", "/api/episodes", "", "")
	eps := list["episodes"].([]any)
	run := eps[0].(map[string]any)["latest_run"].(map[string]any)
	if code != 200 || len(eps) != 1 || run["state"] != "running" {
		t.Fatalf("%d %v", code, list)
	}
	jobs := run["jobs"].([]any)
	if len(jobs) != 1 || jobs[0].(map[string]any)["state"] != "queued" {
		t.Fatalf("jobs %v", jobs)
	}
}

func TestErrorsAreSanitized(t *testing.T) {
	ts := server(t)
	code, body := call(t, ts, "GET", "/api/episodes/999", "", "")
	if code != 404 || body["error"] != "not found" {
		t.Fatalf("%d %v", code, body)
	}
	code, body = call(t, ts, "POST", "/api/videos/999/decision", "", `{"decision":"approved","sha256":"x"}`)
	if code != 409 || strings.Contains(body["error"].(string), "/") {
		t.Fatalf("%d %v", code, body)
	}
}

func TestLanguageIsStoredInTheBackend(t *testing.T) {
	ts := server(t)
	if _, body := call(t, ts, "GET", "/api/settings", "", ""); body["ui_lang"] != "th" {
		t.Fatalf("default %v", body)
	}
	if code, _ := call(t, ts, "PUT", "/api/settings", "", `{"ui_lang":"fr"}`); code != 400 {
		t.Fatal("fr accepted")
	}
	call(t, ts, "PUT", "/api/settings", "", `{"ui_lang":"en"}`)
	if _, body := call(t, ts, "GET", "/api/settings", "", ""); body["ui_lang"] != "en" {
		t.Fatalf("after put %v", body)
	}
}

func TestRerenderCanStartFromARewriteRun(t *testing.T) {
	ts, db := serverDB(t)
	_, ep := storetest.Episode(t, db)
	ctx := context.Background()
	// The original full run failed at the voice stage, so only the rewrite run reached subtitles.
	var full, rewrite int64
	if err := db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'fictional_series', 'full', 'failed') RETURNING id`, ep).Scan(&full); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, parent_run_id, state, feedback) VALUES ($1, 'fictional_series', 'rewrite', $2, 'ready_for_review', 'x') RETURNING id`, ep, full).Scan(&rewrite); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state) VALUES ($1, 'subtitles', 3, 'completed')`, rewrite); err != nil {
		t.Fatal(err)
	}
	code, body := call(t, ts, "POST", "/api/episodes/"+strconv.FormatInt(ep, 10)+"/rerender", "", "{}")
	if code != http.StatusCreated || body["run_id"] == nil {
		t.Fatalf("%d %v", code, body)
	}
	var parent int64
	if err := db.QueryRow(ctx, `SELECT parent_run_id FROM workflow_runs WHERE id = $1`, int64(body["run_id"].(float64))).Scan(&parent); err != nil || parent != rewrite {
		t.Fatalf("re-render parent %d (err %v), want the rewrite run %d", parent, err, rewrite)
	}
}

func TestRunActionsReportConflictAndNotFoundDistinctly(t *testing.T) {
	ts, db := serverDB(t)
	_, ep := storetest.Episode(t, db)
	var run int64
	if err := db.QueryRow(context.Background(), `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'fictional_series', 'full', 'ready_for_review') RETURNING id`, ep).Scan(&run); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(run, 10)
	if code, body := call(t, ts, "POST", "/api/runs/"+id+"/retry", "", "{}"); code != http.StatusConflict || !strings.Contains(body["error"].(string), "only failed runs can be retried") {
		t.Fatalf("retry of a finished run: %d %v", code, body)
	}
	if code, body := call(t, ts, "POST", "/api/runs/"+id+"/cancel", "", "{}"); code != http.StatusConflict || !strings.Contains(body["error"].(string), "not running") {
		t.Fatalf("cancel of a finished run: %d %v", code, body)
	}
	if code, _ := call(t, ts, "POST", "/api/runs/999999/retry", "", "{}"); code != http.StatusNotFound {
		t.Fatalf("retry of a missing run: %d, want 404", code)
	}
}
