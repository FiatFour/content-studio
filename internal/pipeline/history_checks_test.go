package pipeline

import (
	"reflect"
	"strings"
	"testing"
)

func hs(beats []ShortsBeat, claims ...HistoryClaim) *HistoryScript {
	return &HistoryScript{Beats: beats, Claims: claims}
}

func hc(id, cert, srcID, quote string) HistoryClaim {
	return HistoryClaim{ID: id, Certainty: cert, Sources: []HistorySource{{SourceID: srcID, Quote: quote}}}
}

func codes(fs []HistoryFinding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return out
}

func TestHistoryQuoteChecksReused(t *testing.T) {
	src := []SourceText{{ID: "s1", Text: "วัดสร้างในปี 1782 ตามบันทึก"}}
	good := hs([]ShortsBeat{{Narration: "วัดสร้างในปี 1782", ClaimIDs: []string{"c1"}}}, hc("c1", "documented", "s1", "วัดสร้างในปี 1782"))
	if got := CheckHistoryScript(good, src); len(got) != 0 {
		t.Fatalf("good script has findings: %+v", got)
	}
	bad := hs([]ShortsBeat{{Narration: "วัดสร้างในปี 1782", ClaimIDs: []string{"c1"}}}, hc("c1", "documented", "s1", "ข้อความที่ไม่มี"))
	got := CheckHistoryScript(bad, src)
	if len(got) != 1 || got[0].Code != ClaimQuoteNotFound || got[0].Severity != Blocking {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(got[0].Message, "ข้อความที่ไม่มี") {
		t.Fatal("message echoes the quote")
	}
}

func TestHistoryUncertaintyLabelling(t *testing.T) {
	src := []SourceText{{ID: "s1", Text: "x"}}
	cases := []struct {
		name      string
		cert      string
		narration string
		want      []string
	}{
		{"documented needs no marker", "documented", "เมืองนี้ก่อตั้งขึ้นโดยกษัตริย์", []string{}},
		{"speculation unlabelled", "speculation", "เมืองนี้ถูกทิ้งร้างเพราะภัยพิบัติ", []string{HistoryUnlabelledUncertainty}},
		{"disputed unlabelled", "disputed", "ดาบเล่มนี้เป็นของกษัตริย์", []string{HistoryUnlabelledUncertainty}},
		{"speculation Thai hedge", "speculation", "เชื่อกันว่าเมืองถูกทิ้งร้างเพราะภัยพิบัติ", []string{}},
		{"disputed Thai hedge", "disputed", "นักวิชาการยังถกเถียงเรื่องอายุของดาบ", []string{}},
		{"English hedge, case-insensitive", "speculation", "Some say เมืองล่มสลาย", []string{}},
		{"flat assertion of disputed claim", "disputed", "ดาบเป็นของกษัตริย์แน่นอน", []string{HistoryUnlabelledUncertainty}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := hs([]ShortsBeat{{Narration: tc.narration, ClaimIDs: []string{"c1"}}}, hc("c1", tc.cert, "s1", "x"))
			if got := codes(CheckHistoryScript(s, src)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestHistoryHedgeIsPerBeat(t *testing.T) {
	src := []SourceText{{ID: "s1", Text: "x"}}
	s := hs([]ShortsBeat{
		{Narration: "เชื่อกันว่ามีสมบัติ", ClaimIDs: []string{"c1"}},
		{Narration: "สมบัติถูกซ่อนไว้ใต้วัด", ClaimIDs: []string{"c1"}},
	}, hc("c1", "speculation", "s1", "x"))
	got := CheckHistoryScript(s, src)
	if len(got) != 1 || got[0].Beat != 2 || got[0].ClaimID != "c1" || got[0].Severity != Blocking {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryNumbers(t *testing.T) {
	cases := []struct {
		name   string
		source string
		narr   string
		want   []string // numbers flagged
	}{
		{"number in source passes", "สร้างเมื่อ 1782", "สร้างเมื่อปี 1782", nil},
		{"number in no source", "สร้างเมื่อ 1782", "สร้างเมื่อปี 1783", []string{"1783"}},
		{"Thai digits normalised, found", "built in 1782", "สร้างเมื่อ ๑๗๘๒", nil},
		{"Thai digits normalised, flagged as Arabic", "built in 1782", "สร้างเมื่อ ๑๗๘๓", []string{"1783"}},
		{"Thai digits in source match Arabic narration", "สร้างเมื่อ ๒๕๖๗", "สร้างเมื่อ 2567", nil},
		{"Buddhist era is not converted", "built in 2024", "สร้างเมื่อ 2567", []string{"2567"}},
		{"thousands separator", "ประชากร 1,200 คน", "ประชากร 1200 คน", nil},
		{"decimal", "ยาว 3.5 เมตร", "ยาว 3.5 เมตร", nil},
		{"sentence-final dot not part of number", "ปี 1782", "ในปี 1782.", nil},
		{"number words not checked", "ปี 1782", "สามพันคนและ three hundred", nil},
		{"duplicate reported once", "x", "7 และ 7", []string{"7"}},
		{"multiple, only missing flagged", "ปี 1782 มี 40 คน", "ปี 1782 มี 41 คน", []string{"41"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := hs([]ShortsBeat{{Narration: tc.narr, NoFactualClaim: true}})
			got := CheckHistoryScript(s, []SourceText{{ID: "s1", Text: tc.source}})
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v want %v", got, tc.want)
			}
			for i, n := range tc.want {
				f := got[i]
				if f.Code != HistoryNumberNotInSource || f.Severity != Warning || f.Beat != 1 || !strings.Contains(f.Message, "number "+n+",") {
					t.Fatalf("finding %+v does not flag %s", f, n)
				}
			}
		})
	}
}

func TestHistoryNumberFromAnySource(t *testing.T) {
	s := hs([]ShortsBeat{{Narration: "ปี 1900", NoFactualClaim: true}})
	got := CheckHistoryScript(s, []SourceText{{ID: "a", Text: "ปี 1782"}, {ID: "b", Text: "ปี 1900"}})
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
