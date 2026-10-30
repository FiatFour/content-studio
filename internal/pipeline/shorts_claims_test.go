package pipeline_test

import (
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/pipeline"
)

var (
	srcs = []pipeline.SourceText{
		{ID: "s1", Text: "น้ำทะเลมีความเค็มเฉลี่ย\n  ประมาณ 35 กรัมต่อลิตร และเป็นแหล่งเกลือหลัก"},
		{ID: "s2", Text: "ดาวศุกร์ร้อนกว่าดาวพุธ แม้จะอยู่ไกลดวงอาทิตย์กว่า"},
	}
	beatFact = pipeline.ShortsBeat{Narration: "x", ClaimIDs: []string{"c1"}}
	beatHook = pipeline.ShortsBeat{Narration: "x", NoFactualClaim: true}
)

func claim(id, src, quote string) pipeline.ShortsClaim {
	return pipeline.ShortsClaim{ID: id, Text: "t", SourceID: src, Quote: quote}
}

func TestCheckShortsClaims(t *testing.T) {
	tests := []struct {
		name    string
		sources []pipeline.SourceText
		beats   []pipeline.ShortsBeat
		claims  []pipeline.ShortsClaim
		want    []string // failure codes, in order
	}{
		{"valid", srcs, []pipeline.ShortsBeat{beatHook, beatFact}, []pipeline.ShortsClaim{claim("c1", "s1", "ประมาณ 35 กรัมต่อลิตร")}, nil},
		{"whitespace only difference (newline and spaces in source)", srcs, []pipeline.ShortsBeat{beatHook, beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s1", "ความเค็มเฉลี่ย ประมาณ 35")}, nil},
		{"whitespace only difference (extra tabs in quote)", srcs, []pipeline.ShortsBeat{beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s1", "  ประมาณ \t 35   กรัมต่อลิตร ")}, nil},
		{"fabricated source id", srcs, []pipeline.ShortsBeat{beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s9", "ประมาณ 35 กรัมต่อลิตร")}, []string{pipeline.ClaimSourceUnknown}},
		{"fabricated quote", srcs, []pipeline.ShortsBeat{beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s1", "น้ำทะเลมีความเค็มเฉลี่ย 50 กรัมต่อลิตร")}, []string{pipeline.ClaimQuoteNotFound}},
		{"quote from the wrong source", srcs, []pipeline.ShortsBeat{beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s2", "ประมาณ 35 กรัมต่อลิตร")}, []string{pipeline.ClaimQuoteWrongSrc}},
		{"nothing fuzzier than whitespace: case differs", []pipeline.SourceText{{ID: "s1", Text: "Water boils at 100 degrees"}},
			[]pipeline.ShortsBeat{beatFact}, []pipeline.ShortsClaim{claim("c1", "s1", "water boils at 100 degrees")}, []string{pipeline.ClaimQuoteNotFound}},
		{"nothing fuzzier than whitespace: punctuation differs", []pipeline.SourceText{{ID: "s1", Text: "Water boils, at 100 degrees"}},
			[]pipeline.ShortsBeat{beatFact}, []pipeline.ShortsClaim{claim("c1", "s1", "Water boils at 100 degrees")}, []string{pipeline.ClaimQuoteNotFound}},
		{"empty quote", srcs, []pipeline.ShortsBeat{beatFact}, []pipeline.ShortsClaim{claim("c1", "s1", " \n ")}, []string{pipeline.ClaimQuoteNotFound}},
		{"empty sources", nil, []pipeline.ShortsBeat{beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s1", "ประมาณ 35 กรัมต่อลิตร")}, []string{pipeline.ClaimSourceUnknown}},
		{"source with empty text", []pipeline.SourceText{{ID: "s1", Text: " \n"}}, []pipeline.ShortsBeat{beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s1", "ประมาณ 35")}, []string{pipeline.ClaimQuoteNotFound}},
		{"beat without claim and not marked", srcs, []pipeline.ShortsBeat{{Narration: "x"}, beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s1", "ประมาณ 35 กรัมต่อลิตร")}, []string{pipeline.BeatNoSupport}},
		{"beat with claims and marked no_factual_claim", srcs, []pipeline.ShortsBeat{{Narration: "x", ClaimIDs: []string{"c1"}, NoFactualClaim: true}},
			[]pipeline.ShortsClaim{claim("c1", "s1", "ประมาณ 35 กรัมต่อลิตร")}, []string{pipeline.BeatContradictory}},
		{"beat references unknown claim", srcs, []pipeline.ShortsBeat{beatFact, {Narration: "x", ClaimIDs: []string{"c7"}}},
			[]pipeline.ShortsClaim{claim("c1", "s1", "ประมาณ 35 กรัมต่อลิตร")}, []string{pipeline.BeatUnknownClaim}},
		{"claim without a beat", srcs, []pipeline.ShortsBeat{beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s1", "ประมาณ 35 กรัมต่อลิตร"), claim("c2", "s2", "ดาวศุกร์ร้อนกว่าดาวพุธ")}, []string{pipeline.ClaimUnreferenced}},
		{"duplicate claim id", srcs, []pipeline.ShortsBeat{beatFact},
			[]pipeline.ShortsClaim{claim("c1", "s1", "ประมาณ 35 กรัมต่อลิตร"), claim("c1", "s2", "ดาวศุกร์ร้อนกว่าดาวพุธ")}, []string{pipeline.ClaimDuplicateID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pipeline.CheckShortsClaims(&pipeline.ShortsScript{Beats: tc.beats, Claims: tc.claims}, tc.sources)
			var codes []string
			for _, f := range got.Failures {
				codes = append(codes, f.Code)
				if f.Message == "" || strings.Contains(f.Message, "กรัม") {
					t.Errorf("message %q must be set and must not echo quote text", f.Message)
				}
			}
			if strings.Join(codes, ",") != strings.Join(tc.want, ",") || got.OK != (len(tc.want) == 0) {
				t.Fatalf("codes %v ok=%v, want %v", codes, got.OK, tc.want)
			}
			if got.Failures == nil {
				t.Fatal("Failures must be an empty list, not null, so the stored JSON is stable")
			}
		})
	}
}

func TestValidateShortsScriptRejectsStructure(t *testing.T) {
	good := shortsJSON(t, 45, nil)
	if _, err := pipeline.ValidateShortsScript([]byte(good), 45); err != nil {
		t.Fatalf("good script rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"not json":         "nope",
		"extra property":   strings.Replace(good, `"language"`, `"claim_check": {}, "language"`, 1),
		"wrong language":   strings.Replace(good, `"language":"th"`, `"language":"en"`, 1),
		"bad source id":    strings.Replace(good, `"source_id":"s1"`, `"source_id":"../etc"`, 1),
		"too short for 45": shortsJSON(t, 10, nil),
		"too long for 45":  shortsJSON(t, 120, nil),
	} {
		if _, err := pipeline.ValidateShortsScript([]byte(raw), 45); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
