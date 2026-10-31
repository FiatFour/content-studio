package pipeline_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/providers/fake"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// M5 T7 acceptance: one Fictional Series episode and one Knowledge Shorts episode go through the
// real queue and two real worker loops side by side, with fake providers, real Postgres and real
// ffmpeg. Nothing here loads a model.

// gpuProbe counts provider calls in flight across every GPU capability (LLM, images, voice). The
// queue holds one "gpu" slot, so the peak must be 1 even with two workers and two profiles.
type gpuProbe struct {
	cur, peak, calls atomic.Int32
}

func (g *gpuProbe) enter() func() {
	n := g.cur.Add(1)
	g.calls.Add(1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(3 * time.Millisecond) // widen the window in which an overlap would be seen
	return func() { g.cur.Add(-1) }
}

// routedLLM answers the Shorts schema with the Shorts script and the Fictional Series schema with its
// script, so both profiles can share one registry like the real worker does.
type routedLLM struct {
	probe     *gpuProbe
	fs, short string
	// history is the History & Mystery script (M6 T7). Only its schema has a "visual" field, so it is
	// recognised before the Shorts schema, which also has "claims". Empty: History is not routed.
	history   string
	mu        sync.Mutex
	shortCall int
}

func (l *routedLLM) Info() providers.Info             { return providers.Info{Provider: "fake", Model: "fake-llm"} }
func (l *routedLLM) ReleaseGPU(context.Context) error { return nil }
func (l *routedLLM) GenerateJSON(_ context.Context, _ string, schema json.RawMessage, _ int) ([]byte, error) {
	defer l.probe.enter()()
	if l.history != "" && bytes.Contains(schema, []byte(`"visual"`)) {
		return []byte(l.history), nil
	}
	if bytes.Contains(schema, []byte(`"claims"`)) {
		l.mu.Lock()
		l.shortCall++
		l.mu.Unlock()
		return []byte(l.short), nil
	}
	return []byte(l.fs), nil
}

type probedImages struct {
	probe *gpuProbe
	fake.Images
}

func (p *probedImages) Generate(ctx context.Context, r providers.ImageRequest) ([]byte, error) {
	defer p.probe.enter()()
	return p.Images.Generate(ctx, r)
}

// probedVoice speaks Shorts narration (only the letter ก in these fixtures) at the Shorts pace and
// everything else at the Fictional Series pace.
type probedVoice struct {
	probe     *gpuProbe
	fs, short fake.Voice
}

func (v *probedVoice) Info() providers.Info { return v.fs.Info() }
func (v *probedVoice) pick(text string) *fake.Voice {
	if strings.Trim(text, "ก") == "" {
		return &v.short
	}
	return &v.fs
}
func (v *probedVoice) Design(ctx context.Context, d, text string, seed int) ([]byte, error) {
	defer v.probe.enter()()
	return v.pick(text).Design(ctx, d, text, seed)
}
func (v *probedVoice) Clone(ctx context.Context, ref []byte, text string, seed int) ([]byte, error) {
	defer v.probe.enter()()
	return v.pick(text).Clone(ctx, ref, text, seed)
}

type both struct {
	db         *pgxpool.Pool
	deps       *pipeline.Deps
	queues     []*jobs.Queue
	cloud      *fake.CloudSpy
	probe      *gpuProbe
	llm        *routedLLM
	fsEp, shEp int64
	shHarness  *shortsHarness
	exportDir  string
}

func newBoth(t *testing.T, shortsScript string) *both {
	t.Helper()
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	db := storetest.New(t)
	b := &both{db: db, cloud: &fake.CloudSpy{}, probe: &gpuProbe{}, exportDir: t.TempDir()}
	_, b.fsEp = storetest.Episode(t, db)
	b.llm = &routedLLM{probe: b.probe, fs: fakeScript(t), short: shortsScript}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := providers.NewRegistry(false)
	for _, r := range []providers.Registration{
		{Name: "test-llm", Kind: providers.KindLocal, Capability: providers.CapLLM, Impl: b.llm},
		{Name: "test-images", Kind: providers.KindLocal, Capability: providers.CapImage, Impl: &probedImages{probe: b.probe}},
		{Name: "test-voice", Kind: providers.KindLocal, Capability: providers.CapTTS,
			Impl: &probedVoice{probe: b.probe, fs: fake.Voice{SecondsPerRune: 0.09}, short: fake.Voice{SecondsPerRune: 0.06}}},
		{Name: "test-segment", Kind: providers.KindLocal, Capability: providers.CapSegment, Impl: fake.Segmenter{}},
		{Name: "cloud-llm", Kind: providers.KindCloud, Capability: providers.CapLLM, Impl: b.cloud},
		{Name: "cloud-images", Kind: providers.KindCloud, Capability: providers.CapImage, Impl: b.cloud},
		{Name: "cloud-voice", Kind: providers.KindCloud, Capability: providers.CapTTS, Impl: b.cloud},
		{Name: "cloud-segment", Kind: providers.KindCloud, Capability: providers.CapSegment, Impl: b.cloud},
	} {
		if err := reg.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	// Two queue handles with their own lease owner, the way two worker processes would have.
	for _, owner := range []string{"w1", "w2"} {
		b.queues = append(b.queues, &jobs.Queue{DB: db, Plans: pipeline.RunnablePlans(), MaxAttempts: 2, Owner: owner,
			Delay: func(*jobs.StageError, int) time.Duration { return 0 }})
	}
	b.deps = &pipeline.Deps{DB: db, Store: &artifacts.Store{Root: t.TempDir()}, Queue: b.queues[0], Providers: reg, GPU: &providers.GPU{},
		Tools: media.Tools{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"},
		Settings: pipeline.Settings{ImageWidth: 96, ImageHeight: 168, OutWidth: 144, OutHeight: 256, FPS: 10,
			SubtitleFont: "Noto Sans Thai", ScriptRetries: 3},
		Log: log}

	var shPid int64
	if err := db.QueryRow(context.Background(), `INSERT INTO projects (name, profile) VALUES ('Knowledge Shorts', 'knowledge_shorts') RETURNING id`).Scan(&shPid); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), `INSERT INTO episodes (project_id, number, title, idea, target_duration_s)
		VALUES ($1, 1, 'ทะเล', 'why is the sea salty', 45) RETURNING id`, shPid).Scan(&b.shEp); err != nil {
		t.Fatal(err)
	}
	b.shHarness = &shortsHarness{db: db, deps: b.deps, ep: b.shEp, pid: shPid}
	b.shHarness.addSource(t, "s1", "Sea water facts", shortsSource1)
	return b
}

// start runs one worker loop per queue handle until the test ends.
func (b *both) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, q := range b.queues {
		w := &jobs.Worker{Queue: q, ProfileStages: b.deps.RunnableStages(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), PollInterval: 5 * time.Millisecond}
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Run(ctx) }()
	}
	t.Cleanup(func() { cancel(); wg.Wait() })
}

