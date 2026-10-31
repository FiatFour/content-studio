package pipeline

import (
	"strings"
	"testing"
)

const historyTarget = 90

// padTo returns lead followed by filler so the text has n runes.
func padTo(lead string, n int) string {
	if r := []rune(lead); len(r) < n {
		return lead + strings.Repeat("ก", n-len(r))
	}
	return lead
}

// goodHistoryQAInput is a 2-beat script whose narration sits in the middle of the speech budget for a
// 90 s target: one documented claim, one speculation claim worded with a hedge, numbers all in sources.
func goodHistoryQAInput() *historyQAInput {
	speech := shortsSpeechSeconds(historyTarget, 2)
	each := int(speech*(minCharsPerSecond+maxCharsPerSecond)/2) / 2
	return &historyQAInput{
		script: &HistoryScript{
			Beats: []ShortsBeat{
				{Narration: padTo("วัดสร้างในปี 1782 ", each), ClaimIDs: []string{"c1"}},
				{Narration: padTo("เชื่อกันว่าเมืองถูกทิ้งร้างเพราะภัยพิบัติ ", each), ClaimIDs: []string{"c2"}},
			},
			Claims: []HistoryClaim{
				{ID: "c1", Certainty: "documented", Sources: []HistorySource{{SourceID: "s1", Quote: "วัดสร้างในปี 1782"}}},
				{ID: "c2", Certainty: "speculation", Sources: []HistorySource{{SourceID: "s1", Quote: "อาจถูกทิ้งร้าง"}}},
			},
		},
		sources: []SourceText{{ID: "s1", Text: "วัดสร้างในปี 1782 ตามบันทึก และเมืองอาจถูกทิ้งร้าง"}},
		stills: []map[string]any{
			{"image_kind": "illustration", "evidence": false},
			{"image_kind": "illustration", "evidence": false},
		},
	}
}

func TestHistoryRulesEachFailAlone(t *testing.T) {
	spec := historyMysterySpec
	card := historySourcesHead + "\nTemple"
	speech := shortsSpeechSeconds(historyTarget, 2)
	maxC := int(speech * maxCharsPerSecond)
	tests := []struct {
		name   string
		mut    func(in *historyQAInput)
		dur    float64
		subs   string
		target int
		rule   string // the one rule expected to fail ("" = all pass)
		sev    Severity
	}{
		{"all good", nil, 90, card, historyTarget, "", ""},
		{"too short for the range", nil, 50, card, historyTarget, historyRuleDuration, Blocking},
		{"too long for the range", nil, 130, card, historyTarget, historyRuleDuration, Blocking},
		{"outside +-10% of target", nil, 100, card, historyTarget, historyRuleDuration, Blocking},
		{"narration over budget", func(in *historyQAInput) {
			in.script.Beats[0].Narration = padTo("วัดสร้างในปี 1782 ", maxC)
		}, 90, card, historyTarget, historyRuleNarration, Blocking},
		{"narration under budget", func(in *historyQAInput) {
			in.script.Beats[0].Narration = "วัดสร้างในปี 1782"
			in.script.Beats[1].Narration = "เชื่อกันว่าเมืองถูกทิ้งร้าง"
		}, 90, card, historyTarget, historyRuleNarration, Blocking},
		{"source changed", func(in *historyQAInput) {
			in.problems = []string{"source s1 (artifact 3) no longer matches what the script was made from"}
		}, 90, card, historyTarget, historyRuleSources, Blocking},
		{"quote not in source", func(in *historyQAInput) {
			in.script.Claims[0].Sources[0].Quote = "ข้อความที่ไม่มีในแหล่ง"
		}, 90, card, historyTarget, historyRuleClaims, Blocking},
		{"claim cites unknown source", func(in *historyQAInput) { in.script.Claims[0].Sources[0].SourceID = "s9" }, 90, card, historyTarget, historyRuleClaims, Blocking},
		{"claim with no source", func(in *historyQAInput) { in.script.Claims[0].Sources = nil }, 90, card, historyTarget, historyRuleClaims, Blocking},
		{"beat without claim and not framing", func(in *historyQAInput) {
			in.script.Beats[0].ClaimIDs = nil
			in.script.Beats[1].ClaimIDs = []string{"c2", "c1"}
		}, 90, card, historyTarget, historyRuleBeats, Blocking},
		{"unlabelled speculation", func(in *historyQAInput) {
			in.script.Beats[1].Narration = padTo("เมืองถูกทิ้งร้างเพราะภัยพิบัติ ", len([]rune(in.script.Beats[1].Narration)))
		}, 90, card, historyTarget, historyRuleUncertainty, Blocking},
		{"unlabelled disputed claim", func(in *historyQAInput) {
			in.script.Claims[1].Certainty = "disputed"
			in.script.Beats[1].Narration = padTo("ดาบเป็นของกษัตริย์แน่นอน ", len([]rune(in.script.Beats[1].Narration)))
		}, 90, card, historyTarget, historyRuleUncertainty, Blocking},
		{"number in no source", func(in *historyQAInput) {
			in.script.Beats[0].Narration = padTo("วัดสร้างในปี 1782 มีคน 4500 คน ", len([]rune(in.script.Beats[0].Narration)))
		}, 90, card, historyTarget, historyRuleNumbers, Warning},
		{"no Sources card", nil, 90, "00:00 hello", historyTarget, historyRuleCard, Blocking},
		{"newer source version", func(in *historyQAInput) { in.newer = []string{"s1"} }, 90, card, historyTarget, historyRuleCurrent, Warning},
		{"still not labelled", func(in *historyQAInput) { in.stills[1] = map[string]any{} }, 90, card, historyTarget, historyRuleIllustration, Blocking},
		{"still marked as evidence", func(in *historyQAInput) { in.stills[0]["evidence"] = true }, 90, card, historyTarget, historyRuleIllustration, Blocking},
		{"no stills to check passes", func(in *historyQAInput) { in.stills = nil }, 90, card, historyTarget, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := goodHistoryQAInput()
			if tc.mut != nil {
				tc.mut(in)
			}
			res := historyRules(in, Episode{TargetDurationS: tc.target}, tc.dur, tc.subs, spec)
			if len(res) != 10 {
				t.Fatalf("%d rules, want 10", len(res))
			}
			var failed []string
			for _, r := range res {
				if r.RuleVersion != 1 || r.Reason == "" {
					t.Errorf("rule %s has version %d, reason %q", r.Rule, r.RuleVersion, r.Reason)
				}
				if !r.Passed {
					failed = append(failed, r.Rule)
					if r.SuggestedFix == "" || r.Severity != tc.sev {
						t.Errorf("failed rule %s: fix %q severity %s, want %s", r.Rule, r.SuggestedFix, r.Severity, tc.sev)
					}
				}
			}
			if tc.rule == "" && len(failed) != 0 || tc.rule != "" && (len(failed) != 1 || failed[0] != tc.rule) {
				t.Fatalf("failed rules %v, want only %q", failed, tc.rule)
			}
		})
	}
}

