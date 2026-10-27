package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/providers"
)

const styleVersion = 1

const stylePrefix = "adult animated sitcom style, original character design, thick black outlines, flat cel shading, " +
	"bold saturated colors, expressive cartoon faces, clean vector look, vertical cinematic framing"

const negativePrompt = "photo, realistic, 3d render, blurry, text, letters, caption, watermark, logo, signature, extra fingers, deformed"

func shotKey(i int) string { return fmt.Sprintf("shot-%03d", i+1) }
func lineKey(i int) string { return fmt.Sprintf("line-%03d", i+1) }

// imagePrompt adds the style and the looks of the characters who speak in the shot.
func imagePrompt(s *Script, sh FlatShot) string {
	parts := []string{stylePrefix, sh.ImagePrompt, "setting: " + sh.Setting}
	seen := map[string]bool{}
	for _, l := range sh.Lines {
		if c, ok := s.Character(l.Speaker); ok && !seen[c.ID] {
			seen[c.ID] = true
			parts = append(parts, c.Look)
		}
	}
	return strings.Join(parts, ", ")
}

type imagesStage struct{ d *Deps }

func (s *imagesStage) Run(ctx context.Context, j *jobs.Job, progress func(int, int)) (func(pgx.Tx) error, error) {
	d := s.d
	script, scriptArt, err := d.loadScript(ctx, j)
	if err != nil {
		return nil, err
	}
	if err := d.GPU.Acquire(ctx, gpuHolder(d.Images)); err != nil {
		return nil, err
	}
	info := d.Images.Info()
	if err := d.Queue.SetProvider(ctx, j.ID, info.Provider, info.Model); err != nil {
		return nil, err
	}
	shots := script.Shots()
	out := d.newOutputs(j)
	w, h := d.Settings.ImageWidth, d.Settings.ImageHeight
	for i, sh := range shots {
		progress(i, len(shots))
		prompt := imagePrompt(script, sh)
		seed := j.EpisodeID*1000 + int64(i)
		data, err := d.Images.Generate(ctx, providers.ImageRequest{Prompt: prompt, Negative: negativePrompt, Seed: seed, Width: w, Height: h})
		if err != nil {
			return nil, err
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, jobs.Errorf(jobs.ErrValidationFailed, err, "shot %d image is not a valid PNG", i+1)
		}
		if cfg.Width != w || cfg.Height != h {
			return nil, jobs.Errorf(jobs.ErrValidationFailed, nil, "shot %d image is %dx%d, expected %dx%d", i+1, cfg.Width, cfg.Height, w, h)
		}
		meta := map[string]any{"prompt": prompt, "negative": negativePrompt, "seed": seed, "width": w, "height": h, "style_version": styleVersion}
		if _, err := out.bytes(artifacts.Image, shotKey(i), data, info, meta, []int64{scriptArt.ID}); err != nil {
			return nil, err
		}
		progress(i+1, len(shots))
	}
	return out.persist(nil), nil
}
