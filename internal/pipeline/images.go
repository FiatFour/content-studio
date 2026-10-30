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

// styleVersion 2: prompts may start with LoRA trigger tokens and the image meta records loras and
// trigger_tokens. Version 1 artifacts had neither and were made without LoRAs.
const styleVersion = 2

const defaultLoraStrength = 0.8

// maxLorasPerShot caps the LoRAs stacked on one image. Every extra LoRA costs VRAM and time on the 8 GB
// card, and stacked character LoRAs bleed into each other's faces, so a third speaker is drawn from the
// Look text alone.
const maxLorasPerShot = 2

const stylePrefix = "adult animated sitcom style, original character design, thick black outlines, flat cel shading, " +
	"bold saturated colors, expressive cartoon faces, clean vector look, vertical cinematic framing"

const negativePrompt = "photo, realistic, 3d render, blurry, text, letters, caption, watermark, logo, signature, extra fingers, deformed"

func shotKey(i int) string { return fmt.Sprintf("shot-%03d", i+1) }
func lineKey(i int) string { return fmt.Sprintf("line-%03d", i+1) }

// shotLoras picks the LoRAs for a shot: the distinct speakers, in order of first line, whose character
// has a LoRA name, up to maxLorasPerShot. It returns their LoRA refs and trigger tokens (set ones only).
func shotLoras(s *Script, sh FlatShot, strength float64) ([]providers.LoraRef, []string) {
	var loras []providers.LoraRef
	var tokens []string
	seen := map[string]bool{}
	for _, l := range sh.Lines {
		c, ok := s.Character(l.Speaker)
		if !ok || seen[c.ID] || c.LoraName == "" || len(loras) >= maxLorasPerShot {
			continue
		}
		seen[c.ID] = true
		loras = append(loras, providers.LoraRef{Name: c.LoraName, Strength: strength})
		if c.TriggerToken != "" {
			tokens = append(tokens, c.TriggerToken)
		}
	}
	return loras, tokens
}

// imagePrompt adds the style, then LoRA trigger tokens, then the looks of the characters who speak in the shot.
func imagePrompt(s *Script, sh FlatShot, tokens []string) string {
	parts := []string{stylePrefix, sh.ImagePrompt, "setting: " + sh.Setting}
	parts = append(parts, tokens...)
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
	images, err := resolve[providers.ImageGen](d, providers.TaskImages)
	if err != nil {
		return nil, err
	}
	if err := d.GPU.Acquire(ctx, gpuHolder(images)); err != nil {
		return nil, err
	}
	info := images.Info()
	if err := d.Queue.SetProvider(ctx, j.ID, info.Provider, info.Model); err != nil {
		return nil, err
	}
	shots := script.Shots()
	out := d.newOutputs(j)
	w, h := d.Settings.ImageWidth, d.Settings.ImageHeight
	strength := d.Settings.LoraStrength
	if strength == 0 {
		strength = defaultLoraStrength
	}
	negative := negativePrompt
	if script.visual != nil {
		negative = script.visual.Negative
	}
	for i, sh := range shots {
		progress(i, len(shots))
		var loras []providers.LoraRef
		var tokens []string
		var prompt string
		if script.visual != nil { // Knowledge Shorts: flat infographic style, no character, never a LoRA
			prompt = shortsImagePrompt(script.visual, sh)
		} else {
			loras, tokens = shotLoras(script, sh, strength)
			prompt = imagePrompt(script, sh, tokens)
		}
		seed := j.EpisodeID*1000 + int64(i)
		data, err := images.Generate(ctx, providers.ImageRequest{Prompt: prompt, Negative: negative, Seed: seed, Width: w, Height: h, Loras: loras})
		if err != nil {
			return nil, err
		}
		img, err := png.Decode(bytes.NewReader(data)) // full decode: DecodeConfig reads only the header and accepts a truncated file
		if err != nil {
			return nil, jobs.Errorf(jobs.ErrValidationFailed, err, "shot %d image is not a valid PNG", i+1)
		}
		if cfg := img.Bounds(); cfg.Dx() != w || cfg.Dy() != h {
			return nil, jobs.Errorf(jobs.ErrValidationFailed, nil, "shot %d image is %dx%d, expected %dx%d", i+1, cfg.Dx(), cfg.Dy(), w, h)
		}
		meta := map[string]any{"prompt": prompt, "negative": negative, "seed": seed, "width": w, "height": h, "style_version": styleVersion}
		if len(loras) > 0 {
			meta["loras"] = loras
			meta["trigger_tokens"] = tokens
		}
		if _, err := out.bytes(artifacts.Image, shotKey(i), data, info, meta, []int64{scriptArt.ID}); err != nil {
			return nil, err
		}
		progress(i+1, len(shots))
	}
	return out.persist(nil), nil
}
