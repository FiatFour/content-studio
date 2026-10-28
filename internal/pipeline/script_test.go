package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

// scriptWith builds a valid script of the given shots, each with linesPerShot lines of lineChars Thai characters.
func scriptWith(t *testing.T, shots, linesPerShot, lineChars int) []byte {
	t.Helper()
	text := strings.Repeat("ก", lineChars)
	var sc []Shot
	for i := 0; i < shots; i++ {
		sh := Shot{ImagePrompt: "a grandmother holding a phone in a bright kitchen", Camera: "zoom_in"}
		for k := 0; k < linesPerShot; k++ {
			sh.Lines = append(sh.Lines, Line{Speaker: []string{"a", "b"}[k%2], Text: text})
		}
		sc = append(sc, sh)
	}
	raw, err := json.Marshal(Script{Title: "t",
		Characters: []Character{{ID: "a", Name: "A", Look: "old woman", Voice: "warm elderly woman"}, {ID: "b", Name: "B", Look: "old man", Voice: "gruff elderly man"}},
		Scenes:     []Scene{{Setting: "kitchen", Shots: sc}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOneLinePerShotIsRejectedWithAnActionableMessage(t *testing.T) {
	// The real 8B model wrote 8 shots with one line each in every run, so this is the case that matters.
	_, err := ValidateScript(scriptWith(t, 8, 1, 60), 60)
	if err == nil || !strings.Contains(err.Error(), "only 8 dialogue lines in 8 shots") || !strings.Contains(err.Error(), "at least 14 lines") {
		t.Fatalf("got %v", err)
	}
}

func TestTwoLinesPerShotPasses(t *testing.T) {
	if _, err := ValidateScript(scriptWith(t, 8, 2, 50), 60); err != nil {
		t.Fatal(err)
	}
}

func TestTwoLinesPerShotStillNeedsEnoughCharacters(t *testing.T) {
	// 16 lines of 20 characters is 320 characters, below 9 per second of a 60 s episode.
	_, err := ValidateScript(scriptWith(t, 8, 2, 20), 60)
	if err == nil || !strings.Contains(err.Error(), "needs at least 540") {
		t.Fatalf("got %v", err)
	}
}
