package pipeline

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed shorts_script.schema.json
var shortsSchemaJSON []byte

var shortsSchema = mustCompile(shortsSchemaJSON)

// ShortsScript is the Knowledge Shorts script: narration beats and the claims they rest on. Every
// claim cites one owner-pasted source and quotes it. ClaimCheck is filled in by code after the model
// answered (the schema does not allow it), so it can never come from the model.
type ShortsScript struct {
	Title      string        `json:"title"`
	Language   string        `json:"language"`
	Beats      []ShortsBeat  `json:"beats"`
	Claims     []ShortsClaim `json:"claims"`
	ClaimCheck *ClaimCheck   `json:"claim_check,omitempty"`
}

type ShortsBeat struct {
	Narration string   `json:"narration"`
	ClaimIDs  []string `json:"claim_ids"`
	// NoFactualClaim marks framing: a hook, a transition or a call to action that states no fact.
	NoFactualClaim bool `json:"no_factual_claim"`
}

type ShortsClaim struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	SourceID string `json:"source_id"`
	Quote    string `json:"quote"`
}

// SourceText is the latest version of one source of the episode.
type SourceText struct {
	ID   string
	Text string
}

// Claim check failure codes.
const (
	ClaimDuplicateID   = "claim_duplicate_id"
	ClaimSourceUnknown = "source_unknown"
	ClaimQuoteNotFound = "quote_not_found"
	ClaimQuoteWrongSrc = "quote_wrong_source"
	ClaimUnreferenced  = "claim_unreferenced"
	BeatNoSupport      = "beat_unsupported"
	BeatUnknownClaim   = "beat_unknown_claim"
	BeatContradictory  = "beat_contradictory"
)

type ClaimFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ClaimCheck is the stored result of the deterministic claim check.
//
// It proves only that each quote is present, word for word (whitespace aside), in the cited source.
// It does not prove the claim is true, nor that the claim summarises the quote fairly. The owner reviews that.
type ClaimCheck struct {
	OK       bool           `json:"ok"`
	Claims   int            `json:"claims"`
	Beats    int            `json:"beats"`
	Failures []ClaimFailure `json:"failures"`
}

