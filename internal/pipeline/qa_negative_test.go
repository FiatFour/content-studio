package pipeline_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// beforeStage runs a hook, then the real stage. It lets a test damage the rendered
// output at the exact point QA looks at it.
type beforeStage struct {
	real jobs.Stage
	hook func()
}

func (b beforeStage) Run(ctx context.Context, j *jobs.Job, progress func(int, int)) (func(pgx.Tx) error, error) {
	b.hook()
	return b.real.Run(ctx, j, progress)
}

// runWithDamage runs the whole pipeline, calling damage right before the qa stage.
// It returns the run id and the (damaged) video artifact.
func runWithDamage(t *testing.T, damage func(h *harness, runID int64)) (*harness, int64, artifacts.Artifact) {
	t.Helper()
	h := newHarness(t, fakeScript(t))
	storetest.AllowArtifactTampering(t, h.db) // the damage hooks rewrite artifact rows on purpose
	ctx := context.Background()
	runID, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.worker.Stages["qa"] = beforeStage{real: h.worker.Stages["qa"], hook: func() { damage(h, runID) }}
	h.drain(t)
	videos, err := artifacts.ForRun(ctx, h.db, runID, artifacts.Video)
	if err != nil || len(videos) != 1 {
		t.Fatalf("videos %v %v", videos, err)
	}
	return h, runID, videos[0]
}

