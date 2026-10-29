package pipeline_test

import (
	"context"
	"io"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
)

// fullDiskOnce fails the first artifact write like a full disk does, with the path in the error
// the way os.File.Write reports it, and lets every later write through.
type fullDiskOnce struct {
	w     io.Writer
	path  string
	fired *bool
}

func (f fullDiskOnce) Write(p []byte) (int, error) {
	if !*f.fired {
		*f.fired = true
		return 0, &fs.PathError{Op: "write", Path: f.path, Err: syscall.ENOSPC}
	}
	return f.w.Write(p)
}

func TestFailureDiskFullWaitsForResourceThenRetries(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	fired := false
	h.deps.Store.WrapWriter = func(w io.Writer) io.Writer {
		return fullDiskOnce{w: w, path: "/secret/storage/root/.tmp-123", fired: &fired}
	}
	ctx := context.Background()
	runID, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := h.worker.RunOne(ctx); err != nil || !ran {
		t.Fatalf("RunOne: %v %v", ran, err)
	}
	var state, class, msg, wait string
	if err := h.db.QueryRow(ctx, `SELECT state, error_class, error_message, coalesce(wait_reason, '') FROM jobs WHERE run_id = $1 AND stage = 'script'`, runID).Scan(&state, &class, &msg, &wait); err != nil {
		t.Fatal(err)
	}
	if state != "waiting_for_resource" || class != string(jobs.ErrInsufficientLocal) || wait == "" {
		t.Fatalf("script job: state %q class %q wait %q (message %q)", state, class, wait, msg)
	}
	for _, text := range []string{msg, wait} {
		for _, bad := range []string{"/secret", ".tmp-", "no space left", "ENOSPC", "write "} {
			if strings.Contains(text, bad) {
				t.Fatalf("%q leaked into the stored reason %q", bad, text)
			}
		}
	}
	if n := h.artifactCount(t, runID); n != 0 {
		t.Fatalf("%d artifact rows after a failed write", n)
	}
	if f := storedFiles(t, h.deps.Store.Root); len(f) != 0 {
		t.Fatalf("orphan files after a failed write: %v", f)
	}
	if rs, _ := h.runState(t, runID); rs == string(jobs.RunFailed) {
		t.Fatal("run failed instead of waiting")
	}
	// The next claim wakes the waiting job (backoff is zero in the harness); the retry succeeds.
	h.drain(t)
	if rs, reason := h.runState(t, runID); rs != string(jobs.RunReadyForReview) {
		t.Fatalf("after the disk recovered: run %s: %s", rs, reason)
	}
}
