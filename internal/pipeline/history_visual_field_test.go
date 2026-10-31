package pipeline_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/pipeline"
)

// M6 T7: every beat carries an abstract "visual", schema-validated and cleaned with CleanHistoryVisual.
func TestValidateHistoryScriptVisualField(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
		want string // "" = valid
	}{
		{name: "valid", edit: nil},
		{name: "missing", edit: func(d map[string]any) { delete(d["beats"].([]any)[2].(map[string]any), "visual") }, want: "schema"},
		{name: "too short", edit: func(d map[string]any) { d["beats"].([]any)[0].(map[string]any)["visual"] = "sea" }, want: "schema"},
		{name: "too long", edit: func(d map[string]any) { d["beats"].([]any)[0].(map[string]any)["visual"] = strings.Repeat("a", 201) }, want: "schema"},
		{name: "unknown beat key", edit: func(d map[string]any) { d["beats"].([]any)[0].(map[string]any)["image"] = "x" }, want: "schema"},
		{name: "evidence wording", edit: func(d map[string]any) {
			d["beats"].([]any)[3].(map[string]any)["visual"] = "a photo of the old harbour"
		}, want: "beat 4: visual description uses evidence-style wording"},
		{name: "franchise", edit: func(d map[string]any) {
			d["beats"].([]any)[1].(map[string]any)["visual"] = "a ship from Futurama sailing on the waves"
		}, want: "beat 2: visual description names an existing franchise"},
		{name: "repeats narration", edit: func(d map[string]any) {
			b := d["beats"].([]any)[0].(map[string]any)
			b["visual"] = b["narration"]
		}, want: "beat 1: visual description repeats narration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := historyDoc(t, 90, nil)
			if tc.edit != nil {
				tc.edit(doc)
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			got, err := pipeline.ValidateHistoryScript(raw, 90)
			if tc.want == "" {
				if err != nil || got == nil || got.Beats[0].Visual == "" {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "Futurama") || strings.Contains(err.Error(), "old harbour") {
				t.Fatalf("the error repeats the offending text: %v", err)
			}
		})
	}
}

// The prompt asks for the visual and forbids real names and photo wording.
func TestHistoryPromptAsksForAbstractVisuals(t *testing.T) {
	var rec *recordingLLM
	h := newHistoryHarness(t, 1, recordHistory(&rec), historyJSON(t, 90, nil))
	h.addSource(t, "s1", "tower", historySource1)
	h.run(t)
	if len(rec.prompts) != 1 {
		t.Fatalf("prompts %d", len(rec.prompts))
	}
	for _, want := range []string{`"visual"`, "Never name a real person", "photo, photograph or real footage"} {
		if !strings.Contains(rec.prompts[0], want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

// A visual the images stage would refuse is caught in the script stage's retry loop, with feedback.
func TestHistoryScriptStageRetriesABadVisual(t *testing.T) {
	var rec *recordingLLM
	bad := historyJSON(t, 90, func(s *pipeline.HistoryScript) { s.Beats[2].Visual = "a photograph of the crowd" })
	h := newHistoryHarness(t, 3, recordHistory(&rec), bad, historyJSON(t, 90, nil))
	h.addSource(t, "s1", "tower", historySource1)
	_, state, reason := h.run(t)
	if state != "ready_for_review" {
		t.Fatalf("run %s: %s", state, reason)
	}
	if len(rec.prompts) != 2 || !strings.Contains(rec.prompts[1], "beat 3: visual description uses evidence-style wording") {
		t.Fatalf("retry prompt lacks the visual feedback (%d prompts)", len(rec.prompts))
	}
}
