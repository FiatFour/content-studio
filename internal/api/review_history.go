package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"

	"github.com/fiat/content-studio/internal/pipeline"
)

// historyReviewSource is one source version a History & Mystery script was made from.
type historyReviewSource struct {
	SourceID   string `json:"source_id"`
	ArtifactID int64  `json:"artifact_id"`
	Title      string `json:"title"`
	Version    int    `json:"version"`
	SHA256     string `json:"sha256"`
}

// historyReview is the read-only part of the review response that only a History & Mystery video has:
// the sources used, each claim with its certainty (documented, disputed or speculation), date text and
// quoted sources, the stored claim check, and the findings of the History checks computed again from
// the stored script and the exact source versions. The findings prove quotes are in the sources and
// that uncertain claims are hedged (a phrase-list check); they do not prove a claim is true. Stills of
// this profile are illustrations, never evidence; the note is part of the payload so the page cannot
// forget it.
type historyReview struct {
	Sources    []historyReviewSource     `json:"sources"`
	Claims     []pipeline.HistoryClaim   `json:"claims"`
	ClaimBeats map[string][]int          `json:"claim_beats"`
	Beats      int                       `json:"beats"`
	ClaimCheck *pipeline.ClaimCheck      `json:"claim_check"`
	Findings   []pipeline.HistoryFinding `json:"findings"`
	Certainty  map[string]int            `json:"certainty_counts"`
	Unreadable string                    `json:"unreadable,omitempty"`
	StillsKind string                    `json:"stills_kind"`
}

// loadHistoryReview returns nil for any script that is not a History script, so the review response of
// the other profiles is unchanged. It reads the stored script (hash-checked) and the source rows named
// in the script's metadata. Nothing here writes, and no file path is returned.
func (s *Server) loadHistoryReview(ctx context.Context, scriptID int64) (*historyReview, error) {
	var rel, sum string
	var epID int64
	var meta map[string]any
	if err := s.DB.QueryRow(ctx, `SELECT storage_path, sha256, episode_id, meta FROM artifacts WHERE id = $1`, scriptID).Scan(&rel, &sum, &epID, &meta); err != nil {
		return nil, err
	}
	if meta["profile"] != pipeline.HistoryMysteryID {
		return nil, nil
	}
	out := &historyReview{Sources: []historyReviewSource{}, Claims: []pipeline.HistoryClaim{}, ClaimBeats: map[string][]int{},
		Findings: []pipeline.HistoryFinding{}, Certainty: map[string]int{}, StillsKind: pipeline.ImageKindIllustration}
	path, err := s.Store.Abs(rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		out.Unreadable = "script file is missing"
		return out, nil
	}
	if h := sha256.Sum256(data); hex.EncodeToString(h[:]) != sum {
		out.Unreadable = "script file does not match its recorded sha256"
		return out, nil
	}
	var hs pipeline.HistoryScript
	if err := json.Unmarshal(data, &hs); err != nil {
		out.Unreadable = "script file is not a history script"
		return out, nil
	}
	if hs.Claims != nil {
		out.Claims = hs.Claims
	}
	out.Beats, out.ClaimCheck = len(hs.Beats), hs.ClaimCheck
	for _, c := range hs.Claims {
		out.Certainty[c.Certainty]++
	}
	for i, b := range hs.Beats {
		for _, id := range b.ClaimIDs {
			out.ClaimBeats[id] = append(out.ClaimBeats[id], i+1)
		}
	}
	var texts []pipeline.SourceText
	used, _ := meta["sources"].([]any)
	for _, u := range used {
		m, _ := u.(map[string]any)
		aid, _ := m["artifact_id"].(float64)
		var src historyReviewSource
		var srel string
		err := s.DB.QueryRow(ctx, `SELECT item_key, id, coalesce(meta->>'title', ''), version, sha256, storage_path FROM artifacts
			WHERE id = $1 AND type = 'source' AND episode_id = $2`, int64(aid), epID).Scan(&src.SourceID, &src.ArtifactID, &src.Title, &src.Version, &src.SHA256, &srel)
		if err != nil {
			key, _ := m["source_id"].(string)
			src = historyReviewSource{SourceID: key, ArtifactID: int64(aid), Title: "(source row not found)"}
		} else if p, perr := s.Store.Abs(srel); perr == nil {
			// The findings use the source text only when it still matches its recorded hash.
			if b, rerr := os.ReadFile(p); rerr == nil {
				if h := sha256.Sum256(b); hex.EncodeToString(h[:]) == src.SHA256 {
					texts = append(texts, pipeline.SourceText{ID: src.SourceID, Text: string(b)})
				}
			}
		}
		src.Title = strings.TrimSpace(src.Title)
		out.Sources = append(out.Sources, src)
	}
	out.Findings = pipeline.CheckHistoryScript(&hs, texts)
	return out, nil
}
