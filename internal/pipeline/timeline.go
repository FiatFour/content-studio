package pipeline

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Timeline places every shot and line on the episode clock. The subtitles stage computes it from
// measured audio; the dialogue WAV, the SRT and the render all follow it, so they cannot drift apart.
type Timeline struct {
	TotalMS int64      `json:"total_ms"`
	Shots   []ShotSpan `json:"shots"`
	Lines   []LineSpan `json:"lines"`
}

type ShotSpan struct {
	Index   int    `json:"index"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Camera  string `json:"camera"`
}

type LineSpan struct {
	Index   int    `json:"index"`
	Shot    int    `json:"shot"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
}

const (
	leadMS         = 500
	lineGapMS      = 350
	shotGapMS      = 700
	tailMS         = 1000
	emptyShotMS    = 2500
	maxExtraGapMS  = 2000
	durationTolPct = 10
)

// PlanTimeline lays lines out with fixed pauses, then lengthens the pauses (up to maxExtraGapMS each)
// to approach the target. It fails if the result is still outside the ±10% QA tolerance, so a
// script that cannot fit is rejected before the render.
func PlanTimeline(shots []FlatShot, lineDur []time.Duration, targetS int) (Timeline, error) {
	type slot struct{ shot, line int } // a stretchable pause; line == -1 means the pause after the shot
	var slots []slot
	nLines := 0
	for _, sh := range shots {
		nLines += len(sh.Lines)
	}
	if nLines != len(lineDur) {
		return Timeline{}, fmt.Errorf("timeline: %d lines in script but %d audio clips", nLines, len(lineDur))
	}
	base := int64(leadMS + tailMS)
	li := 0
	for i, sh := range shots {
		if len(sh.Lines) == 0 {
			base += emptyShotMS
		}
		for k := range sh.Lines {
			base += lineDur[li].Milliseconds()
			if k > 0 {
				base += lineGapMS
				slots = append(slots, slot{i, k})
			}
			li++
		}
		if i < len(shots)-1 {
			base += shotGapMS
		}
		slots = append(slots, slot{i, -1})
	}
	target := int64(targetS) * 1000
	tol := target * durationTolPct / 100
	if base > target+tol {
		return Timeline{}, fmt.Errorf("the dialogue runs %.1f s, longer than %d s +%d%%; shorten the script or raise the target length", float64(base)/1000, targetS, durationTolPct)
	}
	extra := map[slot]int64{}
	if want := target - base; want > 0 && len(slots) > 0 {
		per := min(want/int64(len(slots)), maxExtraGapMS)
		for _, s := range slots {
			extra[s] = per
		}
		base += per * int64(len(slots))
	}
	if base < target-tol {
		return Timeline{}, fmt.Errorf("the dialogue only fills %.1f s, shorter than %d s -%d%% even with longer pauses; lengthen the script or lower the target", float64(base)/1000, targetS, durationTolPct)
	}

	tl := Timeline{TotalMS: base}
	t := int64(leadMS)
	li = 0
	for i, sh := range shots {
		span := ShotSpan{Index: i, StartMS: t, Camera: sh.Camera}
		if i == 0 {
			span.StartMS = 0
		}
		if len(sh.Lines) == 0 {
			t += emptyShotMS
		}
		for k, l := range sh.Lines {
			if k > 0 {
				t += lineGapMS + extra[slot{i, k}]
			}
			d := lineDur[li].Milliseconds()
			tl.Lines = append(tl.Lines, LineSpan{Index: li, Shot: i, Speaker: l.Speaker, Text: l.Text, StartMS: t, EndMS: t + d})
			t += d
			li++
		}
		pause := extra[slot{i, -1}]
		if i < len(shots)-1 {
			pause += shotGapMS
		} else {
			pause += tailMS
		}
		// The cut to the next shot lands in the middle of the pause after this shot's last line.
		t += pause
		span.EndMS = t - pause/2
		if i == len(shots)-1 {
			span.EndMS = tl.TotalMS
		}
		tl.Shots = append(tl.Shots, span)
	}
	for i := 1; i < len(tl.Shots); i++ {
		tl.Shots[i].StartMS = tl.Shots[i-1].EndMS
	}
	return tl, nil
}

const zwsp = "​"

// maxCueRunes keeps a cue to about two lines on a 1080 px wide frame at the subtitle size.
const maxCueRunes = 34

// SubtitleText joins segmented words with zero-width spaces. libass breaks lines there, so Thai,
// which has no spaces inside a phrase, wraps between words instead of running off the frame.
func SubtitleText(words []string) string {
	var b strings.Builder
	prevWord := false
	for _, w := range words {
		isSpace := strings.TrimSpace(w) == ""
		if !isSpace && prevWord {
			b.WriteString(zwsp)
		}
		if isSpace {
			b.WriteString(" ")
		} else {
			b.WriteString(w)
		}
		prevWord = !isSpace
	}
	return strings.TrimSpace(b.String())
}

// splitCue breaks a long line into chunks of whole words, each at most maxCueRunes long when possible.
func splitCue(words []string) [][]string {
	var out [][]string
	var cur []string
	n := 0
	for _, w := range words {
		wl := utf8.RuneCountInString(w)
		if n > 0 && n+wl > maxCueRunes {
			out = append(out, cur)
			cur, n = nil, 0
			if strings.TrimSpace(w) == "" {
				continue
			}
		}
		cur = append(cur, w)
		n += wl
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

type Cue struct {
	StartMS, EndMS int64
	Text           string
}

// Cues turns a line span and its words into one or more cues, sharing the line's time by length.
func Cues(l LineSpan, words []string) []Cue {
	chunks := splitCue(words)
	total := 0
	for _, c := range chunks {
		total += utf8.RuneCountInString(strings.Join(c, ""))
	}
	var out []Cue
	t := l.StartMS
	for i, c := range chunks {
		n := utf8.RuneCountInString(strings.Join(c, ""))
		end := t + (l.EndMS-l.StartMS)*int64(n)/int64(max(total, 1))
		if i == len(chunks)-1 {
			end = l.EndMS
		}
		out = append(out, Cue{StartMS: t, EndMS: end, Text: SubtitleText(c)})
		t = end
	}
	return out
}

func WriteSRT(cues []Cue) string {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(c.StartMS), srtTime(c.EndMS), c.Text)
	}
	return b.String()
}

func srtTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
