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
	"github.com/fiat/content-studio/internal/review"
)

// M6 T7 acceptance: Fictional Series, Knowledge Shorts and History & Mystery go through the real queue
// and two real worker loops side by side, with fake providers, real Postgres and real ffmpeg. Nothing
// here loads a model.

// historyRuleNames are the rules of qa-rules-history-v1 (history_qa.go).
var historyRuleNames = []string{"history_duration_range", "history_narration_budget", "history_sources_intact", "history_claims_quoted",
	"history_beats_supported", "history_uncertainty_labelled", "history_numbers_in_sources", "history_sources_card", "history_sources_current",
	"history_stills_labelled"}

// hedgedHistoryJSON is historyJSON with the speculation beat (beat 6, claim c2) worded as an open
// question, which qa-rules-history-v1 requires (history_uncertainty_labelled).
func hedgedHistoryJSON(t *testing.T, secs int) string {
	t.Helper()
	return historyJSON(t, secs, func(s *pipeline.HistoryScript) {
		const hedge = "ยังไม่มีข้อสรุปว่า"
		n := len([]rune(s.Beats[5].Narration))
		s.Beats[5].Narration = hedge + strings.Repeat("ก", n-len([]rune(hedge)))
	})
}

type three struct {
	*both
	hsEp int64
	hh   *historyHarness
}

// newThree is newBoth plus a 90 s history_mystery episode with one source (s1).
func newThree(t *testing.T) *three {
	t.Helper()
	b := newBoth(t, shortsJSON(t, 45, nil))
	b.llm.history = hedgedHistoryJSON(t, 90)
	var pid, ep int64
	ctx := context.Background()
	if err := b.db.QueryRow(ctx, `INSERT INTO projects (name, profile) VALUES ('History', 'history_mystery') RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err := b.db.QueryRow(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s)
		VALUES ($1, 1, 'หอไอเฟล', 'the tower and its open questions', 90) RETURNING id`, pid).Scan(&ep); err != nil {
		t.Fatal(err)
	}
	h := &historyHarness{db: b.db, deps: b.deps, ep: ep, pid: pid}
	h.addSource(t, "s1", "Tower facts", historySource1)
	return &three{both: b, hsEp: ep, hh: h}
}

