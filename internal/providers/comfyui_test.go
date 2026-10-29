package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func testComfy() *ComfyUI {
	return &ComfyUI{Checkpoint: "ck.safetensors", Steps: 8, CFG: 2, Sampler: "euler", Scheduler: "karras", Client: http.DefaultClient}
}

var baseReq = ImageRequest{Prompt: "p", Negative: "n", Seed: 7, Width: 64, Height: 128}

func TestGraphWithoutLorasIsTheOriginalGraph(t *testing.T) {
	want := map[string]any{
		"1": node("CheckpointLoaderSimple", map[string]any{"ckpt_name": "ck.safetensors"}),
		"2": node("CLIPTextEncode", map[string]any{"clip": []any{"1", 1}, "text": "p"}),
		"3": node("CLIPTextEncode", map[string]any{"clip": []any{"1", 1}, "text": "n"}),
		"4": node("EmptyLatentImage", map[string]any{"width": 64, "height": 128, "batch_size": 1}),
		"5": node("KSampler", map[string]any{
			"model": []any{"1", 0}, "positive": []any{"2", 0}, "negative": []any{"3", 0}, "latent_image": []any{"4", 0},
			"seed": int64(7), "steps": 8, "cfg": 2.0, "sampler_name": "euler", "scheduler": "karras", "denoise": 1.0,
		}),
		"6": node("VAEDecode", map[string]any{"samples": []any{"5", 0}, "vae": []any{"1", 2}}),
		"7": node("SaveImage", map[string]any{"images": []any{"6", 0}, "filename_prefix": "studio/img"}),
	}
	if got := testComfy().graph(baseReq); !reflect.DeepEqual(got, want) {
		t.Fatalf("graph changed without LoRAs:\n got %v\nwant %v", got, want)
	}
}

func samplerModel(g map[string]any) []any {
	return g["5"].(map[string]any)["inputs"].(map[string]any)["model"].([]any)
}

func TestGraphChainsOneModelOnlyNodePerLora(t *testing.T) {
	req := baseReq
	req.Loras = []LoraRef{{"a.safetensors", 0.8}, {"b.safetensors", 0.6}}
	g := testComfy().graph(req)
	if len(g) != 9 {
		t.Fatalf("want 9 nodes, got %d", len(g))
	}
	n10 := g["10"].(map[string]any)
	in10 := n10["inputs"].(map[string]any)
	if n10["class_type"] != "LoraLoaderModelOnly" || in10["lora_name"] != "a.safetensors" || in10["strength_model"] != 0.8 ||
		!reflect.DeepEqual(in10["model"], []any{"1", 0}) {
		t.Fatalf("node 10: %v", n10)
	}
	in11 := g["11"].(map[string]any)["inputs"].(map[string]any)
	if in11["lora_name"] != "b.safetensors" || in11["strength_model"] != 0.6 || !reflect.DeepEqual(in11["model"], []any{"10", 0}) {
		t.Fatalf("node 11: %v", in11)
	}
	if !reflect.DeepEqual(samplerModel(g), []any{"11", 0}) {
		t.Fatalf("sampler model %v", samplerModel(g))
	}
	// The text encoders still read CLIP straight from the checkpoint.
	if !reflect.DeepEqual(g["2"].(map[string]any)["inputs"].(map[string]any)["clip"], []any{"1", 1}) {
		t.Fatal("CLIP path must not go through a LoRA")
	}
	req.Loras = req.Loras[:1]
	if g := testComfy().graph(req); len(g) != 8 || !reflect.DeepEqual(samplerModel(g), []any{"10", 0}) {
		t.Fatalf("one LoRA: %d nodes, sampler model %v", len(g), samplerModel(g))
	}
}

func TestGenerateRejectsUnsafeLorasBeforeAnyRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c := testComfy()
	c.BaseURL = srv.URL
	for _, l := range []LoraRef{
		{"../x.safetensors", 0.8}, {"a/b.safetensors", 0.8}, {`a\b.safetensors`, 0.8}, {"/etc/passwd", 0.8},
		{"a..b", 0.8}, {"", 0.8}, {"ok.safetensors", 0}, {"ok.safetensors", -1}, {"ok.safetensors", 5},
	} {
		req := baseReq
		req.Loras = []LoraRef{l}
		if _, err := c.Generate(context.Background(), req); err == nil {
			t.Errorf("%+v accepted", l)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("server was contacted %d times", hits.Load())
	}
}

func TestMissingLoraIsAStageErrorNotAFallback(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Value not in list: lora_name: 'gone.safetensors' /home/secret/path"}}`))
	}))
	defer srv.Close()
	c := testComfy()
	c.BaseURL = srv.URL
	req := baseReq
	req.Loras = []LoraRef{{"gone.safetensors", 0.8}}
	_, err := c.Generate(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "/home/secret") {
		t.Fatalf("server text leaked: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("expected one attempt with the LoRA and no retry without it, got %d", posts.Load())
	}
}
