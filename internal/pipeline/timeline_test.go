package pipeline

import (
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
