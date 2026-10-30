package pipeline_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/store/storetest"
)

type shortsQARow struct {
	rule    string
	version int
	passed  bool
	reason  string
}

func (m *shortsMedia) qaRows(t *testing.T, runID int64) (string, []shortsQARow) {
	t.Helper()
	ctx := context.Background()
	videos, err := artifacts.ForRun(ctx, m.db, runID, artifacts.Video)
	if err != nil || len(videos) != 1 {
		t.Fatalf("videos %v %v", videos, err)
	}
	rows, err := m.db.Query(ctx, `SELECT rule, rule_version, passed, reason FROM qa_results WHERE artifact_id = $1 ORDER BY id`, videos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []shortsQARow
	for rows.Next() {
		var r shortsQARow
		if err := rows.Scan(&r.rule, &r.version, &r.passed, &r.reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	var model string
	if err := m.db.QueryRow(ctx, `SELECT coalesce(model, '') FROM jobs WHERE run_id = $1 AND stage = 'qa' ORDER BY id DESC LIMIT 1`, runID).Scan(&model); err != nil {
		t.Fatal(err)
	}
	return model, out
}

func TestKnowledgeShortsQAPassesAndRecordsShortsRules(t *testing.T) {
	m := newShortsMedia(t, shortsJSON(t, 45, nil))
	m.addSource(t, "s1", "Sea water facts", shortsSource1)
	runID, err := m.worker.Queue.CreateRun(context.Background(), m.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	m.drain(t)
	model, rows := m.qaRows(t, runID)
	if model != "qa-rules-shorts-v1" {
		t.Fatalf("qa job model %q", model)
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[r.rule] = true
		if !r.passed && !strings.HasPrefix(r.rule, "loudness") && r.rule != "black_frames" && r.rule != "long_silence" {
			t.Errorf("rule %s failed: %s", r.rule, r.reason)
		}
	}
	for _, want := range []string{"duration", "file_exists", "shorts_duration_range", "shorts_sources_intact", "shorts_claims_quoted", "shorts_beats_supported", "shorts_sources_card", "shorts_sources_current"} {
		if !have[want] {
			t.Errorf("no qa row for %s (have %v)", want, rows)
		}
	}
	var status string
	if err := m.db.QueryRow(context.Background(), `SELECT validation_status FROM artifacts WHERE run_id = $1 AND type = 'video'`, runID).Scan(&status); err != nil || status != "qa_passed" {
		t.Fatalf("video status %q (%v)", status, err)
	}
}

// A source changed (or lost) after the script was written must fail QA: the claims no longer rest on the text.
func TestKnowledgeShortsQAFailsWhenASourceChangedAfterTheScript(t *testing.T) {
	for name, damage := range map[string]func(path string) error{
		"file rewritten": func(p string) error {
			return os.WriteFile(p, []byte("ข้อความอื่นทั้งหมด"), 0o644)
		},
		"file removed": os.Remove,
	} {
		t.Run(name, func(t *testing.T) {
			m := newShortsMedia(t, shortsJSON(t, 45, nil))
			storetest.AllowArtifactTampering(t, m.db)
			srcID := m.addSource(t, "s1", "Sea water facts", shortsSource1)
			ctx := context.Background()
			m.worker.Stages["qa"] = beforeStage{real: m.worker.Stages["qa"], hook: func() {
				var rel string
				if err := m.db.QueryRow(ctx, `SELECT storage_path FROM artifacts WHERE id = $1`, srcID).Scan(&rel); err != nil {
					t.Error(err)
					return
				}
				p, _ := m.deps.Store.Abs(rel)
				if err := damage(p); err != nil {
					t.Error(err)
				}
			}}
			runID, err := m.worker.Queue.CreateRun(ctx, m.ep, "full", nil)
			if err != nil {
				t.Fatal(err)
			}
			m.drain(t)
			var state, class string
			if err := m.db.QueryRow(ctx, `SELECT state, coalesce((SELECT error_class FROM jobs WHERE run_id = r.id AND stage = 'qa' ORDER BY id DESC LIMIT 1), '') FROM workflow_runs r WHERE id = $1`, runID).Scan(&state, &class); err != nil {
				t.Fatal(err)
			}
			if state != string(jobs.RunFailed) || class != string(jobs.ErrQAFailed) {
				t.Fatalf("run %s class %s, want failed qa_failed", state, class)
			}
			_, rows := m.qaRows(t, runID)
			failed := map[string]string{}
			for _, r := range rows {
				if !r.passed {
					failed[r.rule] = r.reason
				}
			}
			if failed["shorts_sources_intact"] == "" || failed["shorts_claims_quoted"] == "" {
				t.Fatalf("failed rules %v, want shorts_sources_intact and shorts_claims_quoted", failed)
			}
			var status string
			if err := m.db.QueryRow(ctx, `SELECT validation_status FROM artifacts WHERE run_id = $1 AND type = 'video'`, runID).Scan(&status); err != nil || status != "qa_failed" {
				t.Fatalf("video status %q (%v)", status, err)
			}
		})
	}
}

// A newer source version (a new row) does not fail QA, it warns: the script is still checked against
// the versions it was made from.
func TestKnowledgeShortsQAWarnsOnNewerSourceVersion(t *testing.T) {
	m := newShortsMedia(t, shortsJSON(t, 45, nil))
	m.addSource(t, "s1", "Sea water facts", shortsSource1)
	m.worker.Stages["qa"] = beforeStage{real: m.worker.Stages["qa"], hook: func() { m.addSource(t, "s1", "Sea water facts", "ข้อความใหม่") }}
	runID, err := m.worker.Queue.CreateRun(context.Background(), m.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	m.drain(t)
	_, rows := m.qaRows(t, runID)
	for _, r := range rows {
		switch r.rule {
		case "shorts_sources_current":
			if r.passed {
				t.Error("shorts_sources_current should warn")
			}
		case "shorts_claims_quoted", "shorts_sources_intact":
			if !r.passed {
				t.Errorf("%s failed: %s", r.rule, r.reason)
			}
		}
	}
}

// The narration budget is the target minus card and gap time, in the validator and in the prompt.
func TestShortsNarrationLengthUsesTheBudgetAfterCardsAndGaps(t *testing.T) {
	// 700 characters in 8 beats was accepted for 45 s when the whole target was budgeted (max 810);
	// the narration really has only 32.2 s, so the most it can hold is 579.
	long := shortsJSON(t, 45*700/540, nil)
	if _, err := pipeline.ValidateShortsScript([]byte(long), 45); err == nil || !strings.Contains(err.Error(), "allows at most 579") {
		t.Fatalf("700 characters must be rejected with the 579 limit, got %v", err)
	}
	if _, err := pipeline.ValidateShortsScript([]byte(shortsJSON(t, 45, nil)), 45); err != nil {
		t.Fatalf("540 characters must still fit: %v", err)
	}
	// Too short against the same budget: 32.2 s * 9 = 289 characters minimum.
	if _, err := pipeline.ValidateShortsScript([]byte(shortsJSON(t, 20, nil)), 45); err == nil || !strings.Contains(err.Error(), "at least 289") {
		t.Fatalf("240 characters must be rejected with the 289 minimum, got %v", err)
	}

	h := newShortsHarness(t, 1, shortsJSON(t, 45, nil))
	rec := &recordingLLM{LLM: h.llm}
	h = rewire(t, h, rec)
	h.addSource(t, "s1", "sea", shortsSource1)
	h.run(t)
	if len(rec.prompts) != 1 {
		t.Fatalf("prompts %d", len(rec.prompts))
	}
	// 45 s with 8 beats: 12 s of cards and gaps, 32.2 s of speech -> 322 to 450 characters (10 to 14 per second).
	if !strings.Contains(rec.prompts[0], "about 322 to 450 Thai characters") || !strings.Contains(rec.prompts[0], "about 12 seconds of the 45") {
		t.Fatalf("prompt does not carry the reduced budget:\n%s", rec.prompts[0][:min(700, len(rec.prompts[0]))])
	}
}
