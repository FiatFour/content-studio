package pipeline_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/providers/fake"
	"github.com/fiat/content-studio/internal/store/storetest"
)

const thaiLine = "วันนี้ยายจะลองใช้โทรศัพท์เครื่องใหม่ที่หลานซื้อให้ดูนะ"

func fakeScript(t *testing.T) string {
	t.Helper()
	s := pipeline.Script{
		Title: "ยายกับโทรศัพท์",
		Characters: []pipeline.Character{
			{ID: "yai", Name: "ยายมาลี", Look: "elderly Thai woman, grey bun, floral blouse", Voice: "warm elderly woman"},
			{ID: "tan", Name: "ต้น", Look: "Thai teenage boy, messy hair, school uniform", Voice: "bright teenage boy"},
		},
	}
	cams := []string{"zoom_in", "pan_left", "zoom_out", "static", "pan_up", "zoom_in"}
	for sc := 0; sc < 2; sc++ {
		scene := pipeline.Scene{Setting: "a cozy Thai wooden house living room"}
		for sh := 0; sh < 3; sh++ {
			scene.Shots = append(scene.Shots, pipeline.Shot{
				ImagePrompt: "grandmother squints at a smartphone while her grandson laughs",
				Camera:      cams[sc*3+sh],
				Lines:       []pipeline.Line{{Speaker: "yai", Text: thaiLine}, {Speaker: "tan", Text: thaiLine}},
			})
		}
		s.Scenes = append(s.Scenes, scene)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type harness struct {
	db     *pgxpool.Pool
	deps   *pipeline.Deps
	worker *jobs.Worker
	llm    *fake.LLM
	ep     int64
}

func newHarness(t *testing.T, llmResponses ...string) *harness {
	t.Helper()
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	db := storetest.New(t)
	_, ep := storetest.Episode(t, db)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 2}
	llm := &fake.LLM{Responses: llmResponses}
	deps := &pipeline.Deps{
		DB: db, Store: &artifacts.Store{Root: t.TempDir()}, Queue: q,
		LLM: llm, Images: &fake.Images{}, Voice: &fake.Voice{SecondsPerRune: 0.09}, Segmenter: fake.Segmenter{},
		GPU:   &providers.GPU{},
		Tools: media.Tools{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"},
		Settings: pipeline.Settings{ImageWidth: 96, ImageHeight: 168, OutWidth: 144, OutHeight: 256, FPS: 10,
			SubtitleFont: "Noto Sans Thai", ScriptRetries: 3},
		Log: log,
	}
	w := &jobs.Worker{Queue: q, Stages: deps.Stages(), Log: log}
	return &harness{db: db, deps: deps, worker: w, llm: llm, ep: ep}
}

func (h *harness) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 30; i++ {
		ran, err := h.worker.RunOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("pipeline did not finish")
}

func (h *harness) runState(t *testing.T, runID int64) (string, string) {
	var state, reason string
	err := h.db.QueryRow(context.Background(), `SELECT r.state, coalesce((SELECT error_class || ': ' || error_message FROM jobs
		WHERE run_id = r.id AND error_message IS NOT NULL ORDER BY id DESC LIMIT 1), '') FROM workflow_runs r WHERE r.id = $1`, runID).Scan(&state, &reason)
	if err != nil {
		t.Fatal(err)
	}
	return state, reason
}

func TestFullPipelineProducesAVideoThatPassesQA(t *testing.T) {
	h := newHarness(t, `{"title": "x", "characters": [], "scenes": []}`, fakeScript(t))
	ctx := context.Background()
	runID, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.drain(t)
	if state, reason := h.runState(t, runID); state != string(jobs.RunReadyForReview) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if h.llm.Calls != 2 {
		t.Fatalf("invalid script should be re-prompted once, got %d calls", h.llm.Calls)
	}
	videos, err := artifacts.ForRun(ctx, h.db, runID, artifacts.Video)
	if err != nil || len(videos) != 1 {
		t.Fatalf("videos %v %v", videos, err)
	}
	path, _ := h.deps.Store.Abs(videos[0].StoragePath)
	p, err := h.deps.Tools.Probe(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.DurationSeconds(); d < 54 || d > 66 {
		t.Fatalf("duration %.2f outside 60 s ±10%%", d)
	}
	var status string
	h.db.QueryRow(ctx, `SELECT validation_status FROM artifacts WHERE id = $1`, videos[0].ID).Scan(&status)
	if status != "qa_passed" {
		t.Fatalf("video status %s", status)
	}
	var ready int
	h.db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE event = 'episode_ready' AND event_id = $1 AND status = 'pending'`,
		fmt.Sprintf("episode_ready:artifact:%d", videos[0].ID)).Scan(&ready)
	if ready != 1 {
		t.Fatalf("QA pass queued %d episode_ready notifications, want 1", ready)
	}
	var blockingFails int
	h.db.QueryRow(ctx, `SELECT count(*) FROM qa_results WHERE artifact_id = $1 AND severity = 'blocking' AND NOT passed`, videos[0].ID).Scan(&blockingFails)
	if blockingFails != 0 {
		t.Fatalf("%d blocking QA failures", blockingFails)
	}
	var parents int
	h.db.QueryRow(ctx, `SELECT count(*) FROM artifact_parents WHERE artifact_id = $1`, videos[0].ID).Scan(&parents)
	if parents == 0 {
		t.Fatal("video has no lineage")
	}
	for _, typ := range []artifacts.Type{artifacts.Image, artifacts.LineAudio, artifacts.VoiceRef} {
		list, _ := artifacts.ForRun(ctx, h.db, runID, typ)
		want := map[artifacts.Type]int{artifacts.Image: 6, artifacts.LineAudio: 12, artifacts.VoiceRef: 2}[typ]
		if len(list) != want {
			t.Fatalf("%s: %d artifacts, want %d", typ, len(list), want)
		}
	}

	// A re-render reuses the inputs and produces a new, separate video version.
	rerun, err := h.worker.Queue.CreateRun(ctx, h.ep, "rerender", &runID)
	if err != nil {
		t.Fatal(err)
	}
	h.drain(t)
	if state, reason := h.runState(t, rerun); state != string(jobs.RunReadyForReview) {
		t.Fatalf("rerender %s: %s", state, reason)
	}
	again, _ := artifacts.ForRun(ctx, h.db, rerun, artifacts.Video)
	if len(again) != 1 || again[0].Version != 2 {
		t.Fatalf("rerender video %+v", again)
	}
}

func TestScriptThatNeverValidatesFailsWithReason(t *testing.T) {
	h := newHarness(t, `{"title": "x", "characters": [], "scenes": []}`)
	runID, _ := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
	h.drain(t)
	state, reason := h.runState(t, runID)
	if state != string(jobs.RunFailed) || reason == "" {
		t.Fatalf("state %s reason %q", state, reason)
	}
	t.Log(reason)
}
