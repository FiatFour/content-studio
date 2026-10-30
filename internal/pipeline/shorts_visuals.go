package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/providers"
)

// VisualSpec is the per-profile look and voice of a profile whose script is not a character drama
// (Knowledge Shorts, M5 T5). It is plain data: the images, tts, subtitles and render stages are the
// shared ones, and a profile with a VisualSpec only changes what they are given.
type VisualSpec struct {
	// StylePrefix starts every image prompt; Negative is the negative prompt.
	StylePrefix, Negative string
	// NarratorVoice is the one voice description used for the whole video.
	NarratorVoice string
	// SourcesHeading is the first row of the closing Sources card.
	SourcesHeading string
	// MaxSourceTitles caps the titles listed on the Sources card; the rest are counted ("and N more").
	MaxSourceTitles int
}

const (
	narratorID        = "narrator"
	maxPromptRunes    = 220
	maxCaptionRunes   = 70
	shortsCardPrompt  = "clean abstract background, soft geometric shapes, no objects"
	shortsBeatPrefix  = "infographic illustration of: "
	shortsMoreSources = "และอีก %d แหล่ง"
)

// shortsCameras cycles over the beats so a still is not always the same move. Cards are static.
var shortsCameras = []string{"zoom_in", "pan_left", "zoom_out", "pan_right"}

// cleanText makes model or owner text safe for a prompt, a spoken line or an SRT cue: control
// characters, braces (libass override tags) and angle brackets (markup) are dropped, whitespace is
// collapsed, and the result is cut to max runes.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '{' || r == '}' || r == '<' || r == '>' || r == '\\':
			return -1
		case r == '\n' || r == '\t' || unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r) || r == 0x200b || r == 0xfeff:
			return -1
		}
		return r
	}, s)
	s = normSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return strings.TrimSpace(s)
}

// promptSafe replaces text that names an existing franchise (product.md: originality).
func promptSafe(s string) string {
	low := strings.ToLower(s)
	for _, b := range bannedInPrompts {
		if strings.Contains(low, b) {
			return ""
		}
	}
	return s
}

// shortsImagePrompt is the style prefix plus the shot's prompt. It never names a character or a LoRA.
func shortsImagePrompt(v *VisualSpec, sh FlatShot) string {
	return v.StylePrefix + ", " + sh.ImagePrompt
}

// shortsAsScript converts a Knowledge Shorts script into the Script the shared stages read: one
// narrator, a title card, one shot per beat, and a closing Sources card. Nothing in it can name a LoRA.
func shortsAsScript(ss *ShortsScript, v VisualSpec, sourceTitles []string, extraSources int) *Script {
	title := cleanText(ss.Title, maxCaptionRunes)
	s := &Script{
		Title:      title,
		Characters: []Character{{ID: narratorID, Name: "Narrator", Voice: v.NarratorVoice}},
		visual:     &v,
	}
	sc := Scene{Setting: "flat infographic"}
	sc.Shots = append(sc.Shots, Shot{ImagePrompt: shortsCardPrompt, Camera: "static", Caption: title})
	for i, b := range ss.Beats {
		text := cleanText(b.Narration, 300)
		p := promptSafe(cleanText(text, maxPromptRunes))
		if p == "" {
			p = shortsCardPrompt
		}
		sc.Shots = append(sc.Shots, Shot{ImagePrompt: shortsBeatPrefix + p, Camera: shortsCameras[i%len(shortsCameras)],
			Lines: []Line{{Speaker: narratorID, Text: text}}})
	}
	rows := []string{v.SourcesHeading}
	for _, t := range sourceTitles {
		rows = append(rows, t)
	}
	if extraSources > 0 {
		rows = append(rows, fmt.Sprintf(shortsMoreSources, extraSources))
	}
	sc.Shots = append(sc.Shots, Shot{ImagePrompt: shortsCardPrompt, Camera: "static", Caption: strings.Join(rows, "\n")})
	s.Scenes = []Scene{sc}
	return s
}

// shortsScript loads a stored Knowledge Shorts script (already hash-checked bytes) and adapts it. The
// Sources card lists the titles of the source versions the script was written from, taken from the
// stored source artifacts named in the script's metadata.
func (d *Deps) shortsScript(ctx context.Context, a artifacts.Artifact, data []byte) (*Script, error) {
	var ss ShortsScript
	if err := json.Unmarshal(data, &ss); err != nil {
		return nil, err
	}
	var ids []int64
	if used, ok := a.Meta["sources"].([]any); ok {
		for _, u := range used {
			if m, ok := u.(map[string]any); ok {
				if n, ok := m["artifact_id"].(float64); ok {
					ids = append(ids, int64(n))
				}
			}
		}
	}
	type src struct{ key, title string }
	var found []src
	if len(ids) > 0 {
		rows, err := d.DB.Query(ctx, `SELECT item_key, coalesce(meta->>'title', '') FROM artifacts WHERE id = ANY($1) AND type = 'source'`, ids)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var x src
			if err := rows.Scan(&x.key, &x.title); err != nil {
				return nil, err
			}
			found = append(found, x)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	num := func(s string) int { n, _ := strconv.Atoi(strings.TrimPrefix(s, "s")); return n }
	sort.Slice(found, func(i, j int) bool { return num(found[i].key) < num(found[j].key) })
	v := *knowledgeShortsSpec.Visual
	var titles []string
	for _, x := range found {
		t := cleanText(x.title, maxCaptionRunes)
		if t == "" {
			t = x.key
		}
		titles = append(titles, t)
	}
	extra := 0
	if len(titles) > v.MaxSourceTitles {
		extra = len(titles) - v.MaxSourceTitles
		titles = titles[:v.MaxSourceTitles]
	}
	return shortsAsScript(&ss, v, titles, extra), nil
}

// addCardCues adds one cue per captioned shot (a Knowledge Shorts title or sources card) over that
// shot's span, then orders the cues by start time. A script with no captions is returned unchanged.
func addCardCues(ctx context.Context, seg providers.Segmenter, shots []FlatShot, tl Timeline, cues []Cue) ([]Cue, error) {
	added := false
	for i, sh := range shots {
		if sh.Caption == "" {
			continue
		}
		var rows []string
		for _, row := range strings.Split(sh.Caption, "\n") {
			words, err := seg.Segment(ctx, row)
			if err != nil {
				return nil, err
			}
			rows = append(rows, SubtitleText(words))
		}
		cues = append(cues, Cue{StartMS: tl.Shots[i].StartMS, EndMS: tl.Shots[i].EndMS, Text: strings.Join(rows, "\n")})
		added = true
	}
	if added {
		sort.SliceStable(cues, func(a, b int) bool { return cues[a].StartMS < cues[b].StartMS })
	}
	return cues, nil
}
