package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/store/storetest"
)

func auditActions(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT action FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func wantActions(t *testing.T, db *pgxpool.Pool, want ...string) {
	t.Helper()
	got := auditActions(t, db)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit actions %v, want %v", got, want)
	}
}

func TestAuditRowsForEveryAuditedAction(t *testing.T) {
	ts, db, st := reviewServer(t)
	ctx := context.Background()

	// Refused and invalid requests write nothing.
	call(t, ts, "PUT", "/api/settings", "", `{"ui_lang":"xx"}`)
	call(t, ts, "POST", "/api/runs/999999/cancel", "", "{}")
	wantActions(t, db)

	if code, body := call(t, ts, "PUT", "/api/settings", "", `{"ui_lang":"en"}`); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	code, body := call(t, ts, "POST", "/api/characters", "", mia)
	if code != 201 {
		t.Fatalf("%d %v", code, body)
	}
	charID := int64(body["id"].(float64))
	upd := `{"name":"Mia Lee","appearance":"short black hair, green hoodie","voice":"bright, fast talker","status":"alive"}`
	if code, body := call(t, ts, "PUT", fmt.Sprintf("/api/characters/%d", charID), "", upd); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	// Duplicate slug and bad update roll back with no audit row.
	call(t, ts, "POST", "/api/characters", "", mia)
	call(t, ts, "PUT", fmt.Sprintf("/api/characters/%d", charID), "", `{"name":""}`)

	code, body = call(t, ts, "POST", "/api/episodes", "", `{"title":"Ep","idea":"a long enough idea","target_duration_s":60}`)
	if code != 201 {
		t.Fatalf("%d %v", code, body)
	}
	ep := int64(body["episode_id"].(float64))
	run := strconv.FormatInt(int64(body["run_id"].(float64)), 10)
	epPath := "/api/episodes/" + strconv.FormatInt(ep, 10)
	if code, body := call(t, ts, "PUT", epPath+"/summary", "", `{"summary":"Mia moves in.","characters_present":["mia"]}`); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := call(t, ts, "PUT", epPath+"/in-story-date", "", `{"in_story_date":"2025-03-31"}`); code != 200 {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := call(t, ts, "POST", "/api/runs/"+run+"/cancel", "", "{}"); code != 200 {
		t.Fatalf("cancel %d %v", code, body)
	}

	// A failed run to retry.
	_, ep2 := storetest.Episode(t, db)
	var failedRun int64
	if err := db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'fictional_series', 'full', 'failed') RETURNING id`, ep2).Scan(&failedRun); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state, error_class, error_message) VALUES ($1, 'script', 0, 'failed', 'internal', 'boom')`, failedRun); err != nil {
		t.Fatal(err)
	}
	if code, body := call(t, ts, "POST", "/api/runs/"+strconv.FormatInt(failedRun, 10)+"/retry", "", "{}"); code != 200 {
		t.Fatalf("retry %d %v", code, body)
	}

	// Review: reject (notes required), approve, export.
	v := seedVideo(t, db, st, []byte("fake-mp4-audit"), "qa_passed")
	vid := strconv.FormatInt(v.ID, 10)
	call(t, ts, "POST", "/api/videos/"+vid+"/decision", "", `{"decision":"rejected","sha256":"`+v.SHA256+`"}`) // refused: no notes
	if code, body := call(t, ts, "POST", "/api/videos/"+vid+"/decision", "", `{"decision":"rejected","notes":"too short","sha256":"`+v.SHA256+`"}`); code != 201 {
		t.Fatalf("reject %d %v", code, body)
	}
	if code, body := call(t, ts, "POST", "/api/videos/"+vid+"/decision", "", `{"decision":"approved","sha256":"`+v.SHA256+`"}`); code != 201 {
		t.Fatalf("approve %d %v", code, body)
	}
	if code, body := call(t, ts, "POST", "/api/videos/"+vid+"/export", "", "{}"); code != 201 {
		t.Fatalf("export %d %v", code, body)
	}
	call(t, ts, "POST", "/api/videos/"+vid+"/export", "", "{}") // refused: already exported, no row

	wantActions(t, db, "settings.update", "canon.character.create", "canon.character.update", "canon.summary.put", "canon.in_story_date.put",
		"job.cancel", "job.retry", "review.rejected", "review.approved", "export.create")

	// Every row has the owner as actor and no absolute path or request body in detail.
	rows, err := db.Query(ctx, `SELECT actor, detail::text FROM audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var actor, detail string
		if err := rows.Scan(&actor, &detail); err != nil {
			t.Fatal(err)
		}
		if actor != "owner" || strings.Contains(detail, "/tmp") || strings.Contains(detail, "storage_path") || strings.Contains(detail, "export_path") {
			t.Fatalf("bad audit row: actor %q detail %s", actor, detail)
		}
	}
}

func TestAuditListIsNewestFirstAndPaginated(t *testing.T) {
	ts, db := serverDB(t)
	for _, l := range []string{"en", "th", "en"} {
		if code, body := call(t, ts, "PUT", "/api/settings", "", `{"ui_lang":"`+l+`"}`); code != 200 {
			t.Fatalf("%d %v", code, body)
		}
	}
	code, body := call(t, ts, "GET", "/api/audit?limit=2", "", "")
	entries, _ := body["entries"].([]any)
	if code != 200 || len(entries) != 2 {
		t.Fatalf("%d %v", code, body)
	}
	first, second := entries[0].(map[string]any), entries[1].(map[string]any)
	if first["id"].(float64) < second["id"].(float64) || first["action"] != "settings.update" || first["actor"] != "owner" {
		t.Fatalf("not newest first: %v", entries)
	}
	if first["detail"].(map[string]any)["ui_lang"] != "en" {
		t.Fatalf("detail %v", first["detail"])
	}
	_, page2 := call(t, ts, "GET", fmt.Sprintf("/api/audit?limit=2&before=%d", int64(second["id"].(float64))), "", "")
	if got := page2["entries"].([]any); len(got) != 1 {
		t.Fatalf("page 2 %v, want 1 older row", got)
	}
	for _, q := range []string{"limit=0", "limit=201", "limit=x", "before=0"} {
		if code, _ := call(t, ts, "GET", "/api/audit?"+q, "", ""); code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", q, code)
		}
	}
	// Read-only: no write verbs on /audit.
	if code, _ := call(t, ts, "DELETE", "/api/audit", "", ""); code == 200 || code == 204 {
		t.Fatalf("DELETE /api/audit: %d", code)
	}
	_ = db
}
