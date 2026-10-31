package pipeline

import (
	"fmt"
	"strings"
)

// History & Mystery visuals (M6 T5, decisions D20). The profile uses the shared images stage through
// the same hook as Knowledge Shorts (Script.visual): illustration stills from the existing ComfyUI
// path, no character, never a LoRA. Every still is an illustration, never evidence, and says so in
// its artifact metadata.

// ImageKindIllustration is the value of the "image_kind" metadata of a still that illustrates a
// narration beat. A still is never evidence of the event it illustrates.
const ImageKindIllustration = "illustration"

const (
	historyCardPrompt  = "empty aged parchment texture, faint engraved border, no objects"
	historyMaxVisual   = 200
	historyMinVisual   = 8
	historySourcesHead = "แหล่งข้อมูล"
)

// historyVisualSpec is the History illustration style: a muted painterly or engraving-like picture.
// The prefix is a constant, so no model or owner text can change the style.
var historyVisualSpec = VisualSpec{
	StylePrefix: "muted painterly historical illustration, engraving style, aged paper texture, " +
		"soft earthy colors, hand-drawn ink lines, no text, no people's faces, vertical framing",
	Negative: "photo, photograph, photorealistic, realistic, 3d render, camera, film grain, documentary, " +
		"text, letters, numbers, caption, watermark, logo, signature, portrait of a real person, deformed",
	NarratorVoice:   "calm clear adult narrator, neutral Thai, even pace, storytelling tone",
	SourcesHeading:  historySourcesHead,
	MaxSourceTitles: 4,
	ImageKind:       ImageKindIllustration,
}

// evidenceWording is wording that presents a still as a real photo or footage. A beat visual with it
// is refused, because the video must never pass an illustration off as evidence.
var evidenceWording = []string{"photo of", "photograph", "real footage", "documentary photo"}

// CleanHistoryVisual validates one beat's abstract visual description (the model's `visual` field,
// already schema-validated) and returns the text that may enter an image prompt. The errors are
// sanitized: they never repeat the offending text.
func CleanHistoryVisual(raw string) (string, error) {
	v := cleanText(raw, historyMaxVisual)
	if len([]rune(v)) < historyMinVisual {
		return "", fmt.Errorf("visual description is empty or too short")
	}
	low := strings.ToLower(v)
	for _, w := range evidenceWording {
		if strings.Contains(low, w) {
			return "", fmt.Errorf("visual description uses evidence-style wording; describe an illustration, not a photo or footage")
		}
	}
	if promptSafe(v) == "" {
		return "", fmt.Errorf("visual description names an existing franchise or work")
	}
	return v, nil
}

// historyAsScript converts a History script plus one visual description per beat into the Script the
// shared stages read: a narrator, a title card, one shot per beat, and a closing Sources card. Each
// beat's image prompt is the validated visual and nothing else: claim text, quotes and narration never
// enter a prompt, and a visual that repeats any of them is refused.
func historyAsScript(hs *HistoryScript, visuals []string, sourceTitles []string, extraSources int) (*Script, error) {
	if len(visuals) != len(hs.Beats) {
		return nil, fmt.Errorf("history script has %d beats but %d visual descriptions", len(hs.Beats), len(visuals))
	}
	v := historyVisualSpec
	title := cleanText(hs.Title, maxCaptionRunes)
	s := &Script{
		Title:      title,
		Characters: []Character{{ID: narratorID, Name: "Narrator", Voice: v.NarratorVoice}},
		visual:     &v,
	}
	sc := Scene{Setting: "historical illustration"}
	sc.Shots = append(sc.Shots, Shot{ImagePrompt: historyCardPrompt, Camera: "static", Caption: title})
	for i, b := range hs.Beats {
		p, err := CleanHistoryVisual(visuals[i])
		if err != nil {
			return nil, fmt.Errorf("beat %d: %w", i+1, err)
		}
		if repeatsSourceText(hs, b, p) {
			return nil, fmt.Errorf("beat %d: visual description repeats narration, a claim or a quote", i+1)
		}
		sc.Shots = append(sc.Shots, Shot{ImagePrompt: p, Camera: shortsCameras[i%len(shortsCameras)],
			Lines: []Line{{Speaker: narratorID, Text: cleanText(b.Narration, 300)}}})
	}
	rows := append([]string{v.SourcesHeading}, sourceTitles...)
	if extraSources > 0 {
		rows = append(rows, fmt.Sprintf(shortsMoreSources, extraSources))
	}
	sc.Shots = append(sc.Shots, Shot{ImagePrompt: historyCardPrompt, Camera: "static", Caption: strings.Join(rows, "\n")})
	s.Scenes = []Scene{sc}
	return s, nil
}

// repeatsSourceText reports whether the visual contains the beat narration, any claim text or any
// quote (after whitespace normalization). Too-short texts are skipped so a one-word claim cannot
// block every visual.
func repeatsSourceText(hs *HistoryScript, b ShortsBeat, visual string) bool {
	low := strings.ToLower(normSpace(visual))
	has := func(t string) bool {
		t = strings.ToLower(normSpace(t))
		return len([]rune(t)) >= historyMinVisual && strings.Contains(low, t)
	}
	if has(b.Narration) {
		return true
	}
	for _, c := range hs.Claims {
		if has(c.Text) {
			return true
		}
		for _, src := range c.Sources {
			if has(src.Quote) {
				return true
			}
		}
	}
	return false
}

// markImageKind labels a still's metadata when its profile labels stills (History & Mystery): the
// still is an illustration and not evidence. It does nothing for other profiles.
func markImageKind(meta map[string]any, v *VisualSpec) {
	if v == nil || v.ImageKind == "" {
		return
	}
	meta["image_kind"] = v.ImageKind
	meta["evidence"] = false
}
