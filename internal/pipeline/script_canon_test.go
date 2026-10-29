package pipeline_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers/fake"
)

// recordingLLM wraps the fake LLM and keeps every prompt it was sent.
type recordingLLM struct {
	*fake.LLM
	prompts []string
}

func (r *recordingLLM) GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage, seed int) ([]byte, error) {
	r.prompts = append(r.prompts, prompt)
	return r.LLM.GenerateJSON(ctx, prompt, schema, seed)
}

// canonScript builds a valid script whose declared characters are decls and whose shots alternate
// between speakers a and b.
func canonScript(t *testing.T, decls []pipeline.Character, a, b string) string {
	t.Helper()
	s := pipeline.Script{Title: "ยายกับโทรศัพท์", Characters: decls}
	cams := []string{"zoom_in", "pan_left", "zoom_out", "static", "pan_up", "zoom_in"}
	for sc := 0; sc < 2; sc++ {
		scene := pipeline.Scene{Setting: "a cozy Thai wooden house living room"}
		for sh := 0; sh < 3; sh++ {
			scene.Shots = append(scene.Shots, pipeline.Shot{
				ImagePrompt: "grandmother squints at a smartphone while her grandson laughs",
				Camera:      cams[sc*3+sh],
				Lines:       []pipeline.Line{{Speaker: a, Text: thaiLine}, {Speaker: b, Text: thaiLine}},
			})
		}
		s.Scenes = append(s.Scenes, scene)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func decl(id, look, voice string) pipeline.Character {
	return pipeline.Character{ID: id, Name: "model " + id, Look: look, Voice: voice}
}

// canonHarness creates the harness with a recording LLM and two series characters, yai (alive) and tan.
func canonHarness(t *testing.T, responses ...string) (*harness, *recordingLLM, int64) {
	t.Helper()
	h := newHarness(t, responses...)
	rec := &recordingLLM{LLM: h.llm}
	h.setLLM(rec)
	ctx := context.Background()
	var pid int64
	if err := h.db.QueryRow(ctx, `SELECT project_id FROM episodes WHERE id = $1`, h.ep).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{{"yai", "ยายมาลี", "canonical grandmother, grey bun"}, {"tan", "ต้น", "canonical teen boy, messy hair"}} {
		if _, err := h.db.Exec(ctx, `INSERT INTO characters (project_id, slug, name, appearance, voice) VALUES ($1, $2, $3, $4, 'canonical stored voice')`,
			pid, c[0], c[1], c[2]); err != nil {
			t.Fatal(err)
		}
	}
	return h, rec, pid
}

// runScript runs the script job only and returns the stored script, or nil when no script was stored.
func runScript(t *testing.T, h *harness) (runID int64, stored *pipeline.Script) {
	t.Helper()
	ctx := context.Background()
	runID, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.worker.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	arts, err := artifacts.ForRun(ctx, h.db, runID, artifacts.Script)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) == 0 {
		return runID, nil
	}
	path, _ := h.deps.Store.Abs(arts[0].StoragePath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s pipeline.Script
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return runID, &s
}

func TestScriptWithoutSeriesCharactersIsUnchanged(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	rec := &recordingLLM{LLM: h.llm}
	h.setLLM(rec)
	_, s := runScript(t, h)
	if s == nil || len(s.Characters) != 2 || s.Characters[0].Look != "elderly Thai woman, grey bun, floral blouse" {
		t.Fatalf("stored script %+v", s)
	}
	if len(rec.prompts) != 1 || strings.Contains(rec.prompts[0], "Series characters") || strings.Contains(rec.prompts[0], "Recent episodes") {
		t.Fatalf("a project without characters must get the plain prompt: %q", rec.prompts)
	}
}

func TestScriptWithUnknownSpeakerIsRejectedThenRetried(t *testing.T) {
	good := canonScript(t, []pipeline.Character{decl("yai", "x look here", "x voice here"), decl("tan", "x look here", "x voice here")}, "yai", "tan")
	bad := canonScript(t, []pipeline.Character{decl("yai", "x look here", "x voice here"), decl("zed", "x look here", "x voice here")}, "yai", "zed")
	h, rec, _ := canonHarness(t, bad, good)
	_, s := runScript(t, h)
	if s == nil {
		t.Fatal("the retry should have produced a script")
	}
	if h.llm.Calls != 2 || len(rec.prompts) != 2 {
		t.Fatalf("calls %d", h.llm.Calls)
	}
	if !strings.Contains(rec.prompts[1], `"zed" is not a character of this series`) {
		t.Fatalf("the rejection must be fed back: %q", rec.prompts[1])
	}
	for _, c := range s.Characters {
		if c.ID == "zed" {
			t.Fatal("unknown character stored")
		}
	}
}

func TestScriptWithAbsentCharacterAsSpeakerIsRejected(t *testing.T) {
	for _, status := range []string{"deceased", "moved_away"} {
		t.Run(status, func(t *testing.T) {
			good := canonScript(t, []pipeline.Character{decl("yai", "x look here", "x voice here"), decl("tan", "x look here", "x voice here")}, "yai", "tan")
			h, rec, pid := canonHarness(t, good, good)
			ctx := context.Background()
			if _, err := h.db.Exec(ctx, `UPDATE characters SET status = $2, status_since = '2024-01-01' WHERE project_id = $1 AND slug = 'yai'`, pid, status); err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.Exec(ctx, `UPDATE episodes SET in_story_date = '2024-06-01' WHERE id = $1`, h.ep); err != nil {
				t.Fatal(err)
			}
			// Both answers use yai as a speaker, so the script can never be accepted.
			runID, s := runScript(t, h)
			if s != nil {
				t.Fatal("a script with an absent speaker was stored")
			}
			if h.llm.Calls != 3 {
				t.Fatalf("expected %d tries, got %d", 3, h.llm.Calls)
			}
			if !strings.Contains(rec.prompts[1], "cannot appear") {
				t.Fatalf("the rejection must be fed back: %q", rec.prompts[1])
			}
			if state, _ := h.runState(t, runID); state == "ready_for_review" {
				t.Fatalf("run state %s", state)
			}
		})
	}
}

func TestStoredScriptCarriesCanonicalLookAndVoice(t *testing.T) {
	invented := canonScript(t, []pipeline.Character{decl("yai", "a robot with laser eyes", "deep robot voice"), decl("tan", "a purple dragon wearing a hat", "squeaky dragon voice")}, "yai", "tan")
	h, _, _ := canonHarness(t, invented)
	_, s := runScript(t, h)
	if s == nil || len(s.Characters) != 2 {
		t.Fatalf("stored script %+v", s)
	}
	want := map[string][3]string{
		"yai": {"ยายมาลี", "canonical grandmother, grey bun", "canonical stored voice"},
		"tan": {"ต้น", "canonical teen boy, messy hair", "canonical stored voice"},
	}
	for _, c := range s.Characters {
		w := want[c.ID]
		if c.Name != w[0] || c.Look != w[1] || c.Voice != w[2] {
			t.Fatalf("character %s not canonical: %+v", c.ID, c)
		}
	}
}

func TestScriptPromptHasCharacterIdsAndAtMostThreeSummaries(t *testing.T) {
	good := canonScript(t, []pipeline.Character{decl("yai", "x look here", "x voice here"), decl("tan", "x look here", "x voice here")}, "yai", "tan")
	h, rec, pid := canonHarness(t, good)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `UPDATE episodes SET number = 10 WHERE id = $1`, h.ep); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 5; n++ {
		var eid int64
		if err := h.db.QueryRow(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s) VALUES ($1, $2, 'ep', 'idea', 60) RETURNING id`, pid, n).Scan(&eid); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(ctx, `INSERT INTO episode_summaries (episode_id, summary) VALUES ($1, $2)`, eid, fmt.Sprintf("SUMMARYMARK%d", n)); err != nil {
			t.Fatal(err)
		}
	}
	if _, s := runScript(t, h); s == nil {
		t.Fatal("no script stored")
	}
	p := rec.prompts[0]
	for _, id := range []string{`id "yai"`, `id "tan"`} {
		if !strings.Contains(p, id) {
			t.Fatalf("prompt lacks %s", id)
		}
	}
	if got := strings.Count(p, "SUMMARYMARK"); got != 3 {
		t.Fatalf("prompt holds %d summaries, want 3", got)
	}
	if strings.Contains(p, "SUMMARYMARK1") || strings.Contains(p, "SUMMARYMARK2") || !strings.Contains(p, "SUMMARYMARK5") {
		t.Fatal("prompt must hold the three most recent summaries")
	}
}

func TestAbsentCharacterIsFineBeforeItsStatusDate(t *testing.T) {
	good := canonScript(t, []pipeline.Character{decl("yai", "x look here", "x voice here"), decl("tan", "x look here", "x voice here")}, "yai", "tan")
	h, _, pid := canonHarness(t, good)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `UPDATE characters SET status = 'deceased', status_since = '2024-01-01' WHERE project_id = $1 AND slug = 'yai'`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE episodes SET in_story_date = '2023-06-01' WHERE id = $1`, h.ep); err != nil {
		t.Fatal(err)
	}
	if _, s := runScript(t, h); s == nil || h.llm.Calls != 1 {
		t.Fatalf("a character who dies later must still appear earlier (calls %d)", h.llm.Calls)
	}
}
