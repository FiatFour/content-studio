package pipeline

import (
	"fmt"
	"regexp"
	"strings"
)

// History finding codes. The claim-check failure codes (quote_not_found and the others) are passed
// through unchanged.
const (
	HistoryUnlabelledUncertainty = "uncertain_claim_unlabelled"
	HistoryNumberNotInSource     = "number_not_in_source"
)

// HistoryFinding is one advisory result of the History checks. It is data for the QA stage (T6) to
// show; nothing here changes the script. Messages name ids and short numbers, never quote text.
type HistoryFinding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	ClaimID  string   `json:"claim_id,omitempty"`
	Beat     int      `json:"beat,omitempty"` // 1-based, 0 when the finding is not about one beat
	Message  string   `json:"message"`
}

// historyHedgeMarkers are the phrases that count as labelling a speculation or disputed claim in the
// narration (D19). This is a HEURISTIC: it checks that some hedging phrase is present in the beat,
// not that the phrase is attached to the right sentence or that the wording is honest. Matching is a
// plain substring test on the narration (Thai has no word spaces; English is lower-cased). The owner
// still reviews the narration. Extend the list here; keep it visible and explicit.
var historyHedgeMarkers = []string{
	// Thai
	"คาดว่า", "เชื่อกันว่า", "เชื่อว่า", "มีทฤษฎีว่า", "ทฤษฎีหนึ่ง", "ยังไม่มีข้อสรุป", "ยังไม่ทราบแน่ชัด",
	"ไม่มีใครรู้", "ถกเถียง", "โต้แย้ง", "ตามคำกล่าวอ้าง", "ตามที่อ้าง", "อาจจะ", "อาจเป็น", "อาจมี", "สันนิษฐาน",
	// English
	"allegedly", "reportedly", "supposedly", "is believed", "are believed", "it is said", "some say",
	"theory", "theories", "speculat", "disputed", "debated", "unclear", "unknown", "may have", "might have", "possibly",
}

// historyNumberRe matches a number: digits (Arabic or Thai), optionally joined by single "," or "."
// separators that sit between digits ("1,200", "3.5"). A trailing separator is not part of the number.
var historyNumberRe = regexp.MustCompile(`[0-9๐-๙]+(?:[.,][0-9๐-๙]+)*`)

// normDigits maps Thai digits to Arabic digits.
func normDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '๐' && r <= '๙' {
			return r - '๐' + '0'
		}
		return r
	}, s)
}

// historyNumbers returns the numbers in s as normalised tokens: Thai digits become Arabic and commas
// are dropped ("1,200" and "๑,๒๐๐" both give "1200"). Years are plain numbers: a Buddhist-era year
// (2567) is never converted to a Gregorian one (2024); only the literal digits are compared.
func historyNumbers(s string) []string {
	var out []string
	for _, m := range historyNumberRe.FindAllString(normDigits(s), -1) {
		out = append(out, strings.ReplaceAll(m, ",", ""))
	}
	return out
}

// CheckHistoryScript runs the History checks and returns the findings, empty when none. It is pure:
// no store, no model. Findings are advisory data; they never edit the script.
//
//  1. Claim quotes: CheckHistoryClaims (quote in its cited source, claim and beat links), blocking.
//  2. Labelling: a beat that cites a speculation or disputed claim needs a hedge marker
//     (historyHedgeMarkers) in its own narration, blocking. A documented claim needs none.
//  3. Numbers: every number in the narration must occur literally in at least one source text,
//     warning. This is literal matching only: number WORDS ("สามพัน", "three") are not checked,
//     nothing is converted (no Buddhist to Gregorian era, no unit conversion), and a number found in
//     any source passes even if it is not the source the beat's claim cites.
func CheckHistoryScript(s *HistoryScript, sources []SourceText) []HistoryFinding {
	findings := []HistoryFinding{}
	for _, f := range CheckHistoryClaims(s, sources).Failures {
		findings = append(findings, HistoryFinding{Code: f.Code, Severity: Blocking, Message: clipFinding(f.Message)})
	}

	uncertain := map[string]string{} // claim id -> certainty
	for _, c := range s.Claims {
		if c.Certainty == "speculation" || c.Certainty == "disputed" {
			uncertain[c.ID] = c.Certainty
		}
	}
	for i, b := range s.Beats {
		if hasHedge(b.Narration) {
			continue
		}
		for _, id := range b.ClaimIDs {
			if cert, ok := uncertain[id]; ok {
				findings = append(findings, HistoryFinding{
					Code: HistoryUnlabelledUncertainty, Severity: Blocking, ClaimID: id, Beat: i + 1,
					Message: clipFinding(fmt.Sprintf("beat %d cites %s claim %s but its narration has no hedging phrase", i+1, cert, id)),
				})
			}
		}
	}

	inSource := map[string]bool{}
	for _, src := range sources {
		for _, n := range historyNumbers(src.Text) {
			inSource[n] = true
		}
	}
	for i, b := range s.Beats {
		seen := map[string]bool{}
		for _, n := range historyNumbers(b.Narration) {
			if inSource[n] || seen[n] {
				continue
			}
			seen[n] = true
			findings = append(findings, HistoryFinding{
				Code: HistoryNumberNotInSource, Severity: Warning, Beat: i + 1,
				Message: clipFinding(fmt.Sprintf("beat %d narration has the number %s, which appears in no source", i+1, clipNumber(n))),
			})
		}
	}
	return findings
}

func hasHedge(narration string) bool {
	n := strings.ToLower(narration)
	for _, m := range historyHedgeMarkers {
		if strings.Contains(n, m) {
			return true
		}
	}
	return false
}

func clipNumber(n string) string {
	if r := []rune(n); len(r) > 20 {
		return string(r[:20]) + "..."
	}
	return n
}

func clipFinding(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "..."
	}
	return s
}
