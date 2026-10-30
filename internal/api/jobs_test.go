package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

func TestJobDetailResumeFromRecordedData(t *testing.T) {
	ts, db := serverDB(t)
	ctx := context.Background()
	_, ep := storetest.Episode(t, db)
	var run int64
	if err := db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'x', 'full', 'failed') RETURNING id`, ep).Scan(&run); err != nil {
		t.Fatal(err)
	}
	ins := func(stage string, seq int, state string, class any, msg any) int64 {
		var id int64
		if err := db.QueryRow(ctx, `INSERT INTO jobs (run_id, stage, seq, state, error_class, error_message, attempt_count) VALUES ($1,$2,$3,$4,$5,$6,2) RETURNING id`,
			run, stage, seq, state, class, msg).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	script := ins("script", 0, "completed", nil, nil)
	tts := ins("tts", 1, "failed", "transient_network", "dial /home/fiat/secret/path/x.sock refused")
	get := func(id int64) map[string]any {
		code, body := call(t, ts, "GET", fmt.Sprintf("/api/jobs/%d", id), "", "")
		if code != 200 {
			t.Fatalf("%d %v", code, body)
		}
		return body
	}
	verdict := func(b map[string]any) string {
		r := b["resume"].(map[string]any)
		return r["verdict"].(string) + "/" + r["reason"].(string)
	}
	b := get(tts)
	if got := verdict(b); got != "unknown/no_execution_record" {
		t.Fatalf("completed upstream without an execution record must be unknown, got %s", got)
	}
	if b["error_class"] != "transient_network" || b["max_attempts"].(float64) != 3 || b["execution"] != nil {
		t.Fatalf("%v", b)
	}
	if msg := b["error_message"].(string); msg != "dial …/x.sock refused" {
		t.Fatalf("path not scrubbed: %q", msg)
	}
	if _, err := db.Exec(ctx, `INSERT INTO stage_executions (idempotency_key, run_id, stage, input_hash, job_id, attempt) VALUES ('k', $1, 'script', 'abcdef0123456789ff', $2, 1)`, run, script); err != nil {
		t.Fatal(err)
	}
	if got := verdict(get(tts)); got != "safe/upstream_recorded" {
		t.Fatalf("got %s", got)
	}
	if got := verdict(get(script)); got != "not_applicable/not_failed" {
		t.Fatalf("got %s", got)
	}
	ex := get(script)["execution"].(map[string]any)
	if ex["input_hash"] != "abcdef012345" {
		t.Fatalf("%v", ex)
	}
	unc := ins("images", 2, "failed", "uncertain_outcome", "maybe")
	if got := verdict(get(unc)); got != "unsafe/uncertain_outcome" {
		t.Fatalf("got %s", got)
	}
	if code, _ := call(t, ts, "GET", "/api/jobs/999999", "", ""); code != 404 {
		t.Fatalf("missing job: %d", code)
	}
	if code, _ := call(t, ts, "GET", "/api/jobs/abc", "", ""); code != 400 {
		t.Fatalf("bad id: %d", code)
	}
	raw, _ := json.Marshal(b)
	for _, banned := range []string{"lease_owner", "/home/fiat"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("response leaks %s", banned)
		}
	}
}

// Every error class needs a Thai and an English explanation, in the same dictionaries the UI uses.
func TestEveryErrorClassHasThaiAndEnglishExplanation(t *testing.T) {
	for _, lang := range []string{"th", "en"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "i18n", lang+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var dict map[string]string
		if err := json.Unmarshal(raw, &dict); err != nil {
			t.Fatal(err)
		}
		classes := jobs.AllErrorClasses()
		if len(classes) != 14 {
			t.Fatalf("expected 14 error classes, have %d: update the explanation table", len(classes))
		}
		for _, c := range classes {
			for _, part := range []string{"what", "next"} {
				k := fmt.Sprintf("errclass.%s.%s", c, part)
				if len(dict[k]) < 10 {
					t.Errorf("%s: missing or too short: %q", lang, k)
				}
			}
		}
	}
}
