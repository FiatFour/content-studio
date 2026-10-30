package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
)

// Knowledge Shorts QA rules (rule set qa-rules-shorts-v1). They run inside the shared QA stage, after
// the shared checks, when the run's script says it is a Knowledge Shorts script. Every rule is
// deterministic: no model is asked. The claim rules prove that a quote is in the source, not that the
// claim is true; the owner reviews that.
const (
	shortsRuleDuration = "shorts_duration_range"
	shortsRuleSources  = "shorts_sources_intact"
	shortsRuleClaims   = "shorts_claims_quoted"
	shortsRuleBeats    = "shorts_beats_supported"
	shortsRuleCard     = "shorts_sources_card"
	shortsRuleCurrent  = "shorts_sources_current"
)

// shortsSourceRef is one source version named in the script artifact's metadata.
type shortsSourceRef struct {
	SourceID   string
	ArtifactID int64
	SHA256     string
}

// shortsSourcesFromMeta reads the "sources" list the script stage stored on the script artifact.
func shortsSourcesFromMeta(meta map[string]any) []shortsSourceRef {
	var out []shortsSourceRef
	list, _ := meta["sources"].([]any)
	for _, u := range list {
		m, ok := u.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["artifact_id"].(float64)
		key, _ := m["source_id"].(string)
		sum, _ := m["sha256"].(string)
		out = append(out, shortsSourceRef{SourceID: key, ArtifactID: int64(id), SHA256: sum})
	}
	return out
}

// shortsQAInput is what the Shorts rules read: the stored script and the source versions it was built from.
type shortsQAInput struct {
	script  *ShortsScript
	sources []SourceText
	// problems lists sources that are missing, unreadable or changed since the script was made.
	problems []string
	// newer lists source ids that have a newer version than the one the script used.
	newer []string
}

// isShortsScript reports whether a script artifact is a Knowledge Shorts script.
func isShortsScript(a artifacts.Artifact) bool { return a.Meta["profile"] == KnowledgeShortsID }

// loadShortsQA re-reads the script and every source version it names, by artifact id. A source whose
// artifact row is gone, whose file is missing, or whose hash differs from the recorded one is a
// problem; its text is then left out, so its claims fail the quote check too.
func (d *Deps) loadShortsQA(ctx context.Context, scriptArt artifacts.Artifact) (*shortsQAInput, error) {
	data, err := d.read(scriptArt)
	if err != nil {
		return nil, err
	}
	var ss ShortsScript
	if err := json.Unmarshal(data, &ss); err != nil {
		return nil, jobs.Errorf(jobs.ErrInvalidInput, nil, "the stored shorts script is unreadable")
	}
	in := &shortsQAInput{script: &ss}
	for _, ref := range shortsSourcesFromMeta(scriptArt.Meta) {
		var a artifacts.Artifact
		err := d.DB.QueryRow(ctx, `SELECT id, project_id, episode_id, type, item_key, version, storage_path, sha256, size_bytes FROM artifacts
			WHERE id = $1 AND type = 'source' AND episode_id = $2`, ref.ArtifactID, scriptArt.EpisodeID).
			Scan(&a.ID, &a.ProjectID, &a.EpisodeID, &a.Type, &a.ItemKey, &a.Version, &a.StoragePath, &a.SHA256, &a.SizeBytes)
		if err != nil {
			in.problems = append(in.problems, fmt.Sprintf("source %s (artifact %d) is missing", ref.SourceID, ref.ArtifactID))
			continue
		}
		if a.ItemKey != ref.SourceID || a.SHA256 != ref.SHA256 {
			in.problems = append(in.problems, fmt.Sprintf("source %s (artifact %d) no longer matches what the script was made from", ref.SourceID, ref.ArtifactID))
			continue
		}
		text, err := d.read(a)
		if err != nil {
			in.problems = append(in.problems, fmt.Sprintf("source %s (artifact %d) is missing on disk or does not match its hash", ref.SourceID, ref.ArtifactID))
			continue
		}
		in.sources = append(in.sources, SourceText{ID: ref.SourceID, Text: string(text)})
		var latest int
		if err := d.DB.QueryRow(ctx, `SELECT max(version) FROM artifacts WHERE episode_id = $1 AND type = 'source' AND item_key = $2`, a.EpisodeID, a.ItemKey).Scan(&latest); err == nil && latest > a.Version {
			in.newer = append(in.newer, ref.SourceID)
		}
	}
	return in, nil
}

