package pipeline_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/pipeline"
)

var historySrcs = []pipeline.SourceText{
	{ID: "s1", Text: "เมืองนี้ถูกสร้างขึ้นเมื่อปี 1350 ตามบันทึกของวัด"},
	{ID: "s2", Text: "นักประวัติศาสตร์บางคนเชื่อว่าเมืองถูกทิ้งร้างเพราะโรคระบาด"},
}

func hs(id, q string) pipeline.HistorySource { return pipeline.HistorySource{SourceID: id, Quote: q} }

// historyDoc builds a valid script as a generic JSON document, so tests can add or delete raw keys.
func historyDoc(t *testing.T, secs int, mut func(*pipeline.HistoryScript)) map[string]any {
	t.Helper()
	s := pipeline.HistoryScript{Title: "เมืองที่หายไป", Language: "th",
		Claims: []pipeline.HistoryClaim{{ID: "c1", Text: "built in 1350", Certainty: "documented", DateText: "1350",
			Sources: []pipeline.HistorySource{hs("s1", "ถูกสร้างขึ้นเมื่อปี 1350")}}}}
	per := strings.Repeat("ก", secs*12/8)
	for i := 0; i < 8; i++ {
		b := pipeline.ShortsBeat{Narration: per, ClaimIDs: []string{}, NoFactualClaim: true, Visual: "a quiet misty harbour at dawn, ships at anchor"}
		if i == 1 {
			b.ClaimIDs, b.NoFactualClaim = []string{"c1"}, false
		}
		s.Beats = append(s.Beats, b)
	}
	if mut != nil {
		mut(&s)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func claim0(doc map[string]any) map[string]any { return doc["claims"].([]any)[0].(map[string]any) }

func TestValidateHistoryScript(t *testing.T) {
	setSrc := func(srcs ...pipeline.HistorySource) func(*pipeline.HistoryScript) {
		return func(s *pipeline.HistoryScript) { s.Claims[0].Sources = srcs }
	}
	setCert := func(c string) func(*pipeline.HistoryScript) {
		return func(s *pipeline.HistoryScript) { s.Claims[0].Certainty = c }
	}
	tests := []struct {
		name   string
		secs   int // narration size
		target int
		mut    func(*pipeline.HistoryScript)
		edit   func(map[string]any) // raw edit of the JSON document
		want   string               // error substring; "" = valid
	}{
		{name: "valid 90 s", secs: 90, target: 90},
		{name: "valid at 60 s", secs: 60, target: 60},
		{name: "valid at 120 s", secs: 120, target: 120},
		{name: "valid empty date_text", secs: 90, target: 90, mut: func(s *pipeline.HistoryScript) { s.Claims[0].DateText = "" }},
		{name: "valid speculation", secs: 90, target: 90, mut: setCert("speculation")},
		{name: "valid disputed", secs: 90, target: 90, mut: setCert("disputed")},
		{name: "valid two sources", secs: 90, target: 90, mut: setSrc(hs("s1", "ปี 1350"), hs("s2", "เพราะโรคระบาด"))},
		{name: "missing certainty", secs: 90, target: 90, edit: func(d map[string]any) { delete(claim0(d), "certainty") }, want: "schema"},
		{name: "bad certainty value", secs: 90, target: 90, mut: setCert("probably"), want: "schema"},
		{name: "empty certainty", secs: 90, target: 90, mut: setCert(""), want: "schema"},
		{name: "empty sources", secs: 90, target: 90, mut: setSrc(), want: "schema"},
		{name: "null sources", secs: 90, target: 90, edit: func(d map[string]any) { claim0(d)["sources"] = nil }, want: "schema"},
		{name: "missing sources", secs: 90, target: 90, edit: func(d map[string]any) { delete(claim0(d), "sources") }, want: "schema"},
		{name: "missing date_text", secs: 90, target: 90, edit: func(d map[string]any) { delete(claim0(d), "date_text") }, want: "schema"},
		{name: "bad source id", secs: 90, target: 90, mut: setSrc(hs("../etc", "ปี 1350")), want: "schema"},
		{name: "quote too short", secs: 90, target: 90, mut: setSrc(hs("s1", "ปี")), want: "schema"},
		{name: "unknown field on script", secs: 90, target: 90, edit: func(d map[string]any) { d["claim_check"] = map[string]any{} }, want: "schema"},
		{name: "unknown field on claim", secs: 90, target: 90, edit: func(d map[string]any) { claim0(d)["confidence"] = 1 }, want: "schema"},
		{name: "unknown field on source", secs: 90, target: 90, edit: func(d map[string]any) {
			claim0(d)["sources"].([]any)[0].(map[string]any)["page"] = 3
		}, want: "schema"},
		{name: "unknown field on beat", secs: 90, target: 90, edit: func(d map[string]any) { d["beats"].([]any)[0].(map[string]any)["image"] = "x" }, want: "schema"},
		{name: "wrong language", secs: 90, target: 90, mut: func(s *pipeline.HistoryScript) { s.Language = "en" }, want: "schema"},
		{name: "too few beats", secs: 90, target: 90, mut: func(s *pipeline.HistoryScript) { s.Beats = s.Beats[:2] }, want: "schema"},
		{name: "too many beats", secs: 90, target: 90, mut: func(s *pipeline.HistoryScript) {
			for len(s.Beats) < 17 {
				s.Beats = append(s.Beats, s.Beats[0])
			}
		}, want: "schema"},
		{name: "narration not Thai", secs: 90, target: 90, mut: func(s *pipeline.HistoryScript) { s.Beats[0].Narration = strings.Repeat("a", 180) }, want: "not in Thai"},
		{name: "narration too short for 90 s", secs: 20, target: 90, want: "at least"},
		{name: "narration too long for 60 s", secs: 120, target: 60, want: "at most"},
		{name: "target below 60", secs: 45, target: 45, want: "range"},
		{name: "target above 120", secs: 130, target: 130, want: "range"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := historyDoc(t, tc.secs, tc.mut)
			if tc.edit != nil {
				tc.edit(doc)
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			got, err := pipeline.ValidateHistoryScript(raw, tc.target)
			if tc.want == "" {
				if err != nil || got == nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
	if _, err := pipeline.ValidateHistoryScript([]byte("nope"), 90); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("not json: got %v", err)
	}
}

func TestCheckHistoryClaims(t *testing.T) {
	beats := []pipeline.ShortsBeat{{Narration: "x", NoFactualClaim: true}, {Narration: "x", ClaimIDs: []string{"c1"}}}
	cl := func(srcs ...pipeline.HistorySource) []pipeline.HistoryClaim {
		return []pipeline.HistoryClaim{{ID: "c1", Text: "t", Certainty: "disputed", Sources: srcs}}
	}
	tests := []struct {
		name   string
		claims []pipeline.HistoryClaim
		want   []string
	}{
		{"valid one source", cl(hs("s1", "ปี 1350")), nil},
		{"valid two sources", cl(hs("s1", "ปี 1350"), hs("s2", "เพราะโรคระบาด")), nil},
		{"quote not in source", cl(hs("s1", "ปี 1450")), []string{pipeline.ClaimQuoteNotFound}},
		{"one of two sources has a bad quote", cl(hs("s1", "ปี 1350"), hs("s2", "สงครามกลางเมือง")), []string{pipeline.ClaimQuoteNotFound}},
		{"first of two has a bad quote", cl(hs("s1", "ปี 1450"), hs("s2", "เพราะโรคระบาด")), []string{pipeline.ClaimQuoteNotFound}},
		{"both bad", cl(hs("s1", "ปี 1450"), hs("s2", "สงครามกลางเมือง")), []string{pipeline.ClaimQuoteNotFound, pipeline.ClaimQuoteNotFound}},
		{"quote from the wrong source", cl(hs("s2", "ปี 1350")), []string{pipeline.ClaimQuoteWrongSrc}},
		{"unknown source id", cl(hs("s1", "ปี 1350"), hs("s9", "ปี 1350")), []string{pipeline.ClaimSourceUnknown}},
		{"whitespace-only difference", cl(hs("s1", "ปี   1350")), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pipeline.CheckHistoryClaims(&pipeline.HistoryScript{Beats: beats, Claims: tc.claims}, historySrcs)
			var codes []string
			for _, f := range got.Failures {
				codes = append(codes, f.Code)
				if strings.Contains(f.Message, "1350") || strings.Contains(f.Message, "โรค") {
					t.Errorf("message %q must not echo quote text", f.Message)
				}
			}
			if strings.Join(codes, ",") != strings.Join(tc.want, ",") || got.OK != (len(tc.want) == 0) {
				t.Fatalf("codes %v ok=%v, want %v", codes, got.OK, tc.want)
			}
			if got.Failures == nil {
				t.Fatal("Failures must be an empty list, not null")
			}
		})
	}
}

func TestCheckHistoryClaimsBeatRules(t *testing.T) {
	claims := []pipeline.HistoryClaim{{ID: "c1", Text: "t", Certainty: "documented", Sources: []pipeline.HistorySource{hs("s1", "ปี 1350")}}}
	got := pipeline.CheckHistoryClaims(&pipeline.HistoryScript{
		Beats:  []pipeline.ShortsBeat{{Narration: "x"}, {Narration: "x", ClaimIDs: []string{"c7"}}},
		Claims: claims}, historySrcs)
	var codes []string
	for _, f := range got.Failures {
		codes = append(codes, f.Code)
	}
	want := pipeline.BeatNoSupport + "," + pipeline.BeatUnknownClaim + "," + pipeline.ClaimUnreferenced
	if strings.Join(codes, ",") != want {
		t.Fatalf("codes %v, want %s", codes, want)
	}
}