// replaceVideo swaps the rendered file for a small ffmpeg-made one and keeps the
// artifact row consistent (hash, size), so only the property under test is wrong.
func replaceVideo(t *testing.T, h *harness, runID int64, w, ht, seconds int, video, audio bool) {
	t.Helper()
	ctx := context.Background()
	videos, err := artifacts.ForRun(ctx, h.db, runID, artifacts.Video)
	if err != nil || len(videos) != 1 {
		t.Fatalf("videos %v %v", videos, err)
	}
	path, err := h.deps.Store.Abs(videos[0].StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-v", "error", "-y"}
	if video {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=%dx%d:rate=10:duration=%d", w, ht, seconds))
	}
	if audio {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d", seconds))
	}
	if video {
		args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p")
	}
	if audio {
		args = append(args, "-c:a", "aac")
	}
	tmp := filepath.Join(t.TempDir(), "swap.mp4")
	args = append(args, "-t", fmt.Sprint(seconds), tmp)
	if out, err := exec.Command("/usr/bin/ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := artifacts.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE artifacts SET sha256 = $1, size_bytes = $2 WHERE id = $3`, sum, int64(len(data)), videos[0].ID); err != nil {
		t.Fatal(err)
	}
}

type qaRow struct {
	passed   bool
	severity string
	reason   string
}

func qaRules(t *testing.T, h *harness, videoID int64) map[string]qaRow {
	t.Helper()
	rows, err := h.db.Query(context.Background(), `SELECT rule, passed, severity, reason FROM qa_results WHERE artifact_id = $1`, videoID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]qaRow{}
	for rows.Next() {
		var rule string
		var r qaRow
		if err := rows.Scan(&rule, &r.passed, &r.severity, &r.reason); err != nil {
			t.Fatal(err)
		}
		got[rule] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// requireQAFailed checks the named blocking rule failed, the video is qa_failed (never
// qa_passed), and the run failed with the qa_failed class instead of reaching review.
func requireQAFailed(t *testing.T, h *harness, runID int64, video artifacts.Artifact, rule string) {
	t.Helper()
	rules := qaRules(t, h, video.ID)
	r, ok := rules[rule]
	if !ok {
		t.Fatalf("rule %s has no recorded result: %v", rule, rules)
	}
	if r.passed || r.severity != "blocking" {
		t.Fatalf("rule %s: passed=%v severity=%s reason=%q; want a failed blocking rule", rule, r.passed, r.severity, r.reason)
	}
	var status string
	if err := h.db.QueryRow(context.Background(), `SELECT validation_status FROM artifacts WHERE id = $1`, video.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "qa_failed" {
		t.Fatalf("video validation_status = %q, want qa_failed", status)
	}
	state, reason := h.runState(t, runID)
	if state != string(jobs.RunFailed) || !strings.Contains(reason, string(jobs.ErrQAFailed)) || !strings.Contains(reason, rule) {
		t.Fatalf("run state %q reason %q; want failed with qa_failed naming %s", state, reason, rule)
	}
}

func TestQAFailsWrongDimensions(t *testing.T) {
	h, runID, video := runWithDamage(t, func(h *harness, runID int64) {
		replaceVideo(t, h, runID, 100, 100, 60, true, true) // right fps and length, wrong size
	})
	requireQAFailed(t, h, runID, video, "dimensions")
	// Only the damaged property is blamed: the other stream rules still pass.
	rules := qaRules(t, h, video.ID)
	for _, ok := range []string{"file_exists", "decodes", "frame_rate", "video_stream", "audio_stream", "duration"} {
		if !rules[ok].passed {
			t.Errorf("rule %s should pass for a 100x100 h264/aac 60 s clip: %q", ok, rules[ok].reason)
		}
	}
}

func TestQAFailsDurationOutsideTolerance(t *testing.T) {
	for _, secs := range []int{30, 67} { // target is 60 s, tolerance +-10% (54..66)
		t.Run(fmt.Sprintf("%ds", secs), func(t *testing.T) {
			h, runID, video := runWithDamage(t, func(h *harness, runID int64) {
				replaceVideo(t, h, runID, 144, 256, secs, true, true)
			})
			requireQAFailed(t, h, runID, video, "duration")
			if r := qaRules(t, h, video.ID); !r["dimensions"].passed {
				t.Errorf("dimensions should pass: %q", r["dimensions"].reason)
			}
		})
	}
}

func TestQADurationInsideToleranceIsNotBlamed(t *testing.T) {
	// Guards the negative tests above: a 57 s clip is within tolerance, so duration must pass.
	h, _, video := runWithDamage(t, func(h *harness, runID int64) {
		replaceVideo(t, h, runID, 144, 256, 57, true, true)
	})
	if r := qaRules(t, h, video.ID)["duration"]; !r.passed {
		t.Fatalf("57 s against a 60 s target should pass duration: %q", r.reason)
	}
}

func TestQAFailsMissingStream(t *testing.T) {
	cases := []struct {
		name         string
		video, audio bool
		rule         string
	}{
		{"no audio", true, false, "audio_stream"},
		{"no video", false, true, "video_stream"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, runID, video := runWithDamage(t, func(h *harness, runID int64) {
				replaceVideo(t, h, runID, 144, 256, 60, c.video, c.audio)
			})
			requireQAFailed(t, h, runID, video, c.rule)
		})
	}
}

func TestQAFailsEmptyLineAudio(t *testing.T) {
	h, runID, video := runWithDamage(t, func(h *harness, runID int64) {
		tag, err := h.db.Exec(context.Background(), `UPDATE artifacts SET meta = jsonb_set(meta, '{duration_ms}', '0')
			WHERE id = (SELECT id FROM artifacts WHERE run_id = $1 AND type = $2 ORDER BY id LIMIT 1)`, runID, artifacts.LineAudio)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("zeroing a line clip: %v rows=%d", err, tag.RowsAffected())
		}
	})
	requireQAFailed(t, h, runID, video, "line_audio")
	rules := qaRules(t, h, video.ID)
	if !strings.HasPrefix(rules["line_audio"].reason, "1 of ") {
		t.Errorf("line_audio reason %q should say exactly one empty clip", rules["line_audio"].reason)
	}
	if !rules["dimensions"].passed || !rules["duration"].passed {
		t.Error("an empty line clip must not be blamed on the video's own rules")
	}
}
