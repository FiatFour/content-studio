package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

const token = "test-token-1234"

func server(t *testing.T) *httptest.Server {
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()}, AdminToken: token,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewServer(s.Echo())
	t.Cleanup(ts.Close)
	return ts
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

func TestMutatingEndpointsRequireTheToken(t *testing.T) {
	ts := server(t)
	for _, ep := range []struct{ method, path string }{
		{"POST", "/api/episodes"}, {"POST", "/api/episodes/1/rerender"}, {"POST", "/api/runs/1/cancel"}, {"POST", "/api/runs/1/retry"},
		{"POST", "/api/videos/1/decision"}, {"POST", "/api/videos/1/export"}, {"PUT", "/api/settings"},
	} {
		for _, auth := range []string{"", "wrong"} {
			if code, _ := call(t, ts, ep.method, ep.path, auth, "{}"); code != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: %d", ep.method, ep.path, auth, code)
			}
		}
	}
}

func TestCreateEpisodeValidatesAndQueuesARun(t *testing.T) {
	ts := server(t)
	if code, body := call(t, ts, "POST", "/api/episodes", token, `{"title":"x","idea":"short","target_duration_s":60}`); code != 400 {
		t.Fatalf("short idea accepted: %d %v", code, body)
	}
	if code, _ := call(t, ts, "POST", "/api/episodes", token, `{"title":"x","idea":"long enough idea here","target_duration_s":30}`); code != 400 {
		t.Fatal("30 s target accepted")
	}
	code, body := call(t, ts, "POST", "/api/episodes", token, `{"title":"ยายกับมือถือ","idea":"ยายหัดใช้สมาร์ทโฟนครั้งแรก","target_duration_s":90}`)
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
	code, body = call(t, ts, "POST", "/api/videos/999/decision", token, `{"decision":"approved","sha256":"x"}`)
	if code != 409 || strings.Contains(body["error"].(string), "/") {
		t.Fatalf("%d %v", code, body)
	}
}

func TestLanguageIsStoredInTheBackend(t *testing.T) {
	ts := server(t)
	if _, body := call(t, ts, "GET", "/api/settings", "", ""); body["ui_lang"] != "th" {
		t.Fatalf("default %v", body)
	}
	if code, _ := call(t, ts, "PUT", "/api/settings", token, `{"ui_lang":"fr"}`); code != 400 {
		t.Fatal("fr accepted")
	}
	call(t, ts, "PUT", "/api/settings", token, `{"ui_lang":"en"}`)
	if _, body := call(t, ts, "GET", "/api/settings", "", ""); body["ui_lang"] != "en" {
		t.Fatalf("after put %v", body)
	}
}
