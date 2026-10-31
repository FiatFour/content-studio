package pipeline

import (
	"strings"
	"testing"
)

func historyFixture() *HistoryScript {
	return &HistoryScript{
		Title:    "ปริศนาเรือโบราณ",
		Language: "th",
		Beats: []ShortsBeat{
			{Narration: "เรือลำนี้จมลงเมื่อหลายร้อยปีก่อน", ClaimIDs: []string{"c1"}},
			{Narration: "ไม่มีใครรู้ว่าเกิดอะไรขึ้นกับลูกเรือ", ClaimIDs: []string{"c2"}},
			{Narration: "ปริศนานี้ยังคงอยู่", NoFactualClaim: true},
		},
		Claims: []HistoryClaim{
			{ID: "c1", Text: "The vessel sank in a storm near the coast", Certainty: "documented",
				Sources: []HistorySource{{SourceID: "s1", Quote: "the vessel was lost in a great storm off the cape"}}},
			{ID: "c2", Text: "The fate of the crew is unknown", Certainty: "speculation",
				Sources: []HistorySource{{SourceID: "s1", Quote: "no survivor was ever recorded by the harbour"}}},
		},
	}
}

var historyVisuals = []string{
	"a wooden sailing ship tilting in rough grey waves under dark clouds",
	"an empty ship deck with coiled ropes and a lantern at dusk",
	"a sealed bottle floating on calm water near a misty shore",
}

func TestHistoryScriptAdapterUsesIllustrationStyleAndNoLora(t *testing.T) {
	hs := historyFixture()
	s, err := historyAsScript(hs, historyVisuals, []string{"Harbour records"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if s.visual == nil || s.visual.ImageKind != ImageKindIllustration {
		t.Fatalf("visual spec %+v", s.visual)
	}
	shots := s.Shots()
	if len(shots) != len(hs.Beats)+2 {
		t.Fatalf("shots %d, want %d (title card, beats, sources card)", len(shots), len(hs.Beats)+2)
	}
	for i, sh := range shots {
		p := shortsImagePrompt(s.visual, sh)
		if !strings.HasPrefix(p, historyVisualSpec.StylePrefix) {
			t.Fatalf("shot %d prompt lacks the illustration prefix: %q", i, p)
		}
		low := strings.ToLower(p)
		if strings.Contains(low, "photo of") || strings.Contains(low, "photograph") {
			t.Fatalf("shot %d prompt is evidence-style: %q", i, p)
		}
	}
	if !strings.Contains(shots[1].ImagePrompt, "wooden sailing ship") {
		t.Fatalf("beat prompt is not the visual: %q", shots[1].ImagePrompt)
	}
	if shots[0].Caption != "ปริศนาเรือโบราณ" || !strings.Contains(shots[len(shots)-1].Caption, "แหล่งข้อมูล\nHarbour records\nและอีก 2 แหล่ง") {
		t.Fatalf("cards %q / %q", shots[0].Caption, shots[len(shots)-1].Caption)
	}
	if shots[1].Camera == shots[2].Camera || shots[0].Camera != "static" {
		t.Fatalf("cameras %s %s %s", shots[0].Camera, shots[1].Camera, shots[2].Camera)
	}
	if len(s.Characters) != 1 || s.Characters[0].ID != narratorID {
		t.Fatalf("characters %+v", s.Characters)
	}
}

func TestHistoryImagePromptsNeverContainClaimQuoteOrNarrationText(t *testing.T) {
	hs := historyFixture()
	s, err := historyAsScript(hs, historyVisuals, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var forbidden []string
	for _, b := range hs.Beats {
		forbidden = append(forbidden, b.Narration)
	}
	for _, c := range hs.Claims {
		forbidden = append(forbidden, c.Text)
		for _, src := range c.Sources {
			forbidden = append(forbidden, src.Quote)
		}
	}
	for i, sh := range s.Shots() {
		p := shortsImagePrompt(s.visual, sh)
		for _, f := range forbidden {
			if strings.Contains(p, f) {
				t.Fatalf("shot %d prompt contains source text %q", i, f)
			}
		}
	}
}

func TestHistoryVisualThatRepeatsSourceTextIsRefused(t *testing.T) {
	for _, bad := range []string{
		"The vessel sank in a storm near the coast",                // claim text
		"scene: the vessel was lost in a great storm off the cape", // quote
		"เรือลำนี้จมลงเมื่อหลายร้อยปีก่อน และคลื่น",                // narration
	} {
		v := append([]string{bad}, historyVisuals[1:]...)
		_, err := historyAsScript(historyFixture(), v, nil, 0)
		if err == nil || strings.Contains(err.Error(), "vessel") {
			t.Fatalf("visual %q: err %v", bad, err)
		}
	}
}

func TestHistoryVisualEvidenceWordingAndFranchisesAreRefusedWithSanitizedErrors(t *testing.T) {
	for _, bad := range []string{
		"Photo of a ship at sea in the old days",
		"a photograph of the harbour at night",
		"real footage of the wreck on the seabed",
		"documentary photo of the crew members",
		"a ship from Futurama sailing on the waves",
		"", "ship",
	} {
		_, err := CleanHistoryVisual(bad)
		if err == nil {
			t.Fatalf("visual %q accepted", bad)
		}
		if bad != "" && strings.Contains(strings.ToLower(err.Error()), strings.ToLower(bad)) {
			t.Fatalf("error repeats the visual: %v", err)
		}
	}
	if got, err := CleanHistoryVisual("  a ship {\\an8} <b>at sea\n at dawn "); err != nil || got != "a ship an8 bat sea at dawn" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestHistoryVisualCountMustMatchBeats(t *testing.T) {
	if _, err := historyAsScript(historyFixture(), historyVisuals[:2], nil, 0); err == nil {
		t.Fatal("two visuals for three beats accepted")
	}
}

func TestMarkImageKindLabelsIllustrationsOnly(t *testing.T) {
	m := map[string]any{}
	markImageKind(m, &historyVisualSpec)
	if m["image_kind"] != "illustration" || m["evidence"] != false {
		t.Fatalf("meta %v", m)
	}
	for _, v := range []*VisualSpec{nil, {StylePrefix: "x"}} {
		m := map[string]any{}
		markImageKind(m, v)
		if len(m) != 0 {
			t.Fatalf("meta %v for %+v", m, v)
		}
	}
	if !strings.Contains(historyVisualSpec.Negative, "photograph") {
		t.Fatal("negative prompt does not exclude photographs")
	}
}
