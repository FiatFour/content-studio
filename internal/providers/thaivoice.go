package providers

import (
	"context"
	"encoding/base64"
	"net/http"
)

// ThaiVoice talks to services/thaivoice, the local Python service that wraps VoxCPM2 (D5)
// and PyThaiNLP word segmentation.
type ThaiVoice struct {
	BaseURL string
	Client  *http.Client
}

func (t *ThaiVoice) Info() Info { return Info{Provider: "thaivoice", Model: "openbmb/VoxCPM2"} }

type ttsResponse struct {
	WAV string `json:"wav_b64"`
}

func (t *ThaiVoice) speak(ctx context.Context, body map[string]any) ([]byte, error) {
	data, err := postJSON(ctx, t.Client, t.BaseURL+"/tts", body)
	if err != nil {
		return nil, err
	}
	var r ttsResponse
	if err := decode(data, &r, "thaivoice tts"); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.WAV)
}

func (t *ThaiVoice) Design(ctx context.Context, description, text string, seed int) ([]byte, error) {
	return t.speak(ctx, map[string]any{"text": text, "voice_description": description, "seed": seed})
}

func (t *ThaiVoice) Clone(ctx context.Context, reference []byte, text string, seed int) ([]byte, error) {
	return t.speak(ctx, map[string]any{"text": text, "reference_wav_b64": base64.StdEncoding.EncodeToString(reference), "seed": seed})
}

func (t *ThaiVoice) Segment(ctx context.Context, text string) ([]string, error) {
	data, err := postJSON(ctx, t.Client, t.BaseURL+"/segment", map[string]any{"text": text})
	if err != nil {
		return nil, err
	}
	var r struct {
		Words []string `json:"words"`
	}
	if err := decode(data, &r, "thaivoice segment"); err != nil {
		return nil, err
	}
	return r.Words, nil
}

func (t *ThaiVoice) ReleaseGPU(ctx context.Context) error {
	_, err := postJSON(ctx, t.Client, t.BaseURL+"/unload", map[string]any{})
	return err
}
