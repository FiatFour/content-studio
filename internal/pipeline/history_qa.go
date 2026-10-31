package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
)

// History & Mystery QA rules (rule set qa-rules-history-v1). They run inside the shared QA stage,
// after the shared checks, when the run's script says it is a History script. Every rule is
// deterministic: no model is asked. The claim rules prove that a quote is in its source and that
// uncertain claims are hedged in the narration (a phrase-list heuristic); they do not prove a claim
// is true. The owner reviews that.
//
// Severities (decisions D19, D20):
//   - quote not in source, broken claim/beat links: blocking (the claim has no footing).
//   - unlabelled speculation or disputed claim: blocking (the video would state a guess as fact).
//   - a number in the narration that is in no source: WARNING, as in CheckHistoryScript. The check is
//     literal matching only (it cannot see number words or converted units), so it advises and the
//     owner decides.
//   - a still without image_kind=illustration / evidence=false: blocking. The video must never pass an
//     illustration off as evidence, and the label is set by code, so a missing one is a pipeline fault.
//     A run with no stills to look at passes this rule (nothing to mislabel).
const (
	historyRuleDuration     = "history_duration_range"
	historyRuleNarration    = "history_narration_budget"
	historyRuleSources      = "history_sources_intact"
	historyRuleClaims       = "history_claims_quoted"
	historyRuleBeats        = "history_beats_supported"
	historyRuleUncertainty  = "history_uncertainty_labelled"
	historyRuleNumbers      = "history_numbers_in_sources"
	historyRuleCard         = "history_sources_card"
	historyRuleCurrent      = "history_sources_current"
	historyRuleIllustration = "history_stills_labelled"
)

// historyQAInput is what the History rules read.
type historyQAInput struct {
	script  *HistoryScript
	sources []SourceText
	// problems lists sources that are missing, unreadable or changed since the script was made.
	problems []string
	// newer lists source ids that have a newer version than the one the script used.
	newer []string
	// stills holds the metadata of every still the render used (empty when none could be found).
	stills []map[string]any
}

// isHistoryScript reports whether a script artifact is a History & Mystery script.
func isHistoryScript(a artifacts.Artifact) bool { return a.Meta["profile"] == HistoryMysteryID }

// loadHistoryQA re-reads the history script and its source versions. Source loading is the same as
// for Knowledge Shorts (loadShortsQA reads the script's "sources" list and the source rows; it also
// decodes the body as a Shorts script, which is ignored here because the history body is decoded
// separately).
func (d *Deps) loadHistoryQA(ctx context.Context, scriptArt artifacts.Artifact) (*historyQAInput, error) {
	data, err := d.read(scriptArt)
	if err != nil {
		return nil, err
	}
	var hs HistoryScript
	if err := json.Unmarshal(data, &hs); err != nil {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "the stored history script is unreadable")
	}
	sq, err := d.loadShortsQA(ctx, scriptArt)
	if err != nil {
		return nil, err
	}
	return &historyQAInput{script: &hs, sources: sq.sources, problems: sq.problems, newer: sq.newer}, nil
}

// historyQA measures what the History rules need (length, subtitle text, still metadata) and runs them.
func (d *Deps) historyQA(ctx context.Context, e Episode, scriptArt, video, specArt artifacts.Artifact) ([]QAResult, error) {
	in, err := d.loadHistoryQA(ctx, scriptArt)
	if err != nil {
		return nil, err
	}
	var dur float64
	if path, err := d.Store.Abs(video.StoragePath); err == nil {
		if p, err := d.Tools.Probe(ctx, path); err == nil {
			dur = p.DurationSeconds()
		}
	}
	var subs string
	var spec RenderSpec
	if data, err := d.read(specArt); err == nil && json.Unmarshal(data, &spec) == nil {
		if abs, err := d.Store.Abs(spec.Subtitles.Path); err == nil {
			subs, _ = readSmall(abs)
		}
		for _, sh := range spec.Shots {
			var meta map[string]any
			err := d.DB.QueryRow(ctx, `SELECT meta FROM artifacts WHERE type = 'image' AND storage_path = $1 AND sha256 = $2 ORDER BY id DESC LIMIT 1`,
				sh.Image.Path, sh.Image.SHA256).Scan(&meta)
			if err != nil {
				meta = map[string]any{} // not found: counts as unlabelled
			}
			in.stills = append(in.stills, meta)
		}
	}
	return historyRules(in, e, dur, subs, historyMysterySpec), nil
}

