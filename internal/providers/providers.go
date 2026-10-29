// Package providers defines the small interfaces stages use to reach local AI runtimes.
// M1a has local adapters only; there is no cloud code path.
package providers

import (
	"context"
	"encoding/json"
)

type Info struct {
	Provider string
	Model    string
}

type LLM interface {
	Info() Info
	// GenerateJSON returns raw model output constrained to schema. The caller must still validate it.
	GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage, seed int) ([]byte, error)
}

type ImageRequest struct {
	Prompt   string
	Negative string
	Seed     int64
	Width    int
	Height   int
	// Loras are applied to the model (U-Net) in order. Empty means the plain checkpoint.
	Loras []LoraRef
}

// LoraRef names a LoRA file in the image server's loras folder and the strength to apply it at.
type LoraRef struct {
	Name     string  `json:"name"`
	Strength float64 `json:"strength"`
}

type ImageGen interface {
	Info() Info
	// Generate returns PNG bytes.
	Generate(ctx context.Context, req ImageRequest) ([]byte, error)
}

type Voice interface {
	Info() Info
	// Design creates a new voice from a text description and speaks text with it. Returns WAV bytes.
	Design(ctx context.Context, description, text string, seed int) ([]byte, error)
	// Clone speaks text in the voice of a reference WAV. Returns WAV bytes.
	Clone(ctx context.Context, reference []byte, text string, seed int) ([]byte, error)
}

type Segmenter interface {
	// Segment splits Thai text into words, keeping spaces and punctuation as their own tokens.
	Segment(ctx context.Context, text string) ([]string, error)
}

// GPUHolder is a runtime that may keep a model resident in VRAM. Only one may hold the GPU at a time.
type GPUHolder interface {
	ReleaseGPU(ctx context.Context) error
}

// GPU hands the single GPU slot to one holder by asking every other holder to unload first.
type GPU struct {
	Holders []GPUHolder
}

func (g *GPU) Acquire(ctx context.Context, user GPUHolder) error {
	for _, h := range g.Holders {
		if h == user {
			continue
		}
		if err := h.ReleaseGPU(ctx); err != nil {
			return err
		}
	}
	return nil
}
