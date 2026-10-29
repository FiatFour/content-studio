package jobs

import (
	"fmt"
	"io/fs"
	"syscall"
	"testing"
)

func TestClassifyDiskFull(t *testing.T) {
	pe := func(e error) error { return &fs.PathError{Op: "write", Path: "/data/x", Err: e} }
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"ENOSPC bare", syscall.ENOSPC, ErrInsufficientLocal},
		{"EDQUOT bare", syscall.EDQUOT, ErrInsufficientLocal},
		{"ENOSPC in path error and wrapped", fmt.Errorf("write video: %w", pe(syscall.ENOSPC)), ErrInsufficientLocal},
		{"EDQUOT inside an internal StageError", Errorf(ErrInternal, pe(syscall.EDQUOT), "ffmpeg failed"), ErrInsufficientLocal},
		{"explicit class is kept", Errorf(ErrInvalidInput, pe(syscall.ENOSPC), "bad input"), ErrInvalidInput},
		{"read-only fs stays internal", pe(syscall.EROFS), ErrInternal},
		{"permission stays internal", pe(syscall.EACCES), ErrInternal},
		{"text alone does not count", fmt.Errorf("no space left on device"), ErrInternal},
	}
	for _, c := range cases {
		se := classify(c.err)
		if se.Class != c.want {
			t.Errorf("%s: class %s, want %s", c.name, se.Class, c.want)
		}
		if se.Class == ErrInsufficientLocal && se.Message != diskFullMessage {
			t.Errorf("%s: message %q", c.name, se.Message)
		}
	}
}
