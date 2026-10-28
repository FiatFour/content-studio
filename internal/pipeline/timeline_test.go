package pipeline

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func shotsWith(lines ...int) []FlatShot {
	var out []FlatShot
	for i, n := range lines {
		sh := FlatShot{Index: i, Shot: Shot{Camera: "zoom_in"}}
		for k := 0; k < n; k++ {
			sh.Lines = append(sh.Lines, Line{Speaker: "a", Text: "ทดสอบ"})
		}
		out = append(out, sh)
	}
	return out
}

func durs(n int, d time.Duration) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = d
	}
	return out
}

func TestTimelineStretchesPausesToReachTarget(t *testing.T) {
	tl, err := PlanTimeline(shotsWith(2, 2, 1, 0, 2), durs(7, 6*time.Second), 60)
	if err != nil {
		t.Fatal(err)
	}
	if tl.TotalMS < 54000 || tl.TotalMS > 66000 {
		t.Fatalf("total %d ms outside ±10%% of 60 s", tl.TotalMS)
	}
	if tl.Shots[0].StartMS != 0 || tl.Shots[len(tl.Shots)-1].EndMS != tl.TotalMS {
		t.Fatalf("shots must cover the whole video: %+v", tl.Shots)
	}
	for i := 1; i < len(tl.Shots); i++ {
		if tl.Shots[i].StartMS != tl.Shots[i-1].EndMS {
			t.Fatalf("gap or overlap between shots %d and %d", i-1, i)
		}
	}
	for _, l := range tl.Lines {
		sh := tl.Shots[l.Shot]
		if l.StartMS < sh.StartMS || l.EndMS > sh.EndMS {
			t.Fatalf("line %d (%d-%d) spills out of its shot (%d-%d)", l.Index, l.StartMS, l.EndMS, sh.StartMS, sh.EndMS)
		}
		if l.EndMS-l.StartMS != 6000 {
			t.Fatalf("line %d length changed", l.Index)
		}
	}
}

func TestTimelineRejectsDialogueThatCannotFit(t *testing.T) {
	if _, err := PlanTimeline(shotsWith(3, 3), durs(6, 15*time.Second), 60); err == nil || !strings.Contains(err.Error(), "longer") {
		t.Fatalf("too long: %v", err)
	}
	if _, err := PlanTimeline(shotsWith(1), durs(1, 2*time.Second), 60); err == nil || !strings.Contains(err.Error(), "shorter") {
		t.Fatalf("too short: %v", err)
	}
	if _, err := PlanTimeline(shotsWith(2), durs(1, time.Second), 60); err == nil {
		t.Fatal("clip count mismatch accepted")
	}
}

func TestSubtitleTextPutsZeroWidthSpacesBetweenWords(t *testing.T) {
	got := SubtitleText([]string{"ยาย", "ครับ", " ", "ลอง", "กด"})
	if got != "ยาย​ครับ ลอง​กด" {
		t.Fatalf("%q", got)
	}
}

func TestLongLinesSplitIntoTimedCuesAtWordBoundaries(t *testing.T) {
	words := strings.Split("ไอ้|เครื่อง|นี้|มัน|จะ|กด|ตรง|ไหน|ให้|เห็น|รูป|หลาน|ล่ะ|ลูก| |ยาย|งง|ไป|หมด|แล้ว|นะ", "|")
	cues := Cues(LineSpan{StartMS: 1000, EndMS: 5000}, words)
	if len(cues) != 2 {
		t.Fatalf("want 2 cues, got %d: %+v", len(cues), cues)
	}
	if cues[0].StartMS != 1000 || cues[1].EndMS != 5000 || cues[0].EndMS != cues[1].StartMS {
		t.Fatalf("cue times %+v", cues)
	}
	for _, w := range words {
		if strings.TrimSpace(w) == "" {
			continue
		}
		if !strings.Contains(cues[0].Text+cues[1].Text, w) {
			t.Fatalf("word %q was cut", w)
		}
	}
}

func TestSRTTimeFormat(t *testing.T) {
	if got := srtTime(3723045); got != "01:02:03,045" {
		t.Fatal(got)
	}
}

