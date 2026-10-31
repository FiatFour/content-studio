package pipeline

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed history_script.schema.json
var historySchemaJSON []byte

var historySchema = mustCompile(historySchemaJSON)

// History & Mystery runs 60 to 120 s (D20).
const (
	historyMinSeconds = 60
	historyMaxSeconds = 120
)

// HistoryScript is the History & Mystery script: narration beats and the claims they rest on. Every
// claim has a certainty (documented, disputed or speculation; D19) and one or more owner-pasted
// sources, each quoted. ClaimCheck is filled in by code after the model answered (the schema does
// not allow it), so it can never come from the model.
type HistoryScript struct {
	Title      string         `json:"title"`
	Language   string         `json:"language"`
	Beats      []ShortsBeat   `json:"beats"`
	Claims     []HistoryClaim `json:"claims"`
	ClaimCheck *ClaimCheck    `json:"claim_check,omitempty"`
}

type HistoryClaim struct {
	ID        string          `json:"id"`
	Text      string          `json:"text"`
	Certainty string          `json:"certainty"`
	DateText  string          `json:"date_text"` // empty when the claim has no date
	Sources   []HistorySource `json:"sources"`
}

type HistorySource struct {
	SourceID string `json:"source_id"`
	Quote    string `json:"quote"`
}

// ValidateHistoryScript parses untrusted model output, checks it against the schema, and checks the
// Thai narration length against the episode target (60 to 120 s), using the same speech-time budget
// as Knowledge Shorts. The claim check is separate (CheckHistoryClaims) because it needs the sources.
func ValidateHistoryScript(raw []byte, targetSeconds int) (*HistoryScript, error) {
	if targetSeconds < historyMinSeconds || targetSeconds > historyMaxSeconds {
		return nil, fmt.Errorf("target %d s is outside the %d to %d s range of a history script", targetSeconds, historyMinSeconds, historyMaxSeconds)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("output is not valid JSON")
	}
	if err := historySchema.Validate(doc); err != nil {
		return nil, fmt.Errorf("output does not match the history script schema: %s", schemaDetail(err))
	}
	var s HistoryScript
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("output does not match the history script schema")
	}
	chars := 0
	for i, b := range s.Beats {
		if !hasThai(b.Narration) {
			return nil, fmt.Errorf("beat %d narration is not in Thai", i+1)
		}
		chars += utf8.RuneCountInString(b.Narration)
		// The visual becomes part of an image prompt, so it gets the same checks the images stage
		// applies later; failing here lets the stage retry with feedback instead of failing at images.
		v, err := CleanHistoryVisual(b.Visual)
		if err != nil {
			return nil, fmt.Errorf("beat %d: %w", i+1, err)
		}
		if repeatsSourceText(&s, b, v) {
			return nil, fmt.Errorf("beat %d: visual description repeats narration, a claim or a quote; describe a scene instead", i+1)
		}
	}
	speech := shortsSpeechSeconds(targetSeconds, len(s.Beats))
	minChars, maxChars := int(speech*minCharsPerSecond), int(speech*maxCharsPerSecond)
	if chars < minChars {
		return nil, fmt.Errorf("narration has only %d characters but a %d s video with %d beats needs at least %d. Add about %d more characters", chars, targetSeconds, len(s.Beats), minChars, minChars-chars)
	}
	if chars > maxChars {
		return nil, fmt.Errorf("narration has %d characters but a %d s video with %d beats allows at most %d. Remove about %d characters", chars, targetSeconds, len(s.Beats), maxChars, chars-maxChars)
	}
	return &s, nil
}

// CheckHistoryClaims runs the deterministic claim rules: the quote-in-source rule of Knowledge Shorts
// (checkQuote) for every entry of every claim's sources, plus the same claim and beat linking rules.
// It proves the quotes are in the sources, not that the claims are true.
func CheckHistoryClaims(s *HistoryScript, sources []SourceText) ClaimCheck {
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
		for _, src := range c.Sources {
			if code, msg := checkQuote(text, sources, c.ID, src.SourceID, src.Quote); code != "" {
				fail(code, "%s", msg)
			}
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
