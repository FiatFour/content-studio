package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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

func (c *ComfyUI) graph(req ImageRequest) map[string]any {
	return map[string]any{
		"1": node("CheckpointLoaderSimple", map[string]any{"ckpt_name": c.Checkpoint}),
		"2": node("CLIPTextEncode", map[string]any{"clip": []any{"1", 1}, "text": req.Prompt}),
		"3": node("CLIPTextEncode", map[string]any{"clip": []any{"1", 1}, "text": req.Negative}),
		"4": node("EmptyLatentImage", map[string]any{"width": req.Width, "height": req.Height, "batch_size": 1}),
		"5": node("KSampler", map[string]any{
			"model": []any{"1", 0}, "positive": []any{"2", 0}, "negative": []any{"3", 0}, "latent_image": []any{"4", 0},
			"seed": req.Seed, "steps": c.Steps, "cfg": c.CFG, "sampler_name": c.Sampler, "scheduler": c.Scheduler, "denoise": 1.0,
		}),
		"6": node("VAEDecode", map[string]any{"samples": []any{"5", 0}, "vae": []any{"1", 2}}),
		"7": node("SaveImage", map[string]any{"images": []any{"6", 0}, "filename_prefix": "studio/img"}),
	}
}

func node(class string, inputs map[string]any) map[string]any {
	return map[string]any{"class_type": class, "inputs": inputs}
}

func (c *ComfyUI) Generate(ctx context.Context, req ImageRequest) ([]byte, error) {
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
