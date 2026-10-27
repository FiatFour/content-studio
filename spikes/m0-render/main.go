// Throwaway M0 spike for decision D11. Not application code.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Move struct {
	Zoom   string  `json:"zoom"`
	Amount float64 `json:"amount"`
	Pan    string  `json:"pan"`
}

type Shot struct {
	Image    string  `json:"image"`
	Duration float64 `json:"duration"`
	Move     Move    `json:"move"`
}

type Spec struct {
	Shots     []Shot `json:"shots"`
	Audio     string `json:"audio"`
	Subtitles string `json:"subtitles"`
	Font      string `json:"font"`
	FontSize  int    `json:"font_size"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	FPS       int    `json:"fps"`
}

type Renderer interface {
	Render(ctx context.Context, s Spec, work, out string) error
}

const ffmpegBin = "/usr/bin/ffmpeg"
const pythonBin = "/usr/bin/python3"

var skillDir = filepath.Join(os.Getenv("HOME"), ".claude/skills/ffmpeg-skill/scripts")

func run(ctx context.Context, dir, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		tail := stderr.String()
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w\nstderr tail: %s\nstdout: %.600s", filepath.Base(bin), args[0], err, tail, stdout.String())
	}
	return stdout.Bytes(), nil
}

type plainFFmpeg struct{}

func (plainFFmpeg) Render(ctx context.Context, s Spec, work, out string) error {
	for _, p := range append([]string{s.Audio, s.Subtitles}, shotImages(s)...) {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("spec invalid: %w", err)
		}
	}
	srt := filepath.Join(work, "subs.srt")
	if err := copyFile(s.Subtitles, srt); err != nil {
		return err
	}
	args := []string{"-y", "-v", "error"}
	for _, sh := range s.Shots {
		args = append(args, "-i", sh.Image)
	}
	args = append(args, "-i", s.Audio)
	var fc strings.Builder
	for i, sh := range s.Shots {
		frames := int(sh.Duration * float64(s.FPS))
		up := 2
		z, x, y := zoompanExpr(sh.Move, frames)
		fmt.Fprintf(&fc, "[%d:v]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,zoompan=z='%s':x='%s':y='%s':d=%d:s=%dx%d:fps=%d,setsar=1[v%d];",
			i, s.Width*up, s.Height*up, s.Width*up, s.Height*up, z, x, y, frames, s.Width, s.Height, s.FPS, i)
	}
	for i := range s.Shots {
		fmt.Fprintf(&fc, "[v%d]", i)
	}
	style := fmt.Sprintf("FontName=%s,FontSize=%d,Outline=3,BorderStyle=1,MarginV=60,Alignment=2", s.Font, s.FontSize)
	fmt.Fprintf(&fc, "concat=n=%d:v=1:a=0,subtitles=subs.srt:force_style='%s',format=yuv420p[v]", len(s.Shots), style)
	args = append(args, "-filter_complex", fc.String(), "-map", "[v]", "-map", fmt.Sprintf("%d:a", len(s.Shots)),
		"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-r", strconv.Itoa(s.FPS),
		"-c:a", "aac", "-b:a", "160k", "-shortest", "-movflags", "+faststart", out)
	_, err := run(ctx, work, ffmpegBin, args...)
	return err
}

func zoompanExpr(m Move, frames int) (z, x, y string) {
	a := m.Amount
	if a <= 1 {
		a = 1.15
	}
	if m.Zoom == "out" {
		z = fmt.Sprintf("%g-(%g-1)*on/%d", a, a, frames)
	} else {
		z = fmt.Sprintf("1+(%g-1)*on/%d", a, frames)
	}
	x, y = "iw/2-(iw/zoom/2)", "ih/2-(ih/zoom/2)"
	switch m.Pan {
	case "left":
		x = fmt.Sprintf("(iw-iw/zoom)*(1-on/%d)", frames)
	case "right":
		x = fmt.Sprintf("(iw-iw/zoom)*on/%d", frames)
	case "up":
		y = fmt.Sprintf("(ih-ih/zoom)*(1-on/%d)", frames)
	case "down":
		y = fmt.Sprintf("(ih-ih/zoom)*on/%d", frames)
	}
	return z, x, y
}

type ffmpegSkill struct{}

func (ffmpegSkill) Render(ctx context.Context, s Spec, work, out string) error {
	var clips []string
	for i, sh := range s.Shots {
		clip := filepath.Join(work, fmt.Sprintf("clip%02d.mp4", i))
		args := []string{filepath.Join(skillDir, "insert.py"), sh.Image, "-o", clip, "--duration", fmt.Sprint(sh.Duration),
			"--width", strconv.Itoa(s.Width), "--height", strconv.Itoa(s.Height), "--fps", strconv.Itoa(s.FPS),
			"--zoom", orDefault(sh.Move.Zoom, "in"), "--zoom-amount", fmt.Sprint(orFloat(sh.Move.Amount, 1.15)), "--overwrite", "--json-brief"}
		if sh.Move.Pan != "" {
			args = append(args, "--pan", sh.Move.Pan)
		}
		if _, err := run(ctx, work, pythonBin, args...); err != nil {
			return fmt.Errorf("shot %d: %w", i, err)
		}
		clips = append(clips, clip)
	}
	joined := filepath.Join(work, "joined.mp4")
	if _, err := run(ctx, work, pythonBin, append(append([]string{filepath.Join(skillDir, "join.py")}, clips...), "-o", joined, "--transition", "none", "--overwrite", "--json-brief")...); err != nil {
		return err
	}
	voiced := filepath.Join(work, "voiced.mp4")
	if _, err := run(ctx, work, pythonBin, filepath.Join(skillDir, "audio.py"), joined, "--replace", s.Audio, "-o", voiced, "--overwrite", "--json-brief"); err != nil {
		return err
	}
	if _, err := run(ctx, work, pythonBin, filepath.Join(skillDir, "caption.py"), voiced, "--srt", s.Subtitles, "--font", s.Font,
		"--position", "bottom", "-o", out, "--overwrite", "--json-brief"); err != nil {
		return err
	}
	return nil
}

func main() {
	backend := flag.String("backend", "plain", "plain|skill")
	specPath := flag.String("spec", "spec.json", "")
	out := flag.String("o", "out.mp4", "")
	flag.Parse()
	raw, err := os.ReadFile(*specPath)
	must(err)
	var s Spec
	must(json.Unmarshal(raw, &s))
	work, err := os.MkdirTemp(filepath.Dir(must2(filepath.Abs(*out))), "work-"+*backend+"-")
	must(err)
	abs, _ := filepath.Abs(*out)
	var r Renderer = plainFFmpeg{}
	if *backend == "skill" {
		r = ffmpegSkill{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	t0 := time.Now()
	err = r.Render(ctx, s, work, abs)
	fmt.Printf("backend=%s elapsed=%.1fs\n", *backend, time.Since(t0).Seconds())
	if err != nil {
		fmt.Println("RENDER ERROR:", err)
		os.Exit(1)
	}
}

func shotImages(s Spec) []string {
	var p []string
	for _, sh := range s.Shots {
		p = append(p, sh.Image)
	}
	return p
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func orFloat(v, d float64) float64 {
	if v <= 1 {
		return d
	}
	return v
}

func must(err error) {
	if err != nil {
		fmt.Println(errors.Unwrap(err), err)
		os.Exit(2)
	}
}

func must2(s string, err error) string {
	must(err)
	return s
}