func TestDialogueFitAcceptsAScriptNearTheTarget(t *testing.T) {
	// 4 shots of 2 lines at 6 s each: 48 s of speech plus pauses is about 53 s of a 60 s target.
	if fb := dialogueFit(shotsWith(2, 2, 2, 2), durs(8, 6*time.Second), 560, 60); fb != "" {
		t.Fatalf("a script at 88%% of the target was rejected: %s", fb)
	}
}

func TestDialogueFitRejectsShortAndLongWithMeasuredNumbers(t *testing.T) {
	short := dialogueFit(shotsWith(2, 2, 2, 2), durs(8, 4*time.Second), 480, 60)
	for _, want := range []string{"480 Thai characters", "took 32 s", "15.0 characters per second", "must be 60 s", "51 to 66 s"} {
		if !strings.Contains(short, want) {
			t.Errorf("short feedback lacks %q: %s", want, short)
		}
	}
	if long := dialogueFit(shotsWith(2, 2, 2, 2), durs(8, 9*time.Second), 900, 60); long == "" || !strings.Contains(long, "took 72 s") {
		t.Fatalf("a script over 110%% was accepted: %q", long)
	}
}

func TestDialogueFitAsksForTheCharactersThatWouldHitTheTarget(t *testing.T) {
	// 480 characters took 32 s (15 per second) and the pauses add 5 s, so 97% of the 60 s target
	// (58.2 s) leaves 53.2 s of speech, which is 798 characters.
	fb := dialogueFit(shotsWith(2, 2, 2, 2), durs(8, 4*time.Second), 480, 60)
	if !strings.Contains(fb, "about 798 Thai characters") {
		t.Fatalf("expected a request for about 798 characters: %s", fb)
	}
}

func TestScriptPromptCarriesMeasuredFeedbackOnlyWhenThereIsSome(t *testing.T) {
	e := Episode{Title: "t", Idea: "ไอเดียสั้นๆ", TargetDurationS: 60}
	if p := scriptPrompt(e, ""); strings.Contains(p, "Measured feedback") {
		t.Fatal("first prompt mentions feedback")
	}
	if p := scriptPrompt(e, "write about 750 Thai characters"); !strings.Contains(p, "Measured feedback") || !strings.Contains(p, "write about 750 Thai characters") {
		t.Fatalf("feedback missing: %s", p)
	}
}

func TestDialogueFitNeverAsksForLessThanTheScriptValidatorAccepts(t *testing.T) {
	// A slow voice (8 characters per second) makes the arithmetic want fewer characters than
	// ValidateScript's own minimum; a rewrite that obeys the feedback must still be accepted.
	fb := dialogueFit(shotsWith(2, 2, 2, 2, 2, 2, 2, 2), durs(16, 2*time.Second), 256, 60)
	want := 60 * int(minCharsPerSecond)
	if !strings.Contains(fb, fmt.Sprintf("about %d Thai characters", want)) {
		t.Fatalf("want a request for the validator minimum %d: %s", want, fb)
	}
	if !strings.Contains(fb, "around 14 lines") {
		t.Fatalf("the advised lines must not fall below 1.7 per shot (14 for 8 shots): %s", fb)
	}
}

func TestDialogueFitWindowBoundaries(t *testing.T) {
	// 16 clips; overhead = 1.5 s lead and tail + 7 shot gaps (4.9 s) + 8 line gaps (2.8 s) = 9.2 s.
	// Clip lengths are whole milliseconds because the planner works in milliseconds.
	shots := shotsWith(2, 2, 2, 2, 2, 2, 2, 2)
	for _, c := range []struct {
		clip time.Duration
		fits bool
	}{
		{2613 * time.Millisecond, true},  // 51.008 s, just over 85% of 60 s
		{2612 * time.Millisecond, false}, // 50.992 s, just under
		{3550 * time.Millisecond, true},  // 66.000 s, exactly 110%
		{3551 * time.Millisecond, false}, // 66.016 s, just over
	} {
		got := dialogueFit(shots, durs(16, c.clip), 600, 60) == ""
		if got != c.fits {
			t.Errorf("clip %v: fits=%v, want %v", c.clip, got, c.fits)
		}
	}
}
