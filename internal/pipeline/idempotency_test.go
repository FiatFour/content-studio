package pipeline_test

import (
	"context"
	"testing"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers/fake"
)

func (h *harness) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (h *harness) imageCalls() int { return h.deps.Images.(*fake.Images).Calls }
func (h *harness) voiceCalls() int { return h.deps.Voice.(*fake.Voice).Calls }

// Re-enqueueing every completed stage of a finished run must produce no new artifact and call no
// provider: the fakes' call counters and the artifact count stay exactly where they were.
func TestReenqueuedCompletedRunMakesNoProviderCallAndNoNewArtifact(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	ctx := context.Background()
	runID, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.drain(t)
	if state, reason := h.runState(t, runID); state != string(jobs.RunReadyForReview) {
		t.Fatalf("run %s: %s", state, reason)
	}
	llmCalls, imageCalls, voiceCalls := h.llm.Calls, h.imageCalls(), h.voiceCalls()
	artifactsBefore := h.count(t, `SELECT count(*) FROM artifacts WHERE run_id = $1`, runID)
	videos, _ := artifacts.ForRun(ctx, h.db, runID, artifacts.Video)
	if llmCalls == 0 || imageCalls == 0 || voiceCalls == 0 || artifactsBefore == 0 || len(videos) != 1 {
		t.Fatalf("first pass did no work? llm=%d images=%d voice=%d artifacts=%d videos=%d", llmCalls, imageCalls, voiceCalls, artifactsBefore, len(videos))
	}
	if n := h.count(t, `SELECT count(*) FROM stage_executions WHERE run_id = $1`, runID); n != len(pipeline.FictionalSeries.Full) {
		t.Fatalf("recorded %d stage executions", n)
	}

	if _, err := h.db.Exec(ctx, `UPDATE jobs SET state = 'queued', finished_at = NULL WHERE run_id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE workflow_runs SET state = 'running', finished_at = NULL WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	h.drain(t)

	if state, reason := h.runState(t, runID); state != string(jobs.RunReadyForReview) {
		t.Fatalf("run after re-enqueue %s: %s", state, reason)
	}
	if h.llm.Calls != llmCalls || h.imageCalls() != imageCalls || h.voiceCalls() != voiceCalls {
		t.Fatalf("providers were called again: llm %d->%d images %d->%d voice %d->%d",
			llmCalls, h.llm.Calls, imageCalls, h.imageCalls(), voiceCalls, h.voiceCalls())
	}
	if n := h.count(t, `SELECT count(*) FROM artifacts WHERE run_id = $1`, runID); n != artifactsBefore {
		t.Fatalf("artifacts %d -> %d", artifactsBefore, n)
	}
	videos2, _ := artifacts.ForRun(ctx, h.db, runID, artifacts.Video)
	if len(videos2) != 1 || videos2[0].ID != videos[0].ID || videos2[0].SHA256 != videos[0].SHA256 {
		t.Fatalf("the existing video artifact was not the one returned: %+v", videos2)
	}
}