// historyRules returns the History rules. durS is the measured video length (0 when it could not be
// measured), subs the subtitle file text ("" when unreadable).
func historyRules(in *historyQAInput, e Episode, durS float64, subs string, spec Spec) []QAResult {
	var res []QAResult
	add := func(rule string, sev Severity, ok bool, reason, fix string) {
		res = append(res, QAResult{Rule: rule, RuleVersion: 1, Severity: sev, Passed: ok, Reason: reason, SuggestedFix: fix})
	}
	target := float64(e.TargetDurationS)
	inRange := durS >= float64(spec.MinDurationS) && durS <= float64(spec.MaxDurationS) && math.Abs(durS-target) <= target*durationTolPct/100
	add(historyRuleDuration, Blocking, inRange, fmt.Sprintf("%.2f s (a history video must be %d to %d s and within %d%% of the %d s target)",
		durS, spec.MinDurationS, spec.MaxDurationS, durationTolPct, e.TargetDurationS), "set the target between 60 and 120 s and adjust the script length")

	// Narration budget: the bounds the script stage enforced, recomputed from the stored script and
	// the real timeline constants (lead, tail, two cards, gaps), so a script stored out of band
	// cannot slip past.
	chars := 0
	for _, b := range in.script.Beats {
		chars += utf8.RuneCountInString(b.Narration)
	}
	speech := shortsSpeechSeconds(e.TargetDurationS, len(in.script.Beats))
	minC, maxC := int(speech*minCharsPerSecond), int(speech*maxCharsPerSecond)
	add(historyRuleNarration, Blocking, chars >= minC && chars <= maxC,
		fmt.Sprintf("narration has %d characters; %.1f s of speech for a %d s target with %d beats allows %d to %d",
			chars, speech, e.TargetDurationS, len(in.script.Beats), minC, maxC),
		"write the script again with narration that fits the speech time")

	srcOK := len(in.problems) == 0 && len(in.sources) > 0
	add(historyRuleSources, Blocking, srcOK,
		okOr(srcOK, fmt.Sprintf("%d source versions found and unchanged", len(in.sources)), orText(strings.Join(in.problems, "; "), "the script names no source")),
		"restore the source or write the script again from the current sources")

	var claimMsgs, beatMsgs, hedgeMsgs, numMsgs []string
	for _, f := range CheckHistoryScript(in.script, in.sources) {
		switch f.Code {
		case BeatNoSupport, BeatContradictory, BeatUnknownClaim:
			beatMsgs = append(beatMsgs, f.Message)
		case HistoryUnlabelledUncertainty:
			hedgeMsgs = append(hedgeMsgs, f.Message)
		case HistoryNumberNotInSource:
			numMsgs = append(numMsgs, f.Message)
		default:
			claimMsgs = append(claimMsgs, f.Message)
		}
	}
	if len(in.script.Claims) == 0 {
		claimMsgs = append(claimMsgs, "the script has no claims")
	}
	quotes := 0
	for _, c := range in.script.Claims {
		if len(c.Sources) == 0 {
			claimMsgs = append(claimMsgs, fmt.Sprintf("claim %s cites no source", c.ID))
		}
		quotes += len(c.Sources)
	}
	add(historyRuleClaims, Blocking, len(claimMsgs) == 0, okOr(len(claimMsgs) == 0,
		fmt.Sprintf("all %d claims cite a source and quote it word for word (%d quotes; this does not prove the claims are true)", len(in.script.Claims), quotes),
		clip(strings.Join(claimMsgs, "; "))),
		"write the script again, quoting each claim exactly from its source")
	add(historyRuleBeats, Blocking, len(beatMsgs) == 0, okOr(len(beatMsgs) == 0,
		fmt.Sprintf("all %d beats carry a claim or are marked no_factual_claim", len(in.script.Beats)), clip(strings.Join(beatMsgs, "; "))),
		"write the script again so every beat states a claim or is marked as framing")
	add(historyRuleUncertainty, Blocking, len(hedgeMsgs) == 0, okOr(len(hedgeMsgs) == 0,
		"every beat that cites a speculation or disputed claim carries a hedging phrase (a phrase-list check, not a reading of the sentence)",
		clip(strings.Join(hedgeMsgs, "; "))),
		"write the script again so speculation and disputed claims are worded as such")
	add(historyRuleNumbers, Warning, len(numMsgs) == 0, okOr(len(numMsgs) == 0,
		"every number in the narration appears in a source (literal match; number words are not checked)", clip(strings.Join(numMsgs, "; "))),
		"check the number against the sources, or write the script again")

	// The subtitle stage puts zero-width spaces between Thai words; they are not part of the text.
	subs = strings.ReplaceAll(subs, "​", "")
	card := strings.Contains(subs, historyVisualSpec.SourcesHeading)
	add(historyRuleCard, Blocking, card, okOr(card, "the Sources card is in the subtitles", "no Sources card in the subtitles"), "rerun the subtitles stage")

	add(historyRuleCurrent, Warning, len(in.newer) == 0, okOr(len(in.newer) == 0, "the script uses the latest version of every source",
		"newer versions exist for: "+strings.Join(in.newer, ", ")), "write the script again to use the newer sources")

	unlabelled := 0
	for _, m := range in.stills {
		if m["image_kind"] != ImageKindIllustration || m["evidence"] != false {
			unlabelled++
		}
	}
	add(historyRuleIllustration, Blocking, unlabelled == 0, okOr(unlabelled == 0,
		fmt.Sprintf("%d stills, all labelled illustration and not evidence", len(in.stills)),
		fmt.Sprintf("%d of %d stills are not labelled as illustrations", unlabelled, len(in.stills))), "rerun the images stage")
	return res
}
