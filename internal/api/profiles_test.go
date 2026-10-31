package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/store"
	"github.com/fiat/content-studio/internal/store/storetest"
)

func episodeBody(profile string, secs int) string {
	p := ""
	if profile != "" {
		p = fmt.Sprintf(`"profile":%q,`, profile)
	}
	return fmt.Sprintf(`{%s"title":"ep","idea":"long enough idea here","target_duration_s":%d}`, p, secs)
}

func TestCreateEpisodeDefaultsToFictionalSeries(t *testing.T) {
	ts, db := serverDB(t)
	for _, profile := range []string{"", "fictional_series"} {
		if code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody(profile, 60)); code != 201 {
			t.Fatalf("profile %q: %d %v", profile, code, body)
		}
	}
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM projects`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("projects = %d, %v; want the single fictional_series project", n, err)
	}
	_, list := call(t, ts, "GET", "/api/episodes", "", "")
	for _, e := range list["episodes"].([]any) {
		if p := e.(map[string]any)["profile"]; p != "fictional_series" {
			t.Fatalf("episode profile = %v", p)
		}
	}
	_, detail := call(t, ts, "GET", "/api/episodes/1", "", "")
	if p := detail["episode"].(map[string]any)["profile"]; p != "fictional_series" {
		t.Fatalf("detail profile = %v", p)
	}
}

func TestFictionalSeriesDurationMessageUnchanged(t *testing.T) {
	ts := server(t)
	for _, secs := range []int{59, 121} {
		code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("fictional_series", secs))
		if code != 400 || body["error"] != "target length must be 60 to 120 seconds" {
			t.Fatalf("%d s: %d %v", secs, code, body)
		}
	}
}

func TestUnknownProfileIsRejected(t *testing.T) {
	ts, db := serverDB(t)
	code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("nope", 60))
	if code != 400 {
		t.Fatalf("%d %v", code, body)
	}
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM episodes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("episodes = %d, %v", n, err)
	}
}

// T7 edit: this is the single-plan queue (Plan: FictionalSeries) every older test builds. It still has no
// plan for knowledge_shorts, so it must refuse it with 501 and write nothing; the multi-profile server
// that creates a Shorts episode is covered by TestMultiProfileServer* in multiprofile_test.go.
func TestSinglePlanQueueRefusesAProfileItHasNoPlanFor(t *testing.T) {
	ts, db := serverDB(t)
	for _, secs := range []int{29, 61} {
		code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("knowledge_shorts", secs))
		if code != 400 || body["error"] != "target length must be 30 to 60 seconds" {
			t.Fatalf("%d s: %d %v", secs, code, body)
		}
	}
	for _, secs := range []int{30, 45, 60} {
		code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("knowledge_shorts", secs))
		msg, _ := body["error"].(string)
		if code != 501 || !strings.HasPrefix(msg, "Not implemented") {
			t.Fatalf("%d s: %d %v", secs, code, body)
		}
	}
	// Nothing was written: no project, episode or run.
	for _, table := range []string{"projects", "episodes", "workflow_runs"} {
		var n int
		if err := db.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows = %d, %v", table, n, err)
		}
	}
	// A fictional_series episode still cannot be given a 45 s length.
	if code, _ := call(t, ts, "POST", "/api/episodes", "", episodeBody("fictional_series", 45)); code != 400 {
		t.Fatalf("45 s accepted for fictional_series: %d", code)
	}
}

func TestHistoryMysteryIsRefusedAsNotImplemented(t *testing.T) {
	ts, db := serverDB(t)
	for _, secs := range []int{59, 121} {
		code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("history_mystery", secs))
		if code != 400 || body["error"] != "target length must be 60 to 120 seconds" {
			t.Fatalf("%d s: %d %v", secs, code, body)
		}
	}
	for _, secs := range []int{60, 90, 120} {
		code, body := call(t, ts, "POST", "/api/episodes", "", episodeBody("history_mystery", secs))
		msg, _ := body["error"].(string)
		if code != 501 || !strings.HasPrefix(msg, "Not implemented") {
			t.Fatalf("%d s: %d %v", secs, code, body)
		}
	}
	for _, table := range []string{"projects", "episodes", "workflow_runs"} {
		var n int
		if err := db.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows = %d, %v", table, n, err)
		}
	}
}

func TestCanonEndpointsStayFictionalSeriesOnly(t *testing.T) {
	ts, db := serverDB(t)
	// Even with a knowledge_shorts project present, characters live in the fictional_series one.
	if _, err := db.Exec(context.Background(), `INSERT INTO projects (name, profile) VALUES ('Knowledge Shorts', 'knowledge_shorts')`); err != nil {
		t.Fatal(err)
	}
	if code, body := call(t, ts, "POST", "/api/characters", "", `{"slug":"mali","name":"Mali","appearance":"tall, black hair","personality":"curious and kind","voice":"warm, low","status":"alive"}`); code != 201 {
		t.Fatalf("%d %v", code, body)
	}
	code, body := call(t, ts, "GET", "/api/characters", "", "")
	if code != 200 || len(body["characters"].([]any)) != 1 {
		t.Fatalf("%d %v", code, body)
	}
	var profile string
	if err := db.QueryRow(context.Background(), `SELECT p.profile FROM characters c JOIN projects p ON p.id = c.project_id`).Scan(&profile); err != nil || profile != "fictional_series" {
		t.Fatalf("character project profile = %q, %v", profile, err)
	}
}

func TestDurationCheckIsTheUnionOfProfileRanges(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	var pid int64
	if err := db.QueryRow(ctx, `INSERT INTO projects (name, profile) VALUES ('p', 'knowledge_shorts') RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	for i, secs := range []int{30, 60, 120} {
		if _, err := db.Exec(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s) VALUES ($1, $2, 't', 'i', $3)`, pid, i+1, secs); err != nil {
			t.Fatalf("%d s refused: %v", secs, err)
		}
	}
	for i, secs := range []int{29, 121} {
		if _, err := db.Exec(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s) VALUES ($1, $2, 't', 'i', $3)`, pid, i+10, secs); err == nil {
			t.Fatalf("%d s accepted by the database", secs)
		}
	}
}

// Existing rows (60-120) survive the migration untouched. The test puts the old CHECK back, adds rows,
// forgets that 0018 ran, and migrates again.
func TestMigration0018KeepsExistingEpisodes(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	for _, q := range []string{
		`ALTER TABLE episodes DROP CONSTRAINT episodes_target_duration_s_check`,
		`ALTER TABLE episodes ADD CONSTRAINT episodes_target_duration_s_check CHECK (target_duration_s BETWEEN 60 AND 120)`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_, ep := storetest.Episode(t, db) // 60 s
	if _, err := db.Exec(ctx, `UPDATE episodes SET target_duration_s = 120 WHERE id = $1`, ep); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s) SELECT project_id, 2, 't', 'i', 90 FROM episodes WHERE id = $1`, ep); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM schema_migrations WHERE name = '0018_episode_duration_range.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var n, sum int
	if err := db.QueryRow(ctx, `SELECT count(*), sum(target_duration_s) FROM episodes`).Scan(&n, &sum); err != nil || n != 2 || sum != 210 {
		t.Fatalf("rows = %d sum = %d, %v", n, sum, err)
	}
	if _, err := db.Exec(ctx, `UPDATE episodes SET target_duration_s = 30 WHERE id = $1`, ep); err != nil {
		t.Fatalf("30 s refused after migration: %v", err)
	}
}
