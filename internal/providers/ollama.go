package providers

import (
	"context"
	"encoding/json"
	"net/http"
)

// Ollama talks to a local Ollama server with schema-constrained JSON output.
type Ollama struct {
	BaseURL string
	Model   string
	NumCtx  int
	Client  *http.Client
}

func (o *Ollama) Info() Info { return Info{Provider: "ollama", Model: o.Model} }

func (o *Ollama) GenerateJSON(ctx context.Context, prompt string, schema json.RawMessage, seed int) ([]byte, error) {
	data, err := postJSON(ctx, o.Client, o.BaseURL+"/api/generate", map[string]any{
		"model":  o.Model,
		"prompt": prompt,
		"format": schema,
		"stream": false,
		"think":  false,
		"options": map[string]any{
			"num_ctx":     o.NumCtx,
			"temperature": 0.7,
			"seed":        seed,
		},
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Response string `json:"response"`
	}
	if err := decode(data, &resp, "ollama"); err != nil {
		return nil, err
	}
	return []byte(resp.Response), nil
}

// ReleaseGPU asks Ollama to unload the model now.
func (o *Ollama) ReleaseGPU(ctx context.Context) error {
	_, err := postJSON(ctx, o.Client, o.BaseURL+"/api/generate", map[string]any{"model": o.Model, "keep_alive": 0})
	return err
}
