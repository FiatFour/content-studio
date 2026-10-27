package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/media"
	"github.com/fiat/content-studio/internal/providers"
)

const renderSpecVersion = 1

// RenderSpec is the versioned timeline FFmpeg executes. It is stored as an artifact before rendering.
type RenderSpec struct {
	Version    int          `json:"version"`
	Output     OutputSpec   `json:"output"`
	Transition string       `json:"transition"`
	Shots      []ShotSpec   `json:"shots"`
	Dialogue   AssetRef     `json:"dialogue"`
	Subtitles  SubtitleSpec `json:"subtitles"`
}

type OutputSpec struct {
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	FPS          int     `json:"fps"`
	VideoCodec   string  `json:"video_codec"`
	CRF          int     `json:"crf"`
	AudioCodec   string  `json:"audio_codec"`
	AudioBitrate string  `json:"audio_bitrate"`
	LoudnessLUFS float64 `json:"loudness_lufs"`
	DurationMS   int64   `json:"duration_ms"`
}

type AssetRef struct {
	ArtifactID int64  `json:"artifact_id"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
}

type ShotSpec struct {
	Index   int      `json:"index"`
	Image   AssetRef `json:"image"`
	StartMS int64    `json:"start_ms"`
	EndMS   int64    `json:"end_ms"`
	Camera  Camera   `json:"camera"`
}

type Camera struct {
	Preset string  `json:"preset"`
	Zoom   float64 `json:"zoom"`
}

type SubtitleSpec struct {
	AssetRef
	Font     string `json:"font"`
	Size     int    `json:"size"`
	MarginV  int    `json:"margin_v"`
	MarginL  int    `json:"margin_l"`
	MarginR  int    `json:"margin_r"`
	Outline  int    `json:"outline"`
	Language string `json:"language"`
}

var renderInfo = providers.Info{Provider: "ffmpeg", Model: "render-spec-v1"}

// cameraPresets are the moves the render supports. Anything else is reported as unsupported.
var cameraPresets = map[string]bool{"static": true, "zoom_in": true, "zoom_out": true, "pan_left": true, "pan_right": true, "pan_up": true, "pan_down": true}

// zoompan returns the zoom and position expressions for a preset over n frames.
func zoompan(c Camera, n int) (z, x, y string, err error) {
	if !cameraPresets[c.Preset] {
		return "", "", "", jobs.Errorf(jobs.ErrUnsupported, nil, "camera move %q is not supported", c.Preset)
	}
	a := c.Zoom
	frames := max(n-1, 1)
	cx, cy := "iw/2-(iw/zoom/2)", "ih/2-(ih/zoom/2)"
	switch c.Preset {
	case "static":
		return "1", cx, cy, nil
	case "zoom_in":
		return fmt.Sprintf("1+(%g-1)*on/%d", a, frames), cx, cy, nil
	case "zoom_out":
		return fmt.Sprintf("%g-(%g-1)*on/%d", a, a, frames), cx, cy, nil
	case "pan_left":
		return fmt.Sprintf("%g", a), fmt.Sprintf("(iw-iw/zoom)*(1-on/%d)", frames), cy, nil
	case "pan_right":
		return fmt.Sprintf("%g", a), fmt.Sprintf("(iw-iw/zoom)*on/%d", frames), cy, nil
	case "pan_up":
		return fmt.Sprintf("%g", a), cx, fmt.Sprintf("(ih-ih/zoom)*(1-on/%d)", frames), nil
	default: // pan_down
		return fmt.Sprintf("%g", a), cx, fmt.Sprintf("(ih-ih/zoom)*on/%d", frames), nil
	}
}

func ref(a artifacts.Artifact) AssetRef {
	return AssetRef{ArtifactID: a.ID, Path: a.StoragePath, SHA256: a.SHA256}
}

func (d *Deps) buildSpec(ctx context.Context, j *jobs.Job) (RenderSpec, []int64, error) {
	tlArts, err := d.inputs(ctx, j, artifacts.Timeline)
	if err != nil {
		return RenderSpec{}, nil, err
	}
	tlArt := tlArts[len(tlArts)-1]
	data, err := d.read(tlArt)
	if err != nil {
		return RenderSpec{}, nil, err
	}
	var tl Timeline
	if err := json.Unmarshal(data, &tl); err != nil {
		return RenderSpec{}, nil, err
	}
	images, err := d.inputs(ctx, j, artifacts.Image)
	if err != nil {
		return RenderSpec{}, nil, err
	}
	if len(images) != len(tl.Shots) {
		return RenderSpec{}, nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "timeline has %d shots but %d images exist", len(tl.Shots), len(images))
	}
	dlg, err := d.inputs(ctx, j, artifacts.Dialogue)
	if err != nil {
		return RenderSpec{}, nil, err
	}
	subs, err := d.inputs(ctx, j, artifacts.Subtitles)
	if err != nil {
		return RenderSpec{}, nil, err
	}
	st := d.Settings
	spec := RenderSpec{
		Version:    renderSpecVersion,
		Output:     OutputSpec{Width: st.OutWidth, Height: st.OutHeight, FPS: st.FPS, VideoCodec: "libx264", CRF: 20, AudioCodec: "aac", AudioBitrate: "192k", LoudnessLUFS: -14, DurationMS: tl.TotalMS},
		Transition: "cut",
		Dialogue:   ref(dlg[len(dlg)-1]),
		Subtitles: SubtitleSpec{AssetRef: ref(subs[len(subs)-1]), Font: st.SubtitleFont, Size: 14, MarginV: 70, MarginL: 24, MarginR: 48, Outline: 2,
			Language: "th"},
	}
	parents := []int64{tlArt.ID, spec.Dialogue.ArtifactID, spec.Subtitles.ArtifactID}
	for i, sh := range tl.Shots {
		zoom := 1.15
		if sh.Camera == "static" {
			zoom = 1
		}
		spec.Shots = append(spec.Shots, ShotSpec{Index: i, Image: ref(images[i]), StartMS: sh.StartMS, EndMS: sh.EndMS, Camera: Camera{Preset: sh.Camera, Zoom: zoom}})
		parents = append(parents, images[i].ID)
	}
	return spec, parents, nil
}

// validateSpec checks the spec before any encoding: assets exist and match their hashes,
// durations are positive, and the shots cover the dialogue exactly.
func (d *Deps) validateSpec(spec RenderSpec) error {
	refs := []AssetRef{spec.Dialogue, spec.Subtitles.AssetRef}
	var end int64
	for _, sh := range spec.Shots {
		if sh.EndMS <= sh.StartMS {
			return jobs.Errorf(jobs.ErrInvalidInput, nil, "shot %d has no duration", sh.Index+1)
		}
		if sh.StartMS != end {
			return jobs.Errorf(jobs.ErrInvalidInput, nil, "shot %d does not start where the previous one ends", sh.Index+1)
		}
		if _, _, _, err := zoompan(sh.Camera, 2); err != nil {
			return err
		}
		end = sh.EndMS
		refs = append(refs, sh.Image)
	}
	if end != spec.Output.DurationMS {
		return jobs.Errorf(jobs.ErrInvalidInput, nil, "shots cover %d ms but the dialogue is %d ms", end, spec.Output.DurationMS)
	}
	for _, r := range refs {
		got, err := d.Store.Hash(r.Path)
		if err != nil {
			return jobs.Errorf(jobs.ErrInvalidInput, err, "render asset %d is missing", r.ArtifactID)
		}
		if got != r.SHA256 {
			return jobs.Errorf(jobs.ErrInvalidInput, nil, "render asset %d does not match its recorded hash", r.ArtifactID)
		}
	}
	return nil
}

func frameAt(ms int64, fps int) int { return int(math.Round(float64(ms) * float64(fps) / 1000)) }

type renderStage struct{ d *Deps }

func (s *renderStage) Run(ctx context.Context, j *jobs.Job, progress func(int, int)) (func(pgx.Tx) error, error) {
	d := s.d
	if err := d.Queue.SetProvider(ctx, j.ID, renderInfo.Provider, renderInfo.Model); err != nil {
		return nil, err
	}
	spec, parents, err := d.buildSpec(ctx, j)
	if err != nil {
		return nil, err
	}
	if err := d.validateSpec(spec); err != nil {
		return nil, err
	}
	work, cleanup, err := d.workDir(j)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	out := d.newOutputs(j)
	specJSON, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	specArt, err := out.bytes(artifacts.RenderSpec, "", specJSON, renderInfo, map[string]any{"version": spec.Version}, parents)
	if err != nil {
		return nil, err
	}
	o := spec.Output
	steps := len(spec.Shots) + 1
	var list strings.Builder
	for i, sh := range spec.Shots {
		progress(i, steps)
		n := frameAt(sh.EndMS, o.FPS) - frameAt(sh.StartMS, o.FPS)
		z, x, y, err := zoompan(sh.Camera, n)
		if err != nil {
			return nil, err
		}
		img, err := d.Store.Abs(sh.Image.Path)
		if err != nil {
			return nil, err
		}
		clip := fmt.Sprintf("clip%03d.mp4", i)
		// zoompan samples the still at its own resolution, so upscale 2x first for a clean zoom.
		vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase:flags=lanczos,crop=%d:%d,zoompan=z='%s':x='%s':y='%s':d=%d:s=%dx%d:fps=%d,setsar=1,format=yuv420p",
			o.Width*2, o.Height*2, o.Width*2, o.Height*2, z, x, y, n, o.Width, o.Height, o.FPS)
		if stderr, err := d.Tools.Run(ctx, work, "-i", img, "-vf", vf, "-frames:v", fmt.Sprint(n), "-c:v", "libx264", "-preset", "veryfast", "-crf", "16", "-an", clip); err != nil {
			return nil, renderErr(ctx, err, stderr, fmt.Sprintf("shot %d", i+1))
		}
		fmt.Fprintf(&list, "file '%s'\n", clip)
	}
	progress(len(spec.Shots), steps)
	if err := os.WriteFile(filepath.Join(work, "clips.txt"), []byte(list.String()), 0o644); err != nil {
		return nil, err
	}
	for name, r := range map[string]AssetRef{"dialogue.wav": spec.Dialogue, "subs.srt": spec.Subtitles.AssetRef} {
		src, err := d.Store.Abs(r.Path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(work, name), data, 0o644); err != nil {
			return nil, err
		}
	}
	sub := spec.Subtitles
	style := fmt.Sprintf("FontName=%s,FontSize=%d,PrimaryColour=&H00FFFFFF,OutlineColour=&H00000000,BorderStyle=1,Outline=%d,Shadow=0,Alignment=2,MarginV=%d,MarginL=%d,MarginR=%d",
		sub.Font, sub.Size, sub.Outline, sub.MarginV, sub.MarginL, sub.MarginR)
	args := []string{
		"-f", "concat", "-safe", "0", "-i", "clips.txt", "-i", "dialogue.wav",
		"-map", "0:v:0", "-map", "1:a:0",
		"-vf", fmt.Sprintf("subtitles=subs.srt:force_style='%s',format=yuv420p", style),
		"-af", fmt.Sprintf("loudnorm=I=%g:TP=-1.5:LRA=11", o.LoudnessLUFS),
		"-c:v", o.VideoCodec, "-preset", "medium", "-crf", fmt.Sprint(o.CRF), "-r", fmt.Sprint(o.FPS),
		"-c:a", o.AudioCodec, "-b:a", o.AudioBitrate, "-ar", "48000",
		"-t", fmt.Sprintf("%.3f", float64(o.DurationMS)/1000), "-movflags", "+faststart", "final.mp4",
	}
	if stderr, err := d.Tools.Run(ctx, work, args...); err != nil {
		return nil, renderErr(ctx, err, stderr, "final encode")
	}
	progress(steps, steps)
	meta := map[string]any{"duration_ms": o.DurationMS, "width": o.Width, "height": o.Height, "fps": o.FPS}
	if _, err := out.file(artifacts.Video, "", filepath.Join(work, "final.mp4"), renderInfo, meta, nil); err != nil {
		return nil, err
	}
	out.items[len(out.items)-1].parents = []*artifacts.Artifact{specArt}
	return out.persist(nil), nil
}

func renderErr(ctx context.Context, err error, stderr, what string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// The stderr tail can name local paths, so it goes to the server log through Err, not to the UI.
	return jobs.Errorf(jobs.ErrInternal, fmt.Errorf("%w: %s", err, media.Tail(stderr, 5)), "ffmpeg failed during %s", what)
}
