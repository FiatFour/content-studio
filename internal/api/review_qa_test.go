package api_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
)

func TestReviewQAResultsCarryArtifactIDAndTime(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("qa-report-bytes"), "qa_failed")
	if _, err := db.Exec(context.Background(), `INSERT INTO qa_results (artifact_id, rule, rule_version, severity, passed, reason, suggested_fix)
		VALUES ($1, 'duration', 2, 'blocking', false, 'too long', 'shorten the script')`, v.ID); err != nil {
		t.Fatal(err)
	}
	code, body := call(t, ts, "GET", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/review", "", "")
	if code != http.StatusOK {
		t.Fatalf("review: %d %v", code, body)
	}
	qa, _ := body["qa"].([]any)
	if len(qa) != 1 {
		t.Fatalf("qa rows %v", body["qa"])
	}
	row, _ := qa[0].(map[string]any)
	if int64(row["artifact_id"].(float64)) != v.ID || row["rule_version"].(float64) != 2 || row["suggested_fix"] != "shorten the script" {
		t.Fatalf("qa row %v", row)
	}
	if at, _ := row["created_at"].(string); at == "" {
		t.Fatalf("qa row has no created_at: %v", row)
	}
}
