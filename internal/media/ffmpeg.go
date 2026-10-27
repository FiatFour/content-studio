package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Tools runs FFmpeg and ffprobe from fixed paths with argument arrays, never a shell.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Run executes ffmpeg in dir. On failure the error carries only the last lines of stderr.
func (t Tools) Run(ctx context.Context, dir string, args ...string) (stderr string, err error) {
	cmd := exec.CommandContext(ctx, t.FFmpeg, append([]string{"-hide_banner", "-nostdin", "-y"}, args...)...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func Tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

type Probe struct {
	Streams []struct {
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		RFrameRate string `json:"r_frame_rate"`
		PixFmt     string `json:"pix_fmt"`
		Duration   string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

func (t Tools) Probe(ctx context.Context, path string) (*Probe, error) {
	out, err := exec.CommandContext(ctx, t.FFprobe, "-v", "error", "-print_format", "json", "-show_streams", "-show_format", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var p Probe
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	return &p, nil
}

func (p *Probe) DurationSeconds() float64 {
	d, _ := strconv.ParseFloat(p.Format.Duration, 64)
	return d
}

// FrameRate parses "30/1" style rates.
func FrameRate(r string) float64 {
	num, den, ok := strings.Cut(r, "/")
	if !ok {
		f, _ := strconv.ParseFloat(r, 64)
		return f
	}
	n, _ := strconv.ParseFloat(num, 64)
	d, _ := strconv.ParseFloat(den, 64)
	if d == 0 {
		return 0
	}
	return n / d
}
