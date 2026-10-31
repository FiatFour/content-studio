package pipeline_test

import (
	"context"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
)

// The QA loader must re-read the stored script and the exact source versions it was made from, and
// the History rules must react when a source gets a newer version or a still is marked as evidence.
func TestHistoryQALoadsStoredScriptAndSources(t *testing.T) {
	h := newHistoryHarness(t, 3, nil, historyJSON(t, 90, nil))
	h.addSource(t, "s1", "tower", historySource1)
	runID, state, reason := h.run(t)
	if state != string(jobs.RunReadyForReview) {
		t.Fatalf("run %s: %s", state, reason)
	}
	art, _, ok := h.storedScript(t, runID)
	if !ok {
		t.Fatal("no script")
	}
	stills := []map[string]any{{"image_kind": "illustration", "evidence": false}}
	rules := func() map[string]pipeline.QAResult {
		res, err := h.deps.HistoryRulesForTest(context.Background(), art, pipeline.Episode{TargetDurationS: 90}, 90, "แหล่งข้อมูล", stills)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]pipeline.QAResult{}
		for _, r := range res {
			m[r.Rule] = r
		}
		return m
	}
	got := rules()
	for _, rule := range []string{"history_sources_intact", "history_claims_quoted", "history_beats_supported", "history_sources_current", "history_sources_card", "history_stills_labelled"} {
		if !got[rule].Passed {
			t.Errorf("rule %s failed on a fresh stored script: %s", rule, got[rule].Reason)
		}
	}
	h.addSource(t, "s1", "tower v2", historySource1+" ใหม่")
	if r := rules()["history_sources_current"]; r.Passed || r.Severity != pipeline.Warning {
		t.Errorf("newer source version: %+v", r)
	}
	stills[0]["evidence"] = true
	if r := rules()["history_stills_labelled"]; r.Passed {
		t.Errorf("evidence still passed: %+v", r)
	}
}
