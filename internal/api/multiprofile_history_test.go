package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// M6 T7: a History & Mystery episode is created without a run, takes sources, is refused a run until it
// has one, and then starts under its own profile with the History stage list.
func TestMultiProfileServerStartsAHistoryEpisodeOnlyWithSources(t *testing.T) {
	ts, db := multiServer(t)
	for _, secs := range []int{59, 121} {
		code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("history_mystery", secs))
		if code != 400 || body["error"] != "target length must be 60 to 120 seconds" {
			t.Fatalf("%d s: %d %v", secs, code, body)
		}
	}
	code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("history_mystery", 90))
	if code != 201 || body["run_id"] != nil || body["episode_id"] == nil {
		t.Fatalf("create history: %d %v", code, body)
	}
	ep := int64(body["episode_id"].(float64))
	if n := count(t, db, `SELECT count(*) FROM workflow_runs`) + count(t, db, `SELECT count(*) FROM jobs`); n != 0 {
		t.Fatalf("%d runs and jobs exist before any run was started", n)
	}
	if n := count(t, db, `SELECT count(*) FROM projects WHERE profile = 'history_mystery'`); n != 1 {
		t.Fatalf("history projects = %d", n)
	}
	// No source: refused with a message that says what to do, and no job is created.
	code, body = call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/run", ep), "", "")
	msg, _ := body["error"].(string)
	if code != 409 || !strings.Contains(msg, "at least one source") {
		t.Fatalf("run without sources: %d %v", code, body)
	}
	if n := count(t, db, `SELECT count(*) FROM workflow_runs`); n != 0 {
		t.Fatal("a run was created without sources")
	}
	// With a source the run starts under its own profile and queues the History script stage first.
	if code, body := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/sources", ep), "", srcBody("Tower chronicle", "", "หอไอเฟลสร้างเสร็จในปี 1889")); code != 201 {
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
	if profile != "history_mystery" || kind != "full" || stage != "script" || resource != "gpu" {
		t.Fatalf("run %s %s first stage %s resource %q", profile, kind, stage, resource)
	}
	if code, _ := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/run", ep), "", ""); code != 409 {
		t.Fatalf("second run start: %d", code)
	}
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

// Sources stay refused for a profile that does not take them.
func TestMultiProfileServerRefusesSourcesOnAFictionalEpisode(t *testing.T) {
	ts, _ := multiServer(t)
	code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("", 60))
	if code != 201 {
		t.Fatalf("create fictional: %d %v", code, body)
	}
	ep := int64(body["episode_id"].(float64))
	if code, _ := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/sources", ep), "", srcBody("t", "", "text")); code != 409 {
		t.Fatalf("sources on a fictional episode: %d", code)
	}
}
