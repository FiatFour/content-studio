package pipeline

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/providers"
)

func loraScript() *Script {
	return &Script{Characters: []Character{
		{ID: "yai", Look: "grey bun", LoraName: "yai.safetensors", TriggerToken: "yaitok"},
		{ID: "tan", Look: "messy hair", LoraName: "tan.safetensors"},
		{ID: "pla", Look: "pink hat", LoraName: "pla.safetensors", TriggerToken: "platok"},
		{ID: "bob", Look: "plain man"},
	}}
}

func shotWith(speakers ...string) FlatShot {
	var lines []Line
	for _, s := range speakers {
		lines = append(lines, Line{Speaker: s, Text: "x"})
	}
	return FlatShot{Setting: "kitchen", Shot: Shot{ImagePrompt: "a scene", Lines: lines}}
}

func TestShotLorasCapsAtTwoInSpeakingOrder(t *testing.T) {
	s := loraScript()
	loras, tokens := shotLoras(s, shotWith("yai", "tan", "yai", "pla"), 0.8)
	want := []providers.LoraRef{{Name: "yai.safetensors", Strength: 0.8}, {Name: "tan.safetensors", Strength: 0.8}}
	if !reflect.DeepEqual(loras, want) {
		t.Fatalf("loras %v", loras)
	}
	if !reflect.DeepEqual(tokens, []string{"yaitok"}) { // tan has a LoRA but no token
		t.Fatalf("tokens %v", tokens)
	}
	p := imagePrompt(s, shotWith("yai", "tan", "yai", "pla"), tokens)
	if !(strings.Index(p, "yaitok") < strings.Index(p, "grey bun")) || !strings.Contains(p, "pink hat") || strings.Contains(p, "platok") {
		t.Fatalf("prompt %q", p)
	}
}

func TestShotLorasIgnoresCharactersWithoutLoraAndUnknownSpeakers(t *testing.T) {
	s := loraScript()
	loras, tokens := shotLoras(s, shotWith("bob", "ghost", "pla"), 0.5)
	if len(loras) != 1 || loras[0] != (providers.LoraRef{Name: "pla.safetensors", Strength: 0.5}) || !reflect.DeepEqual(tokens, []string{"platok"}) {
		t.Fatalf("%v %v", loras, tokens)
	}
	loras, tokens = shotLoras(s, shotWith("bob"), 0.8)
	if loras != nil || tokens != nil {
		t.Fatalf("%v %v", loras, tokens)
	}
	// With no LoRA the prompt is exactly the pre-LoRA prompt.
	want := strings.Join([]string{stylePrefix, "a scene", "setting: kitchen", "plain man"}, ", ")
	if got := imagePrompt(s, shotWith("bob"), nil); got != want {
		t.Fatalf("prompt %q", got)
	}
}

func TestOldScriptJSONWithoutLoraFieldsLoads(t *testing.T) {
	old := `{"title":"t","characters":[{"id":"yai","name":"n","look":"grey bun","voice":"v"}],"scenes":[{"setting":"kitchen","shots":[{"image_prompt":"a scene","camera":"static","lines":[{"speaker":"yai","text":"x"}]}]}]}`
	var s Script
	if err := json.Unmarshal([]byte(old), &s); err != nil {
		t.Fatal(err)
	}
	sh := s.Shots()[0]
	loras, tokens := shotLoras(&s, sh, 0.8)
	if len(loras) != 0 || len(tokens) != 0 || !strings.Contains(imagePrompt(&s, sh, tokens), "grey bun") {
		t.Fatalf("%v %v", loras, tokens)
	}
	out, err := json.Marshal(s)
	if err != nil || strings.Contains(string(out), "lora") {
		t.Fatalf("empty fields must not be written: %s %v", out, err)
	}
}
