package pipeline

import (
	"strings"
	"testing"
)

func goodShortsQAInput() *shortsQAInput {
	return &shortsQAInput{
		script: &ShortsScript{
			Beats:  []ShortsBeat{{Narration: "x", NoFactualClaim: true}, {Narration: "y", ClaimIDs: []string{"c1"}}},
			Claims: []ShortsClaim{{ID: "c1", Text: "t", SourceID: "s1", Quote: "ประมาณ 35 กรัม"}},
		},
		sources: []SourceText{{ID: "s1", Text: "ความเค็มประมาณ 35 กรัมต่อลิตร"}},
	}
}

// Each Knowledge Shorts rule must be able to fail on its own, with a reason and a suggested fix, and
// the good input must pass every rule.
func TestShortsRulesEachFailAlone(t *testing.T) {
	spec := knowledgeShortsSpec
	card := spec.Visual.SourcesHeading + "\nSea water"
	tests := []struct {
		name   string
		mut    func(in *shortsQAInput)
		dur    float64
		subs   string
		target int
		rule   string // the one rule expected to fail ("" = all pass)
		sev    Severity
	}{
		{"all good", nil, 45, card, 45, "", ""},
		{"too short for the range", nil, 25, card, 25, shortsRuleDuration, Blocking},
		{"too long for the range", nil, 65, card, 65, shortsRuleDuration, Blocking},
		{"outside +-10% of target", nil, 52, card, 45, shortsRuleDuration, Blocking},
		{"source changed", func(in *shortsQAInput) {
			in.problems = []string{"source s1 (artifact 3) no longer matches what the script was made from"}
		}, 45, card, 45, shortsRuleSources, Blocking},
		{"quote not in source", func(in *shortsQAInput) {
			in.script.Claims[0].Quote = "ข้อความที่ไม่มีในแหล่ง"
		}, 45, card, 45, shortsRuleClaims, Blocking},
		{"claim cites unknown source", func(in *shortsQAInput) { in.script.Claims[0].SourceID = "s9" }, 45, card, 45, shortsRuleClaims, Blocking},
		{"no claims at all", func(in *shortsQAInput) {
			in.script.Claims = nil
			in.script.Beats = []ShortsBeat{{Narration: "x", NoFactualClaim: true}}
		}, 45, card, 45, shortsRuleClaims, Blocking},
		{"beat without claim and not framing", func(in *shortsQAInput) {
			in.script.Beats = append(in.script.Beats, ShortsBeat{Narration: "z"})
		}, 45, card, 45, shortsRuleBeats, Blocking},
		{"beat with claims marked framing", func(in *shortsQAInput) { in.script.Beats[1].NoFactualClaim = true }, 45, card, 45, shortsRuleBeats, Blocking},
		{"no Sources card", nil, 45, "00:00 hello", 45, shortsRuleCard, Blocking},
		{"card heading split by zero-width spaces still counts", nil, 45, strings.Join(strings.Split(spec.Visual.SourcesHeading, ""), "​"), 45, "", ""},
		{"newer source version", func(in *shortsQAInput) { in.newer = []string{"s1"} }, 45, card, 45, shortsRuleCurrent, Warning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := goodShortsQAInput()
			if tc.mut != nil {
				tc.mut(in)
			}
			res := shortsRules(in, Episode{TargetDurationS: tc.target}, tc.dur, tc.subs, spec)
			if len(res) != 6 {
				t.Fatalf("%d rules, want 6", len(res))
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
			switch {
			case tc.rule == "" && len(failed) != 0, tc.rule != "" && (len(failed) != 1 || failed[0] != tc.rule):
				t.Fatalf("failed rules %v, want only %q", failed, tc.rule)
			}
		})
	}
}

// The reason of a claim failure must name ids, not echo the quote.
func TestShortsRulesReasonsDoNotEchoQuotes(t *testing.T) {
	in := goodShortsQAInput()
	in.script.Claims[0].Quote = "ข้อความลับที่ไม่มีในแหล่ง"
	for _, r := range shortsRules(in, Episode{TargetDurationS: 45}, 45, knowledgeShortsSpec.Visual.SourcesHeading, knowledgeShortsSpec) {
		if strings.Contains(r.Reason, "ข้อความลับ") {
			t.Fatalf("rule %s echoes the quote: %s", r.Rule, r.Reason)
		}
	}
}

// The narration budget must come from the real timeline constants: a short with 8 beats loses 12.8 s
// to the lead, tail, two cards and nine gaps.
func TestShortsNarrationBudgetSubtractsCardsAndGaps(t *testing.T) {
	if got := shortsOverheadMS(8); got != 500+1000+2*2500+9*700 {
		t.Fatalf("overhead for 8 beats = %d ms", got)
	}
	if got := shortsSpeechSeconds(45, 8); got < 32.1 || got > 32.3 {
		t.Fatalf("speech seconds for 45 s / 8 beats = %.2f, want 32.2", got)
	}
	if shortsSpeechSeconds(45, 3) <= shortsSpeechSeconds(45, 8) {
		t.Fatal("fewer beats must leave more narration time")
	}
}
