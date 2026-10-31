package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
)

// A rewrite run carries the reason the previous run was rejected; the Shorts scriptwriter must see it,
// together with its own in-stage retry feedback, and it must change the input hash.
func TestShortsRewriteRunPromptCarriesRunFeedback(t *testing.T) {
	const note = "SPOKEN-LENGTH-NOTE: the narration ran 9 seconds too long; write about 120 fewer characters"
	const retryMark = "source s9, which does not exist"
	fabricated := shortsJSON(t, 45, func(s *pipeline.ShortsScript) { s.Claims[0].SourceID = "s9" })
	h := newShortsHarness(t, 3, fabricated, shortsJSON(t, 45, nil))
	rec := &recordingLLM{LLM: h.llm}
	h = rewire(t, h, rec)
	h.addSource(t, "s1", "sea", shortsSource1)
	ctx := context.Background()
	full, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Mark the first run failed and drain it out of the way, then start the rewrite run as the queue does.
	if _, err := h.db.Exec(ctx, `UPDATE workflow_runs SET state = 'failed' WHERE id = $1`, full); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM jobs WHERE run_id = $1`, full); err != nil {
		t.Fatal(err)
	}
	var rewrite int64
	if err := h.db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, parent_run_id, state, feedback)
		VALUES ($1, 'knowledge_shorts', 'rewrite', $2, 'running', $3) RETURNING id`, h.ep, full, note).Scan(&rewrite); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state) VALUES ($1, 'script', 0, $2)`, rewrite, jobs.Queued); err != nil {
		t.Fatal(err)
	}
	k := pipeline.NewKnowledgeShortsScriptStage(h.deps).(jobs.Keyed)
	withNote, err := k.InputHash(ctx, &jobs.Job{ID: 1, RunID: rewrite, EpisodeID: h.ep})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := k.InputHash(ctx, &jobs.Job{ID: 1, RunID: full, EpisodeID: h.ep})
	if err != nil {
		t.Fatal(err)
	}
	if withNote == plain {
		t.Fatal("run feedback must change the input hash")
	}
	for i := 0; i < 5; i++ {
		ran, err := h.worker.RunOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	var state string
	if err := h.db.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id = $1`, rewrite).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(jobs.RunReadyForReview) {
		t.Fatalf("rewrite run %s", state)
	}
	if len(rec.prompts) != 2 {
		t.Fatalf("want 2 prompts (rejected try, then good try), got %d", len(rec.prompts))
	}
	if !strings.Contains(rec.prompts[0], note) {
		t.Error("the first prompt of a rewrite run must carry the run feedback")
	}
	if !strings.Contains(rec.prompts[1], note) || !strings.Contains(rec.prompts[1], retryMark) {
		t.Error("the retry prompt must carry both the run feedback and the in-stage feedback")
	}
}
