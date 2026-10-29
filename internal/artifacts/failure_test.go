package artifacts_test

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/fiat/content-studio/internal/artifacts"
)

// TestDiskFullLeavesNoPartialFile injects ENOSPC part-way through a write (the fake writer
// fails like a full disk would). The store must return the error, leave no artifact file and
// no temp file, and a later write to the same location must still succeed.
func TestDiskFullLeavesNoPartialFile(t *testing.T) {
	s := &artifacts.Store{Root: t.TempDir()}
	loc := artifacts.Loc{EpisodeID: 1, RunID: 1, JobID: 1, Attempt: 1}
	_, err := s.Write(loc, artifacts.Video, "", func(w io.Writer) error {
		if _, err := w.Write([]byte("first half of the video")); err != nil {
			return err
		}
		return fmt.Errorf("write video: %w", syscall.ENOSPC)
	})
	if err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("want ENOSPC, got %v", err)
	}
	var left []string
	if err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			left = append(left, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("files left behind after a failed write: %v", left)
	}
	f, err := s.Write(loc, artifacts.Video, "", func(w io.Writer) error { _, err := io.WriteString(w, "whole"); return err })
	if err != nil {
		t.Fatalf("retry at the same location: %v", err)
	}
	abs, _ := s.Abs(f.RelPath)
	st, err := os.Stat(abs)
	if err != nil || st.Size() != 5 {
		t.Fatalf("retry wrote the wrong bytes: %v %v", st, err)
	}
}