func (b *both) wait(t *testing.T, runs ...int64) map[int64]string {
	t.Helper()
	out := map[int64]string{}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		done := true
		for _, id := range runs {
			var s string
			if err := b.db.QueryRow(context.Background(), `SELECT state FROM workflow_runs WHERE id = $1`, id).Scan(&s); err != nil {
				t.Fatal(err)
			}
			out[id] = s
			if s == string(jobs.RunRunning) {
				done = false
			}
		}
		if done {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("runs did not finish: %v", out)
	return nil
}

func (b *both) createRun(t *testing.T, ep int64, kind string, parent *int64) int64 {
	t.Helper()
	id, err := b.queues[0].CreateRun(context.Background(), ep, kind, parent)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (b *both) runProfile(t *testing.T, run int64) string {
	t.Helper()
	var p string
	if err := b.db.QueryRow(context.Background(), `SELECT profile FROM workflow_runs WHERE id = $1`, run).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (b *both) video(t *testing.T, run int64) artifacts.Artifact {
	t.Helper()
	vs, err := artifacts.ForRun(context.Background(), b.db, run, artifacts.Video)
	if err != nil || len(vs) != 1 {
		t.Fatalf("run %d videos %v %v", run, vs, err)
	}
	return vs[0]
}

func (b *both) jobStages(t *testing.T, run int64) []string {
	t.Helper()
	rows, err := b.db.Query(context.Background(), `SELECT stage FROM jobs WHERE run_id = $1 ORDER BY seq`, run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func (b *both) assertNoGPUOverlap(t *testing.T) {
	t.Helper()
	var n, gpuJobs int
	if err := b.db.QueryRow(context.Background(), `SELECT count(*) FROM jobs a JOIN jobs c ON a.id < c.id AND a.resource = 'gpu' AND c.resource = 'gpu'
		AND a.started_at < c.finished_at AND c.started_at < a.finished_at`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE resource = 'gpu'`).Scan(&gpuJobs); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d pairs of gpu jobs overlapped in time", n)
	}
	if p := b.probe.peak.Load(); p != 1 {
		t.Fatalf("peak concurrent GPU provider calls = %d, want 1 (calls %d, gpu jobs %d)", p, b.probe.calls.Load(), gpuJobs)
	}
}

func TestBothProfilesRunSideBySideThroughTheRealWorkerLoop(t *testing.T) {
	b := newBoth(t, shortsJSON(t, 45, nil))
	ctx := context.Background()
	b.start(t)
	fsRun := b.createRun(t, b.fsEp, "full", nil)
	shRun := b.createRun(t, b.shEp, "full", nil)
	if b.runProfile(t, fsRun) != pipeline.FictionalSeriesID || b.runProfile(t, shRun) != pipeline.KnowledgeShortsID {
		t.Fatal("runs did not record their own profile")
	}
	for id, s := range b.wait(t, fsRun, shRun) {
		if s != string(jobs.RunReadyForReview) {
			t.Fatalf("run %d is %s", id, s)
		}
	}
	full := []string{"script", "tts", "images", "subtitles", "render", "qa"}
	if !eq(b.jobStages(t, fsRun), full) || !eq(b.jobStages(t, shRun), full) {
		t.Fatalf("stages fs %v shorts %v", b.jobStages(t, fsRun), b.jobStages(t, shRun))
	}

	// Each run used its own profile's stages: the Shorts run has one narrator and claim-checked
	// sources, the Fictional Series run has its two characters and no Shorts metadata.
	fsVideo, shVideo := b.video(t, fsRun), b.video(t, shRun)
	for _, v := range []artifacts.Artifact{fsVideo, shVideo} {
		var status string
		if err := b.db.QueryRow(ctx, `SELECT validation_status FROM artifacts WHERE id = $1`, v.ID).Scan(&status); err != nil || status != "qa_passed" {
			t.Fatalf("video %d status %q %v", v.ID, status, err)
		}
		var blocking int
		b.db.QueryRow(ctx, `SELECT count(*) FROM qa_results WHERE artifact_id = $1 AND severity = 'blocking' AND NOT passed`, v.ID).Scan(&blocking)
		if blocking != 0 {
			t.Fatalf("video %d has %d blocking QA failures", v.ID, blocking)
		}
		pr, err := b.deps.Tools.Probe(ctx, mustAbs(t, b, v))
		if err != nil {
			t.Fatal(err)
		}
		if pr.DurationSeconds() <= 0 {
			t.Fatalf("video %d has no duration", v.ID)
		}
	}
	var shortsRules, fsShortsRules int
	b.db.QueryRow(ctx, `SELECT count(*) FROM qa_results WHERE artifact_id = $1 AND rule LIKE 'shorts\_%'`, shVideo.ID).Scan(&shortsRules)
	b.db.QueryRow(ctx, `SELECT count(*) FROM qa_results WHERE artifact_id = $1 AND rule LIKE 'shorts\_%'`, fsVideo.ID).Scan(&fsShortsRules)
	if shortsRules != 6 || fsShortsRules != 0 {
		t.Fatalf("shorts QA rules: shorts video %d (want 6), fictional video %d (want 0)", shortsRules, fsShortsRules)
	}
	if d := durationOf(t, b, shVideo); d < 40.5 || d > 49.5 {
		t.Fatalf("shorts duration %.2f outside 45 s +-10%%", d)
	}
	if d := durationOf(t, b, fsVideo); d < 54 || d > 66 {
		t.Fatalf("fictional duration %.2f outside 60 s +-10%%", d)
	}
	count := func(run int64, typ artifacts.Type) int {
		l, _ := artifacts.ForRun(ctx, b.db, run, typ)
		return len(l)
	}
	if count(fsRun, artifacts.Image) != 6 || count(fsRun, artifacts.VoiceRef) != 2 || count(fsRun, artifacts.LineAudio) != 12 {
		t.Fatal("fictional series artifacts are not 6 images, 2 voice refs, 12 clips")
	}
	if count(shRun, artifacts.Image) != 10 || count(shRun, artifacts.VoiceRef) != 1 || count(shRun, artifacts.LineAudio) != 8 {
		t.Fatalf("shorts artifacts: %d images, %d refs, %d clips; want 10, 1, 8",
			count(shRun, artifacts.Image), count(shRun, artifacts.VoiceRef), count(shRun, artifacts.LineAudio))
	}
	m := &shortsMedia{shortsHarness: b.shHarness}
	shScripts, _ := artifacts.ForRun(ctx, b.db, shRun, artifacts.Script)
	fsScripts, _ := artifacts.ForRun(ctx, b.db, fsRun, artifacts.Script)
	if len(shScripts) != 1 || len(fsScripts) != 1 || shScripts[0].Meta["profile"] != pipeline.KnowledgeShortsID || fsScripts[0].Meta["profile"] != nil {
		t.Fatalf("script meta shorts %v fictional %v", shScripts[0].Meta, fsScripts[0].Meta)
	}
	for _, v := range []struct {
		video  artifacts.Artifact
		run    int64
		script artifacts.Artifact
	}{{fsVideo, fsRun, fsScripts[0]}, {shVideo, shRun, shScripts[0]}} {
		specs, _ := artifacts.ForRun(ctx, b.db, v.run, artifacts.RenderSpec)
		imgs, _ := artifacts.ForRun(ctx, b.db, v.run, artifacts.Image)
		if len(specs) != 1 || !hasParent(t, m, v.video.ID, specs[0].ID) || !hasParent(t, m, specs[0].ID, imgs[0].ID) || !hasParent(t, m, imgs[0].ID, v.script.ID) {
			t.Fatalf("run %d lineage is not video <- render spec <- image <- script", v.run)
		}
	}
	// The run never crossed over: no artifact of one episode belongs to the other's run.
	var cross int
	b.db.QueryRow(ctx, `SELECT count(*) FROM artifacts a JOIN workflow_runs r ON r.id = a.run_id WHERE a.episode_id <> r.episode_id`).Scan(&cross)
	if cross != 0 {
		t.Fatalf("%d artifacts sit on another episode's run", cross)
	}

	// Re-render works for both profiles through the same path: a new version, the old one untouched.
	oldFS, oldSh := fsVideo.SHA256, shVideo.SHA256
	fsRe := b.createRun(t, b.fsEp, "rerender", &fsRun)
	shRe := b.createRun(t, b.shEp, "rerender", &shRun)
	for id, s := range b.wait(t, fsRe, shRe) {
		if s != string(jobs.RunReadyForReview) {
			t.Fatalf("rerender %d is %s", id, s)
		}
	}
	if !eq(b.jobStages(t, fsRe), []string{"render", "qa"}) || !eq(b.jobStages(t, shRe), []string{"render", "qa"}) {
		t.Fatal("rerender stages are not render, qa")
	}
	fsV2, shV2 := b.video(t, fsRe), b.video(t, shRe)
	if fsV2.Version != 2 || shV2.Version != 2 {
		t.Fatalf("rerender versions %d, %d", fsV2.Version, shV2.Version)
	}
	for _, id := range []int64{fsVideo.ID, shVideo.ID} {
		var sha string
		b.db.QueryRow(ctx, `SELECT sha256 FROM artifacts WHERE id = $1`, id).Scan(&sha)
		if sha != map[int64]string{fsVideo.ID: oldFS, shVideo.ID: oldSh}[id] {
			t.Fatalf("version 1 of artifact %d changed", id)
		}
	}
	for _, v := range []artifacts.Artifact{fsV2, shV2} {
		var status string
		b.db.QueryRow(ctx, `SELECT validation_status FROM artifacts WHERE id = $1`, v.ID).Scan(&status)
		if status != "qa_passed" {
			t.Fatalf("rerendered video %d status %q", v.ID, status)
		}
	}
	var shortsRules2 int
	b.db.QueryRow(ctx, `SELECT count(*) FROM qa_results WHERE artifact_id = $1 AND rule LIKE 'shorts\_%'`, shV2.ID).Scan(&shortsRules2)
	if shortsRules2 != 6 {
		t.Fatalf("rerendered shorts video has %d shorts QA rules, want 6", shortsRules2)
	}

	// Approval and export are bound to the exact hash, for each profile.
	rv := &review.Service{DB: b.db, Store: b.deps.Store, ExportDir: b.exportDir}
	for _, v := range []artifacts.Artifact{fsV2, shV2} {
		if _, err := rv.Export(ctx, v.ID, "test"); err == nil {
			t.Fatalf("export of unapproved video %d succeeded", v.ID)
		}
		if _, err := rv.Decide(ctx, v.ID, strings.Repeat("0", 64), review.Approved, "", "test"); err == nil {
			t.Fatalf("approval of video %d with a stale hash succeeded", v.ID)
		}
		if _, err := rv.Decide(ctx, v.ID, v.SHA256, review.Approved, "ok", "test"); err != nil {
			t.Fatal(err)
		}
		e, err := rv.Export(ctx, v.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		if e.SHA256 != v.SHA256 {
			t.Fatalf("export hash %s != approved %s", e.SHA256, v.SHA256)
		}
		if got, err := artifacts.HashFile(e.Path); err != nil || got != v.SHA256 {
			t.Fatalf("exported file hash %s %v", got, err)
		}
		if !strings.HasPrefix(filepath.Base(e.Path), "episode-") {
			t.Fatalf("export name %s", e.Path)
		}
	}
	// Approving version 2 approves nothing else: version 1 of each episode is still not exportable.
	for _, v := range []artifacts.Artifact{fsVideo, shVideo} {
		if _, err := rv.Export(ctx, v.ID, "test"); err == nil {
			t.Fatalf("export of unapproved version 1 (artifact %d) succeeded", v.ID)
		}
	}

	b.assertNoGPUOverlap(t)
	if b.cloud.Calls() != 0 {
		t.Fatalf("cloud adapter called %d times", b.cloud.Calls())
	}
}

func mustAbs(t *testing.T, b *both, a artifacts.Artifact) string {
	t.Helper()
	p, err := b.deps.Store.Abs(a.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func durationOf(t *testing.T, b *both, a artifacts.Artifact) float64 {
	t.Helper()
	pr, err := b.deps.Tools.Probe(context.Background(), mustAbs(t, b, a))
	if err != nil {
		t.Fatal(err)
	}
	return pr.DurationSeconds()
}

// A Shorts run that fails (the model never produces a script whose claims quote a source) fails only
// itself; the Fictional Series run beside it finishes and passes QA.
func TestFailedShortsRunDoesNotAffectFictionalSeriesRun(t *testing.T) {
	bad := shortsJSON(t, 45, func(s *pipeline.ShortsScript) { s.Claims[0].Quote = "this quote is not in the source" })
	b := newBoth(t, bad)
	ctx := context.Background()
	b.start(t)
	fsRun := b.createRun(t, b.fsEp, "full", nil)
	shRun := b.createRun(t, b.shEp, "full", nil)
	states := b.wait(t, fsRun, shRun)
	if states[shRun] != string(jobs.RunFailed) {
		t.Fatalf("shorts run is %s, want failed", states[shRun])
	}
	if states[fsRun] != string(jobs.RunReadyForReview) {
		t.Fatalf("fictional run is %s, want ready_for_review", states[fsRun])
	}
	var class, msg string
	if err := b.db.QueryRow(ctx, `SELECT error_class, error_message FROM jobs WHERE run_id = $1 AND state = 'failed'`, shRun).Scan(&class, &msg); err != nil {
		t.Fatal(err)
	}
	if class != string(jobs.ErrValidationFailed) {
		t.Fatalf("shorts failure class %q (%s)", class, msg)
	}
	if arts, _ := artifacts.ForRun(ctx, b.db, shRun, artifacts.Script); len(arts) != 0 {
		t.Fatal("a failed Shorts script was stored")
	}
	v := b.video(t, fsRun)
	var status string
	b.db.QueryRow(ctx, `SELECT validation_status FROM artifacts WHERE id = $1`, v.ID).Scan(&status)
	if status != "qa_passed" {
		t.Fatalf("fictional video status %q", status)
	}
	var shortsVideos int
	b.db.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE episode_id = $1 AND type = 'video'`, b.shEp).Scan(&shortsVideos)
	if shortsVideos != 0 {
		t.Fatal("the failed Shorts run left a video")
	}
	b.assertNoGPUOverlap(t)
	if b.cloud.Calls() != 0 {
		t.Fatalf("cloud adapter called %d times", b.cloud.Calls())
	}
}

// A run whose profile the queue has no plan for fails permanently with a fixed message. It never
// falls back to Fictional Series, and a queue refuses to create such a run.
func TestRunOfAnUnknownProfileFailsPermanently(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	var pid, ep, run int64
	if err := db.QueryRow(ctx, `INSERT INTO projects (name, profile) VALUES ('x', 'mystery_profile') RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s) VALUES ($1, 1, 't', 'idea idea idea', 60) RETURNING id`, pid).Scan(&ep); err != nil {
		t.Fatal(err)
	}
	q := &jobs.Queue{DB: db, Plans: pipeline.RunnablePlans(), MaxAttempts: 3, Delay: func(*jobs.StageError, int) time.Duration { return 0 }}
	if _, err := q.CreateRun(ctx, ep, "full", nil); !errors.Is(err, jobs.ErrUnknownProfile) {
		t.Fatalf("CreateRun for an unknown profile: %v", err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO workflow_runs (episode_id, profile, kind, state) VALUES ($1, 'mystery_profile', 'full', 'running') RETURNING id`, ep).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state) VALUES ($1, 'script', 0, 'queued')`, run); err != nil {
		t.Fatal(err)
	}
	called := false
	w := &jobs.Worker{Queue: q, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProfileStages: map[string]map[string]jobs.Stage{pipeline.FictionalSeriesID: {"script": panicStage{&called}}}}
	if ran, err := w.RunOne(ctx); err != nil || !ran {
		t.Fatalf("RunOne: %v %v", ran, err)
	}
	var runState, jobState, class, msg string
	var attempts int
	if err := db.QueryRow(ctx, `SELECT r.state, j.state, j.error_class, j.error_message, j.attempt_count FROM workflow_runs r JOIN jobs j ON j.run_id = r.id WHERE r.id = $1`, run).
		Scan(&runState, &jobState, &class, &msg, &attempts); err != nil {
		t.Fatal(err)
	}
	if runState != string(jobs.RunFailed) || jobState != "failed" || class != string(jobs.ErrUnsupported) || msg != "unknown content profile" || attempts != 1 {
		t.Fatalf("run %s job %s class %s msg %q attempts %d", runState, jobState, class, msg, attempts)
	}
	if called {
		t.Fatal("a Fictional Series stage ran for an unknown profile")
	}
	var jobsLeft int
	db.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE run_id = $1 AND state NOT IN ('failed')`, run).Scan(&jobsLeft)
	if ran, _ := w.RunOne(ctx); ran || jobsLeft != 0 {
		t.Fatal("the failed run was scheduled again")
	}
}

type panicStage struct{ called *bool }

func (p panicStage) Run(context.Context, *jobs.Job, func(int, int)) (func(pgx.Tx) error, error) {
	*p.called = true
	return nil, nil
}