// shortsRules returns the Knowledge Shorts rules. durS is the measured video length (0 when it could
// not be measured), subs the subtitle file text ("" when unreadable).
func shortsRules(in *shortsQAInput, e Episode, durS float64, subs string, spec Spec) []QAResult {
	var res []QAResult
	add := func(rule string, sev Severity, ok bool, reason, fix string) {
		res = append(res, QAResult{Rule: rule, RuleVersion: 1, Severity: sev, Passed: ok, Reason: reason, SuggestedFix: fix})
	}
	target := float64(e.TargetDurationS)
	inRange := durS >= float64(spec.MinDurationS) && durS <= float64(spec.MaxDurationS) && math.Abs(durS-target) <= target*durationTolPct/100
	add(shortsRuleDuration, Blocking, inRange, fmt.Sprintf("%.2f s (a short must be %d to %d s and within %d%% of the %d s target)",
		durS, spec.MinDurationS, spec.MaxDurationS, durationTolPct, e.TargetDurationS), "set the target between 30 and 60 s and adjust the script length")

	srcOK := len(in.problems) == 0 && len(in.sources) > 0
	add(shortsRuleSources, Blocking, srcOK,
		okOr(srcOK, fmt.Sprintf("%d source versions found and unchanged", len(in.sources)), orText(strings.Join(in.problems, "; "), "the script names no source")),
		"restore the source or write the script again from the current sources")

	check := CheckShortsClaims(in.script, in.sources)
	var claimMsgs, beatMsgs []string
	for _, f := range check.Failures {
		switch f.Code {
		case BeatNoSupport, BeatContradictory, BeatUnknownClaim:
			beatMsgs = append(beatMsgs, f.Message)
		default:
			claimMsgs = append(claimMsgs, f.Message)
		}
	}
	if len(in.script.Claims) == 0 {
		claimMsgs = append(claimMsgs, "the script has no claims")
	}
	add(shortsRuleClaims, Blocking, len(claimMsgs) == 0, okOr(len(claimMsgs) == 0,
		fmt.Sprintf("all %d claims cite a source and quote it word for word (this does not prove the claims are true)", check.Claims), clip(strings.Join(claimMsgs, "; "))),
		"write the script again, quoting each claim exactly from its source")
	add(shortsRuleBeats, Blocking, len(beatMsgs) == 0, okOr(len(beatMsgs) == 0,
		fmt.Sprintf("all %d beats carry a claim or are marked no_factual_claim", check.Beats), clip(strings.Join(beatMsgs, "; "))),
		"write the script again so every beat states a claim or is marked as framing")

	// The subtitle stage puts zero-width spaces between Thai words; they are not part of the text.
	subs = strings.ReplaceAll(subs, "​", "")
	card := spec.Visual != nil && spec.Visual.SourcesHeading != "" && strings.Contains(subs, spec.Visual.SourcesHeading)
	add(shortsRuleCard, Blocking, card, okOr(card, "the Sources card is in the subtitles", "no Sources card in the subtitles"), "rerun the subtitles stage")

	add(shortsRuleCurrent, Warning, len(in.newer) == 0, okOr(len(in.newer) == 0, "the script uses the latest version of every source",
		"newer versions exist for: "+strings.Join(in.newer, ", ")), "write the script again to use the newer sources")
	return res
}

func orText(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// clip keeps a reason short enough to show in a table.
func clip(s string) string {
	if r := []rune(s); len(r) > 400 {
		return string(r[:400]) + "..."
	}
	return s
}
