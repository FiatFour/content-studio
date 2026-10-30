package pipeline_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/providers/fake"
)

type shortsMedia struct {
	*shortsHarness
	images *fake.Images
	voice  *fake.Voice
}

// newShortsMedia seeds a 45 s knowledge_shorts episode and runs the five planned stages (script, tts,
// images, subtitles, render) through a test-only plan built from the profile's own stage list and
// resource map. The profile is not runnable, so no production plan exists for it.
func newShortsMedia(t *testing.T, llmResponses ...string) *shortsMedia {
	t.Helper()
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	h := newShortsHarness(t, 3, llmResponses...)
	p, _ := pipeline.Profiles.Get(pipeline.KnowledgeShortsID)
	spec := p.Spec()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &jobs.Queue{DB: h.db, Plan: jobs.Plan{Profile: spec.ID, Full: spec.Stages["full"], Resources: spec.Resources}, MaxAttempts: 2,
		Delay: func(*jobs.StageError, int) time.Duration { return 0 }}
	m := &shortsMedia{shortsHarness: h, images: &fake.Images{}, voice: &fake.Voice{SecondsPerRune: 0.06}}
	reg := providers.NewRegistry(false)
	for _, r := range []providers.Registration{
		{Name: "test-llm", Kind: providers.KindLocal, Capability: providers.CapLLM, Impl: h.llm},
		{Name: "test-images", Kind: providers.KindLocal, Capability: providers.CapImage, Impl: m.images},
		{Name: "test-voice", Kind: providers.KindLocal, Capability: providers.CapTTS, Impl: m.voice},
		{Name: "test-segment", Kind: providers.KindLocal, Capability: providers.CapSegment, Impl: fake.Segmenter{}},
		{Name: "cloud-llm", Kind: providers.KindCloud, Capability: providers.CapLLM, Impl: h.cloud},
		{Name: "cloud-images", Kind: providers.KindCloud, Capability: providers.CapImage, Impl: h.cloud},
		{Name: "cloud-voice", Kind: providers.KindCloud, Capability: providers.CapTTS, Impl: h.cloud},
		{Name: "cloud-segment", Kind: providers.KindCloud, Capability: providers.CapSegment, Impl: h.cloud},
	} {
		if err := reg.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	h.deps = &pipeline.Deps{DB: h.db, Store: &artifacts.Store{Root: t.TempDir()}, Queue: q, Providers: reg, GPU: &providers.GPU{},
		Tools: media.Tools{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"},
		Settings: pipeline.Settings{ImageWidth: 96, ImageHeight: 168, OutWidth: 144, OutHeight: 256, FPS: 10,
			SubtitleFont: "Noto Sans Thai", ScriptRetries: 3},
		Log: log}
	h.worker = &jobs.Worker{Queue: q, Stages: p.Stages(h.deps), Log: log}
	return m
}

func (m *shortsMedia) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 30; i++ {
		ran, err := m.worker.RunOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("pipeline did not finish")
}

func TestKnowledgeShortsMediaStagesProduceAVideoFromTheShortsScript(t *testing.T) {
	m := newShortsMedia(t, shortsJSON(t, 45, nil))
	ctx := context.Background()
	s1 := m.addSource(t, "s1", "Sea water facts", shortsSource1)
	s2 := m.addSource(t, "s2", "Venus {\\an8}<b>notes", "ดาวศุกร์ร้อนกว่าดาวพุธ")
	runID, err := m.worker.Queue.CreateRun(ctx, m.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	m.drain(t)
	var state string
	if err := m.db.QueryRow(ctx, `SELECT state FROM workflow_runs WHERE id = $1`, runID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == string(jobs.RunFailed) {
		var msg string
		m.db.QueryRow(ctx, `SELECT coalesce(error_message,'') FROM jobs WHERE run_id = $1 AND error_message IS NOT NULL ORDER BY id DESC LIMIT 1`, runID).Scan(&msg)
		t.Fatalf("run failed: %s", msg)
	}

	script, ss, _ := m.storedScript(t, runID)
	// Images: 8 beats plus the title and sources cards, flat style prefix, no LoRA, no character look.
	imgs, _ := artifacts.ForRun(ctx, m.db, runID, artifacts.Image)
	if len(imgs) != len(ss.Beats)+2 || len(m.images.Requests) != len(ss.Beats)+2 {
		t.Fatalf("images %d requests %d, want %d", len(imgs), len(m.images.Requests), len(ss.Beats)+2)
	}
	p, _ := pipeline.Profiles.Get(pipeline.KnowledgeShortsID)
	v := p.Spec().Visual
	for i, r := range m.images.Requests {
		if !strings.HasPrefix(r.Prompt, v.StylePrefix) || r.Negative != v.Negative {
			t.Fatalf("request %d prompt/negative not the flat style: %q / %q", i, r.Prompt, r.Negative)
		}
		if len(r.Loras) != 0 {
			t.Fatalf("request %d carries LoRAs %v", i, r.Loras)
		}
		if strings.Contains(r.Prompt, "adult animated sitcom") || strings.Contains(r.Prompt, "setting:") {
			t.Fatalf("request %d prompt has Fictional Series styling: %q", i, r.Prompt)
		}
	}
	if !strings.Contains(m.images.Requests[1].Prompt, strings.Repeat("ก", 10)) {
		t.Fatalf("beat prompt does not carry the narration: %q", m.images.Requests[1].Prompt)
	}
	for _, a := range imgs {
		if a.Meta["loras"] != nil || a.Meta["trigger_tokens"] != nil {
			t.Fatalf("image meta names a LoRA: %v", a.Meta)
		}
		if !hasParent(t, m, a.ID, script.ID) {
			t.Fatalf("image %d is not a child of the script %d", a.ID, script.ID)
		}
	}

	// TTS: one narrator voice reference, one clip per beat.
	refs, _ := artifacts.ForRun(ctx, m.db, runID, artifacts.VoiceRef)
	clips, _ := artifacts.ForRun(ctx, m.db, runID, artifacts.LineAudio)
	if len(refs) != 1 || refs[0].ItemKey != "narrator" || refs[0].Meta["description"] != v.NarratorVoice {
		t.Fatalf("voice refs %+v", refs)
	}
	if len(clips) != len(ss.Beats) {
		t.Fatalf("%d clips, want %d", len(clips), len(ss.Beats))
	}
	for i, c := range clips {
		if c.Meta["speaker"] != "narrator" || c.Meta["text"] != ss.Beats[i].Narration {
			t.Fatalf("clip %d meta %v", i, c.Meta)
		}
	}

	// Subtitles: every beat is covered, plus the title card and the Sources card with the right titles.
	subs, _ := artifacts.ForRun(ctx, m.db, runID, artifacts.Subtitles)
	if len(subs) != 1 {
		t.Fatalf("%d subtitle artifacts", len(subs))
	}
	path, _ := m.deps.Store.Abs(subs[0].StoragePath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	srt := strings.ReplaceAll(string(raw), "\u200b", "")
	for _, want := range []string{ss.Title, v.SourcesHeading, "Sea water facts", "Venus an8bnotes"} {
		if !strings.Contains(srt, want) {
			t.Fatalf("SRT lacks %q:\n%s", want, srt)
		}
	}
	for _, bad := range []string{"{", "<b>", "\\an8"} {
		if strings.Contains(srt, bad) {
			t.Fatalf("SRT keeps markup %q:\n%s", bad, srt)
		}
	}
	// The fake segmenter cuts narration into pieces, so check coverage by letters: all narration letters appear.
	if got, want := strings.Count(srt, "ก"), len(ss.Beats)*len([]rune(ss.Beats[0].Narration)); got < want {
		t.Fatalf("SRT covers %d narration characters, want at least %d", got, want)
	}
	if strings.Index(srt, ss.Title) > strings.Index(srt, "ก") || strings.LastIndex(srt, "Sea water facts") < strings.LastIndex(srt, "ก") {
		t.Fatalf("title card must come first and sources card last:\n%s", srt)
	}

	// Render: probe the video, parents, duration within 30-60 s and within 10% of the 45 s target.
	videos, _ := artifacts.ForRun(ctx, m.db, runID, artifacts.Video)
	if len(videos) != 1 {
		t.Fatalf("%d videos", len(videos))
	}
	vp, _ := m.deps.Store.Abs(videos[0].StoragePath)
	pr, err := m.deps.Tools.Probe(ctx, vp)
	if err != nil {
		t.Fatal(err)
	}
	if d := pr.DurationSeconds(); d < 30 || d > 60 || d < 40.5 || d > 49.5 {
		t.Fatalf("duration %.2f s outside 30-60 s and 45 s +-10%%", d)
	}
	specs, _ := artifacts.ForRun(ctx, m.db, runID, artifacts.RenderSpec)
	if len(specs) != 1 || !hasParent(t, m, videos[0].ID, specs[0].ID) || !hasParent(t, m, specs[0].ID, imgs[0].ID) {
		t.Fatal("video lineage is not video <- render spec <- images")
	}

	// The script's parents are the source versions, and nothing reached a cloud adapter.
	if !hasParent(t, m, script.ID, s1) || !hasParent(t, m, script.ID, s2) {
		t.Fatal("script lost its source parents")
	}
	if m.cloud.Calls() != 0 {
		t.Fatalf("cloud adapter called %d times", m.cloud.Calls())
	}
}

func hasParent(t *testing.T, m *shortsMedia, child, parent int64) bool {
	t.Helper()
	var n int
	if err := m.db.QueryRow(context.Background(), `SELECT count(*) FROM artifact_parents WHERE artifact_id = $1 AND parent_id = $2`, child, parent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestKnowledgeShortsListsOnlyTheFirstSourcesOnTheCard(t *testing.T) {
	m := newShortsMedia(t, shortsJSON(t, 45, nil))
	for i, key := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		text := "ข้อความที่หก"
		if i == 0 {
			text = shortsSource1
		}
		m.addSource(t, key, "title "+key, text)
	}
	runID, err := m.worker.Queue.CreateRun(context.Background(), m.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	m.drain(t)
	subs, _ := artifacts.ForRun(context.Background(), m.db, runID, artifacts.Subtitles)
	if len(subs) != 1 {
		t.Fatalf("%d subtitle artifacts", len(subs))
	}
	path, _ := m.deps.Store.Abs(subs[0].StoragePath)
	raw, _ := os.ReadFile(path)
	srt := strings.ReplaceAll(string(raw), "\u200b", "")
	if !strings.Contains(srt, "title s4") || strings.Contains(srt, "title s5") || !strings.Contains(srt, "2") {
		t.Fatalf("sources card should list four titles and a count of the rest:\n%s", srt)
	}
}

// T7 edit: this asserted the profile was NOT runnable (T5/T6). It is runnable now: every stage of
// its production list has an implementation, and a rerender is render then qa.
func TestKnowledgeShortsStagesAreImplementedAndRunnable(t *testing.T) {
	p, _ := pipeline.Profiles.Get(pipeline.KnowledgeShortsID)
	spec := p.Spec()
	if !spec.Runnable() || len(spec.Plan().Full) != 6 || len(spec.Plan().Rerender) != 2 {
		t.Fatal("knowledge_shorts must be runnable with a full and a rerender list")
	}
	impl := p.Stages(&pipeline.Deps{})
	for _, st := range spec.Stages["full"] {
		if impl[st] == nil {
			t.Errorf("planned stage %q has no implementation", st)
		}
	}
	// T6 edit: this used to assert qa was absent; the shared qa stage is now planned and implemented.
	if impl["qa"] == nil || spec.Stages["full"][len(spec.Stages["full"])-1] != "qa" {
		t.Error("qa must be the last planned stage and have an implementation")
	}
	for st, res := range map[string]string{"script": "gpu", "tts": "gpu", "images": "gpu"} {
		if spec.Resources[st] != res {
			t.Errorf("resource of %s = %q", st, spec.Resources[st])
		}
	}
	if spec.Resources["render"] != "" {
		t.Error("render must hold no resource")
	}
}
