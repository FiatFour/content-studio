package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/providers/fake"
)

// A canon LoRA flows from the characters table through the stored script to the image request and meta.
func TestImageStageUsesCanonLoraAndRecordsIt(t *testing.T) {
	script := canonScript(t, []pipeline.Character{decl("yai", "x look one", "x voice one"), decl("tan", "x look two", "x voice two")}, "yai", "tan")
	h, _, pid := canonHarness(t, script)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `UPDATE characters SET lora_name = 'yai_v1.safetensors', trigger_token = 'yaiwoman' WHERE project_id = $1 AND slug = 'yai'`, pid); err != nil {
		t.Fatal(err)
	}
	// tan has no LoRA: it must be unaffected.
	imgs := &fake.Images{}
	h.setImages(imgs)
	h.deps.Settings.LoraStrength = 0.7
	runID, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.drain(t)
	if state, reason := h.runState(t, runID); state != string(jobs.RunReadyForReview) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if len(imgs.Requests) != 6 {
		t.Fatalf("want 6 image requests, got %d", len(imgs.Requests))
	}
	want := []providers.LoraRef{{Name: "yai_v1.safetensors", Strength: 0.7}}
	for _, r := range imgs.Requests {
		if len(r.Loras) != 1 || r.Loras[0] != want[0] {
			t.Fatalf("loras %v", r.Loras)
		}
		if !strings.Contains(r.Prompt, ", yaiwoman, ") || strings.Index(r.Prompt, "yaiwoman") > strings.Index(r.Prompt, "canonical grandmother") {
			t.Fatalf("trigger token missing or after the look: %q", r.Prompt)
		}
	}
	arts, err := artifacts.ForRun(ctx, h.db, runID, artifacts.Image)
	if err != nil || len(arts) != 6 {
		t.Fatalf("images %v %v", arts, err)
	}
	for _, a := range arts {
		ls, ok := a.Meta["loras"].([]any)
		if !ok || len(ls) != 1 {
			t.Fatalf("meta loras %v", a.Meta)
		}
		l := ls[0].(map[string]any)
		if l["name"] != "yai_v1.safetensors" || l["strength"] != 0.7 {
			t.Fatalf("meta lora %v", l)
		}
		if tk, _ := a.Meta["trigger_tokens"].([]any); len(tk) != 1 || tk[0] != "yaiwoman" {
			t.Fatalf("meta tokens %v", a.Meta["trigger_tokens"])
		}
	}
}

func TestImageStageWithoutLoraSendsNoLoras(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	imgs := &fake.Images{}
	h.setImages(imgs)
	ctx := context.Background()
	runID, err := h.worker.Queue.CreateRun(ctx, h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.drain(t)
	if state, reason := h.runState(t, runID); state != string(jobs.RunReadyForReview) {
		t.Fatalf("run %s: %s", state, reason)
	}
	arts, _ := artifacts.ForRun(ctx, h.db, runID, artifacts.Image)
	if len(imgs.Requests) != 6 || len(arts) != 6 {
		t.Fatalf("%d requests, %d images", len(imgs.Requests), len(arts))
	}
	for _, r := range imgs.Requests {
		if len(r.Loras) != 0 {
			t.Fatalf("loras %v", r.Loras)
		}
	}
	if _, has := arts[0].Meta["loras"]; has {
		t.Fatalf("meta should have no loras: %v", arts[0].Meta)
	}
}