// QA is where the T4 hedge and number checks become real: the script stage does not call them. An
// unlabelled speculation claim and a source-less number must both be reported by the QA rules.
func TestHistoryQAReportsUnlabelledSpeculationAndSourcelessNumber(t *testing.T) {
	in := goodHistoryQAInput()
	n := len([]rune(in.script.Beats[1].Narration))
	in.script.Beats[1].Narration = padTo("เมืองถูกทิ้งร้างเพราะภัยพิบัติ มีคน 99999 คน ", n)
	got := map[string]QAResult{}
	for _, r := range historyRules(in, Episode{TargetDurationS: historyTarget}, 90, historySourcesHead, historyMysterySpec) {
		got[r.Rule] = r
	}
	if r := got[historyRuleUncertainty]; r.Passed || r.Severity != Blocking || !strings.Contains(r.Reason, "c2") {
		t.Errorf("uncertainty rule: %+v", r)
	}
	if r := got[historyRuleNumbers]; r.Passed || r.Severity != Warning || !strings.Contains(r.Reason, "99999") {
		t.Errorf("number rule: %+v", r)
	}
}

// Reasons name ids and numbers; they never echo a quote or narration text.
func TestHistoryRulesReasonsDoNotEchoQuotes(t *testing.T) {
	in := goodHistoryQAInput()
	in.script.Claims[0].Sources[0].Quote = "ข้อความลับที่ไม่มีในแหล่ง"
	for _, r := range historyRules(in, Episode{TargetDurationS: historyTarget}, 90, historySourcesHead, historyMysterySpec) {
		if strings.Contains(r.Reason, "ข้อความลับ") || strings.Contains(r.Reason, "เมืองถูกทิ้งร้าง") {
			t.Fatalf("rule %s echoes text: %s", r.Rule, r.Reason)
		}
	}
}

func TestHistoryQARuleSetName(t *testing.T) {
	if historyMysterySpec.QARules != "qa-rules-history-v1" {
		t.Fatalf("rule set %q", historyMysterySpec.QARules)
	}
}