func (b *three) status(t *testing.T, a artifacts.Artifact) string {
	t.Helper()
	var s string
	if err := b.db.QueryRow(context.Background(), `SELECT validation_status FROM artifacts WHERE id = $1`, a.ID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (b *three) ruleCount(t *testing.T, a artifacts.Artifact, like string) int {
	t.Helper()
	var n int
	if err := b.db.QueryRow(context.Background(), `SELECT count(*) FROM qa_results WHERE artifact_id = $1 AND rule LIKE $2`, a.ID, like).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestThreeProfilesRunSideBySideThroughTheRealWorkerLoop(t *testing.T) {
	b := newThree(t)
	ctx := context.Background()
	b.start(t)
	fsRun := b.createRun(t, b.fsEp, "full", nil)
	shRun := b.createRun(t, b.shEp, "full", nil)
	hsRun := b.createRun(t, b.hsEp, "full", nil)
	if b.runProfile(t, hsRun) != pipeline.HistoryMysteryID {
		t.Fatal("the history run did not record its own profile")
	}
	for id, s := range b.wait(t, fsRun, shRun, hsRun) {
		if s != string(jobs.RunReadyForReview) {
			t.Fatalf("run %d is %s", id, s)
		}
	}
	full := []string{"script", "tts", "images", "subtitles", "render", "qa"}
	for _, run := range []int64{fsRun, shRun, hsRun} {
		if !eq(b.jobStages(t, run), full) {
			t.Fatalf("run %d stages %v", run, b.jobStages(t, run))
		}
	}
	videos := map[string]artifacts.Artifact{"fs": b.video(t, fsRun), "sh": b.video(t, shRun), "hs": b.video(t, hsRun)}
	for name, v := range videos {
		if s := b.status(t, v); s != "qa_passed" {
			t.Fatalf("%s video status %q", name, s)
		}
		var blocking int
		b.db.QueryRow(ctx, `SELECT count(*) FROM qa_results WHERE artifact_id = $1 AND severity = 'blocking' AND NOT passed`, v.ID).Scan(&blocking)
		if blocking != 0 {
			t.Fatalf("%s video has %d blocking QA failures", name, blocking)
		}
		if durationOf(t, b.both, v) <= 0 {
			t.Fatalf("%s video has no duration", name)
		}
	}
	// Each profile got its own rules and no other profile's.
	hv := videos["hs"]
	if n := b.ruleCount(t, hv, "history\\_%"); n != len(historyRuleNames) {
		t.Fatalf("history video has %d history rules, want %d", n, len(historyRuleNames))
	}
	if b.ruleCount(t, hv, "shorts\\_%") != 0 || b.ruleCount(t, videos["sh"], "history\\_%") != 0 || b.ruleCount(t, videos["fs"], "history\\_%") != 0 {
		t.Fatal("QA rules leaked across profiles")
	}
	if b.ruleCount(t, videos["sh"], "shorts\\_%") != 6 || b.ruleCount(t, videos["fs"], "shorts\\_%") != 0 {
		t.Fatal("the older profiles' QA rules changed")
	}
	if d := durationOf(t, b.both, hv); d < 81 || d > 99 {
		t.Fatalf("history duration %.2f outside 90 s +-10%%", d)
	}
	count := func(run int64, typ artifacts.Type) int {
		l, _ := artifacts.ForRun(ctx, b.db, run, typ)
		return len(l)
	}
	// 8 beats: a title card, 8 beat stills and a Sources card; one narrator; 8 clips.
	if count(hsRun, artifacts.Image) != 10 || count(hsRun, artifacts.VoiceRef) != 1 || count(hsRun, artifacts.LineAudio) != 8 {
		t.Fatalf("history artifacts: %d images, %d refs, %d clips; want 10, 1, 8",
			count(hsRun, artifacts.Image), count(hsRun, artifacts.VoiceRef), count(hsRun, artifacts.LineAudio))
	}
	imgs, _ := artifacts.ForRun(ctx, b.db, hsRun, artifacts.Image)
	for _, im := range imgs {
		if im.Meta["image_kind"] != pipeline.ImageKindIllustration || im.Meta["evidence"] != false {
			t.Fatalf("history still %d is not labelled an illustration: %v", im.ID, im.Meta)
		}
	}
	scripts, _ := artifacts.ForRun(ctx, b.db, hsRun, artifacts.Script)
	if len(scripts) != 1 || scripts[0].Meta["profile"] != pipeline.HistoryMysteryID {
		t.Fatalf("history script meta %v", scripts)
	}
	m := &shortsMedia{shortsHarness: b.shHarness}
	specs, _ := artifacts.ForRun(ctx, b.db, hsRun, artifacts.RenderSpec)
	if len(specs) != 1 || !hasParent(t, m, hv.ID, specs[0].ID) || !hasParent(t, m, specs[0].ID, imgs[0].ID) || !hasParent(t, m, imgs[0].ID, scripts[0].ID) {
		t.Fatal("history lineage is not video <- render spec <- image <- script")
	}
	// The image prompts are the validated visuals inside the History style; no claim, quote or
	// narration text reached them.
	for _, line := range []string{historyQuote1, historyQuote2} {
		for _, im := range imgs {
			if p, _ := im.Meta["prompt"].(string); strings.Contains(p, line) {
				t.Fatalf("a quote reached an image prompt: %q", p)
			}
		}
	}

	// Re-render works for all three through the same path: a new version, the old one untouched.
	old := map[string]string{"fs": videos["fs"].SHA256, "sh": videos["sh"].SHA256, "hs": hv.SHA256}
	fsRe := b.createRun(t, b.fsEp, "rerender", &fsRun)
	shRe := b.createRun(t, b.shEp, "rerender", &shRun)
	hsRe := b.createRun(t, b.hsEp, "rerender", &hsRun)
	for id, s := range b.wait(t, fsRe, shRe, hsRe) {
		if s != string(jobs.RunReadyForReview) {
			t.Fatalf("rerender %d is %s", id, s)
		}
	}
	v2 := map[string]artifacts.Artifact{"fs": b.video(t, fsRe), "sh": b.video(t, shRe), "hs": b.video(t, hsRe)}
	for name, v := range v2 {
		if v.Version != 2 || b.status(t, v) != "qa_passed" {
			t.Fatalf("%s rerender: version %d status %q", name, v.Version, b.status(t, v))
		}
		var sha string
		b.db.QueryRow(ctx, `SELECT sha256 FROM artifacts WHERE id = $1`, videos[name].ID).Scan(&sha)
		if sha != old[name] {
			t.Fatalf("version 1 of the %s video changed", name)
		}
	}
	if !eq(b.jobStages(t, hsRe), []string{"render", "qa"}) {
		t.Fatalf("history rerender stages %v", b.jobStages(t, hsRe))
	}
	if n := b.ruleCount(t, v2["hs"], "history\\_%"); n != len(historyRuleNames) {
		t.Fatalf("rerendered history video has %d history rules", n)
	}

	// Approval and export of the History video are bound to the exact hash.
	rv := &review.Service{DB: b.db, Store: b.deps.Store, ExportDir: b.exportDir}
	h2 := v2["hs"]
	if _, err := rv.Export(ctx, h2.ID, "test"); err == nil {
		t.Fatal("export of an unapproved history video succeeded")
	}
	if _, err := rv.Decide(ctx, h2.ID, strings.Repeat("0", 64), review.Approved, "", "test"); err == nil {
		t.Fatal("approval of the history video with a stale hash succeeded")
	}
	if _, err := rv.Decide(ctx, h2.ID, h2.SHA256, review.Approved, "ok", "test"); err != nil {
		t.Fatal(err)
	}
	e, err := rv.Export(ctx, h2.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := artifacts.HashFile(e.Path); err != nil || got != h2.SHA256 || e.SHA256 != h2.SHA256 {
		t.Fatalf("exported file hash %s (record %s) %v, approved %s", got, e.SHA256, err, h2.SHA256)
	}
	// Approving version 2 approves nothing else: version 1 is still not exportable.
	if _, err := rv.Export(ctx, hv.ID, "test"); err == nil {
		t.Fatal("export of the unapproved history version 1 succeeded")
	}

	b.assertNoGPUOverlap(t)
	if b.cloud.Calls() != 0 {
		t.Fatalf("cloud adapter called %d times", b.cloud.Calls())
	}
}

// The real QA stage on a History run: the qa.go dispatch picks the History rule set from the script
// artifact, the video is probed with ffprobe, the render spec is read, and the report is bound to the
// exact video. A newer source version only warns; a source that changed on disk fails the run closed.
func TestHistoryQAStageRunsOnTheRenderedVideoAndFailsClosed(t *testing.T) {
	b := newThree(t)
	ctx := context.Background()
	b.start(t)
	run := b.createRun(t, b.hsEp, "full", nil)
	if s := b.wait(t, run)[run]; s != string(jobs.RunReadyForReview) {
		t.Fatalf("history run is %s", s)
	}
	v := b.video(t, run)
	reports, _ := artifacts.ForRun(ctx, b.db, run, artifacts.QAReport)
	if len(reports) != 1 || reports[0].Model != "qa-rules-history-v1" || reports[0].Meta["passed"] != true {
		t.Fatalf("qa report %+v", reports)
	}
	rows, err := b.db.Query(ctx, `SELECT rule, severity, passed FROM qa_results WHERE artifact_id = $1 AND rule LIKE 'history\_%'`, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for rows.Next() {
		var rule, sev string
		var ok bool
		if err := rows.Scan(&rule, &sev, &ok); err != nil {
			t.Fatal(err)
		}
		got[rule] = ok
	}
	rows.Close()
	for _, r := range historyRuleNames {
		if ok, found := got[r]; !found || !ok {
			t.Fatalf("rule %s present=%v passed=%v", r, found, ok)
		}
	}

	// A newer version of s1: the script still rests on version 1 of it. Only the warning fails.
	b.hh.addSource(t, "s1", "Tower facts", historySource1+" ข้อความเพิ่ม")
	re1 := b.createRun(t, b.hsEp, "rerender", &run)
	if s := b.wait(t, re1)[re1]; s != string(jobs.RunReadyForReview) {
		t.Fatalf("rerender with a newer source is %s", s)
	}
	v2 := b.video(t, re1)
	var sev string
	var passed bool
	if err := b.db.QueryRow(ctx, `SELECT severity, passed FROM qa_results WHERE artifact_id = $1 AND rule = 'history_sources_current'`, v2.ID).Scan(&sev, &passed); err != nil {
		t.Fatal(err)
	}
	if sev != "warning" || passed || b.status(t, v2) != "qa_passed" {
		t.Fatalf("newer source: severity %s passed %v status %s", sev, passed, b.status(t, v2))
	}

	// The source version the script used changes on disk: blocking, the run fails, the video is not passed.
	var path string
	if err := b.db.QueryRow(ctx, `SELECT storage_path FROM artifacts WHERE episode_id = $1 AND type = 'source' AND item_key = 's1' AND version = 1`, b.hsEp).Scan(&path); err != nil {
		t.Fatal(err)
	}
	abs, err := b.deps.Store.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	re2 := b.createRun(t, b.hsEp, "rerender", &run)
	if s := b.wait(t, re2)[re2]; s != string(jobs.RunFailed) {
		t.Fatalf("rerender with a changed source is %s", s)
	}
	v3 := b.video(t, re2)
	if b.status(t, v3) != "qa_failed" {
		t.Fatalf("video status %q, want qa_failed", b.status(t, v3))
	}
	if err := b.db.QueryRow(ctx, `SELECT severity, passed FROM qa_results WHERE artifact_id = $1 AND rule = 'history_sources_intact'`, v3.ID).Scan(&sev, &passed); err != nil {
		t.Fatal(err)
	}
	if sev != "blocking" || passed {
		t.Fatalf("history_sources_intact severity %s passed %v", sev, passed)
	}
	rv := &review.Service{DB: b.db, Store: b.deps.Store, ExportDir: b.exportDir}
	if _, err := rv.Decide(ctx, v3.ID, v3.SHA256, review.Approved, "", "test"); err == nil {
		t.Fatal("a QA-failed history video was approved")
	}
	b.assertNoGPUOverlap(t)
}

// A History run whose narration is spoken too slowly fails tts with script_duration; the queue starts
// a rewrite run that carries the measured feedback, and the History scriptwriter's prompt contains it.
func TestHistoryRewriteRunFromTTSDurationCarriesFeedbackIntoThePrompt(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	var rec *recordingLLM
	h := newHistoryHarness(t, 3, recordHistory(&rec), hedgedHistoryJSON(t, 90))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &jobs.Queue{DB: h.db, Plans: pipeline.RunnablePlans(), MaxAttempts: 2, Delay: func(*jobs.StageError, int) time.Duration { return 0 }}
	reg := providers.NewRegistry(false)
	for _, r := range []providers.Registration{
		{Name: "test-llm", Kind: providers.KindLocal, Capability: providers.CapLLM, Impl: rec},
		{Name: "test-images", Kind: providers.KindLocal, Capability: providers.CapImage, Impl: &fake.Images{}},
		// 0.2 s per character: 1080 characters take far more than the 90 s target.
		{Name: "test-voice", Kind: providers.KindLocal, Capability: providers.CapTTS, Impl: &fake.Voice{SecondsPerRune: 0.2}},
		{Name: "test-segment", Kind: providers.KindLocal, Capability: providers.CapSegment, Impl: fake.Segmenter{}},
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
	h.worker = &jobs.Worker{Queue: q, ProfileStages: h.deps.RunnableStages(), Log: log}
	// The source is written through the final store (the harness's own store was replaced above).
	h.addSource(t, "s1", "tower", historySource1)
	ctx := context.Background()
	first, err := q.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if ran, err := h.worker.RunOne(ctx); err != nil {
			t.Fatal(err)
		} else if !ran {
			break
		}
	}
	var rewrite int64
	var feedback string
	if err := h.db.QueryRow(ctx, `SELECT id, coalesce(feedback, '') FROM workflow_runs WHERE episode_id = $1 AND kind = 'rewrite' AND parent_run_id = $2 ORDER BY id LIMIT 1`,
		h.ep, first).Scan(&rewrite, &feedback); err != nil {
		t.Fatalf("no rewrite run followed the too-slow tts: %v", err)
	}
	if !strings.Contains(feedback, "Spoken aloud they took") {
		t.Fatalf("rewrite feedback %q", feedback)
	}
	if len(rec.prompts) < 2 {
		t.Fatalf("history script prompts: %d", len(rec.prompts))
	}
	if strings.Contains(rec.prompts[0], "Spoken aloud they took") {
		t.Fatal("the first run's prompt already carried the feedback")
	}
	if !strings.Contains(rec.prompts[1], feedback) {
		t.Fatal("the rewrite run's History prompt does not carry the measured feedback")
	}
}