// normSpace collapses every run of Unicode whitespace to one space and trims the ends. It is the only
// normalisation the quote check applies: no case folding, no punctuation or Unicode form changes.
func normSpace(s string) string { return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ") }

// CheckShortsClaims runs the deterministic claim rules. It is pure: it reads no store and calls no model.
// Messages name claim and source ids, never quote text, so they are safe to show and to feed back to the model.
func CheckShortsClaims(s *ShortsScript, sources []SourceText) ClaimCheck {
	res := ClaimCheck{Claims: len(s.Claims), Beats: len(s.Beats), Failures: []ClaimFailure{}}
	fail := func(code, format string, args ...any) {
		res.Failures = append(res.Failures, ClaimFailure{Code: code, Message: fmt.Sprintf(format, args...)})
	}
	text := make(map[string]string, len(sources))
	for _, src := range sources {
		text[src.ID] = normSpace(src.Text)
	}
	claimIDs := map[string]bool{}
	for _, c := range s.Claims {
		if claimIDs[c.ID] {
			fail(ClaimDuplicateID, "claim id %s is used twice", c.ID)
		}
		claimIDs[c.ID] = true
		src, ok := text[c.SourceID]
		if !ok {
			fail(ClaimSourceUnknown, "claim %s cites source %s, which does not exist for this episode", c.ID, c.SourceID)
			continue
		}
		q := normSpace(c.Quote)
		if q != "" && strings.Contains(src, q) {
			continue
		}
		other := ""
		for _, o := range sources {
			if o.ID != c.SourceID && q != "" && strings.Contains(text[o.ID], q) {
				other = o.ID
				break
			}
		}
		if other != "" {
			fail(ClaimQuoteWrongSrc, "claim %s quotes text that is in source %s, not in the cited source %s", c.ID, other, c.SourceID)
		} else {
			fail(ClaimQuoteNotFound, "the quote of claim %s does not appear word for word in source %s. Copy a passage exactly from that source", c.ID, c.SourceID)
		}
	}
	used := map[string]bool{}
	for i, b := range s.Beats {
		switch {
		case len(b.ClaimIDs) == 0 && !b.NoFactualClaim:
			fail(BeatNoSupport, "beat %d has no claim_ids and is not marked no_factual_claim", i+1)
		case len(b.ClaimIDs) > 0 && b.NoFactualClaim:
			fail(BeatContradictory, "beat %d lists claim_ids and is also marked no_factual_claim", i+1)
		}
		for _, id := range b.ClaimIDs {
			if !claimIDs[id] {
				fail(BeatUnknownClaim, "beat %d references claim %s, which does not exist", i+1, id)
			}
			used[id] = true
		}
	}
	for _, c := range s.Claims {
		if !used[c.ID] {
			fail(ClaimUnreferenced, "claim %s is not used by any beat", c.ID)
		}
	}
	res.OK = len(res.Failures) == 0
	return res
}

// feedback joins the failure messages (at most 8, so the prompt stays small) for the model.
func (c ClaimCheck) feedback() string {
	var parts []string
	for i, f := range c.Failures {
		if i == 8 {
			parts = append(parts, fmt.Sprintf("and %d more", len(c.Failures)-i))
			break
		}
		parts = append(parts, f.Message)
	}
	return strings.Join(parts, "; ")
}

// shortsOverheadMS is the part of a short that is not narration, from the constants the timeline
// uses: the lead and tail, the title card and the Sources card (emptyShotMS each, they carry no
// line) and one shotGapMS between each pair of the beats+2 shots.
func shortsOverheadMS(beats int) int64 {
	return int64(leadMS+tailMS+2*emptyShotMS) + int64(beats+1)*shotGapMS
}

// shortsSpeechSeconds is the time left for narration once the cards and gaps are taken from the target.
func shortsSpeechSeconds(targetSeconds, beats int) float64 {
	return math.Max(float64(int64(targetSeconds)*1000-shortsOverheadMS(beats))/1000, 1)
}

// ValidateShortsScript parses untrusted model output, checks it against the schema, and checks the
// Thai narration length against the episode target with the same characters-per-second bounds the
// Fictional Series script uses. The claim check is separate (CheckShortsClaims) because it needs the sources.
func ValidateShortsScript(raw []byte, targetSeconds int) (*ShortsScript, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("output is not valid JSON")
	}
	if err := shortsSchema.Validate(doc); err != nil {
		return nil, fmt.Errorf("output does not match the shorts script schema: %s", schemaDetail(err))
	}
	var s ShortsScript
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("output does not match the shorts script schema")
	}
	chars := 0
	for i, b := range s.Beats {
		if !hasThai(b.Narration) {
			return nil, fmt.Errorf("beat %d narration is not in Thai", i+1)
		}
		chars += utf8.RuneCountInString(b.Narration)
	}
	speech := shortsSpeechSeconds(targetSeconds, len(s.Beats))
	minChars, maxChars := int(speech*minCharsPerSecond), int(speech*maxCharsPerSecond)
	if chars < minChars {
		return nil, fmt.Errorf("narration has only %d characters but a %d s short with %d beats needs at least %d (the title card, the Sources card and the pauses between beats take the rest of the time). Add about %d more characters", chars, targetSeconds, len(s.Beats), minChars, minChars-chars)
	}
	if chars > maxChars {
		return nil, fmt.Errorf("narration has %d characters but a %d s short with %d beats allows at most %d (the title card, the Sources card and the pauses between beats take the rest of the time). Remove about %d characters", chars, targetSeconds, len(s.Beats), maxChars, chars-maxChars)
	}
	return &s, nil
}
