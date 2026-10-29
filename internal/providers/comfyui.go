package providers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fiat/content-studio/internal/jobs"
)

// ComfyUI runs the fixed text-to-image graph from decision D4 on a local ComfyUI server.
type ComfyUI struct {
	BaseURL    string
	Checkpoint string
	Steps      int
	CFG        float64
	Sampler    string
	Scheduler  string
	Client     *http.Client
}

func (c *ComfyUI) Info() Info { return Info{Provider: "comfyui", Model: c.Checkpoint} }

// graph builds the fixed graph. Each LoRA adds one LoraLoaderModelOnly node (ids "10", "11", ...) chained
// on the model path only, so the text encoder (CLIP) is never modified (clip strength 0). The sampler takes
// the model from the last LoRA node, or straight from the checkpoint when there are none.
func (c *ComfyUI) graph(req ImageRequest) map[string]any {
	model := []any{"1", 0}
	loras := map[string]any{}
	for i, l := range req.Loras {
		id := strconv.Itoa(10 + i)
		loras[id] = node("LoraLoaderModelOnly", map[string]any{"model": model, "lora_name": l.Name, "strength_model": l.Strength})
		model = []any{id, 0}
	}
	g := map[string]any{
		"1": node("CheckpointLoaderSimple", map[string]any{"ckpt_name": c.Checkpoint}),
		"2": node("CLIPTextEncode", map[string]any{"clip": []any{"1", 1}, "text": req.Prompt}),
		"3": node("CLIPTextEncode", map[string]any{"clip": []any{"1", 1}, "text": req.Negative}),
		"4": node("EmptyLatentImage", map[string]any{"width": req.Width, "height": req.Height, "batch_size": 1}),
		"5": node("KSampler", map[string]any{
			"model": model, "positive": []any{"2", 0}, "negative": []any{"3", 0}, "latent_image": []any{"4", 0},
			"seed": req.Seed, "steps": c.Steps, "cfg": c.CFG, "sampler_name": c.Sampler, "scheduler": c.Scheduler, "denoise": 1.0,
		}),
		"6": node("VAEDecode", map[string]any{"samples": []any{"5", 0}, "vae": []any{"1", 2}}),
		"7": node("SaveImage", map[string]any{"images": []any{"6", 0}, "filename_prefix": "studio/img"}),
	}
	for id, n := range loras {
		g[id] = n
	}
	return g
}

// checkLoras refuses LoRA references that could point outside the loras folder or that are unusable.
// The file's existence is not checked here: it lives in ComfyUI's folder, and ComfyUI rejects a missing
// one, which surfaces as a stage error (there is no fallback to the plain checkpoint).
func checkLoras(loras []LoraRef) error {
	for _, l := range loras {
		if l.Name == "" || strings.ContainsAny(l.Name, `/\`) || strings.Contains(l.Name, "..") {
			return jobs.Errorf(jobs.ErrInvalidInput, nil, "invalid LoRA name %q", l.Name)
		}
		if math.IsNaN(l.Strength) || math.IsInf(l.Strength, 0) || l.Strength <= 0 || l.Strength > 2 {
			return jobs.Errorf(jobs.ErrInvalidInput, nil, "invalid LoRA strength %v for %q", l.Strength, l.Name)
		}
	}
	return nil
}

func node(class string, inputs map[string]any) map[string]any {
	return map[string]any{"class_type": class, "inputs": inputs}
}

func (c *ComfyUI) Generate(ctx context.Context, req ImageRequest) ([]byte, error) {
	if err := checkLoras(req.Loras); err != nil {
		return nil, err
	}
	data, err := postJSON(ctx, c.Client, c.BaseURL+"/prompt", map[string]any{"prompt": c.graph(req)})
	if err != nil {
		return nil, err
	}
	var queued struct {
		PromptID string `json:"prompt_id"`
	}
	if err := decode(data, &queued, "comfyui prompt"); err != nil {
		return nil, err
	}
	if queued.PromptID == "" {
		return nil, fmt.Errorf("comfyui returned no prompt id")
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		data, err := get(ctx, c.Client, c.BaseURL+"/history/"+url.PathEscape(queued.PromptID))
		if err != nil {
			return nil, err
		}
		var hist map[string]struct {
			Status struct {
				StatusStr string `json:"status_str"`
				Completed bool   `json:"completed"`
			} `json:"status"`
			Outputs map[string]struct {
				Images []struct {
					Filename  string `json:"filename"`
					Subfolder string `json:"subfolder"`
					Type      string `json:"type"`
				} `json:"images"`
			} `json:"outputs"`
		}
		if err := decode(data, &hist, "comfyui history"); err != nil {
			return nil, err
		}
		h, ok := hist[queued.PromptID]
		if !ok {
			continue
		}
		if h.Status.StatusStr == "error" {
			return nil, jobs.Errorf(jobs.ErrInternal, nil, "comfyui reported an error while generating the image")
		}
		if !h.Status.Completed {
			continue
		}
		imgs := h.Outputs["7"].Images
		if len(imgs) == 0 {
			return nil, jobs.Errorf(jobs.ErrInternal, nil, "comfyui finished without an image")
		}
		q := url.Values{"filename": {imgs[0].Filename}, "subfolder": {imgs[0].Subfolder}, "type": {imgs[0].Type}}
		return get(ctx, c.Client, c.BaseURL+"/view?"+q.Encode())
	}
}

// ReleaseGPU unloads ComfyUI's models and frees its cached VRAM.
func (c *ComfyUI) ReleaseGPU(ctx context.Context) error {
	_, err := postJSON(ctx, c.Client, c.BaseURL+"/free", map[string]any{"unload_models": true, "free_memory": true})
	return err
}
