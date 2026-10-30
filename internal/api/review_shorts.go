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

// shortsReviewSource is one source version a Knowledge Shorts script was made from.
type shortsReviewSource struct {
	SourceID   string `json:"source_id"`
	ArtifactID int64  `json:"artifact_id"`
	Title      string `json:"title"`
	Version    int    `json:"version"`
	SHA256     string `json:"sha256"`
}

// shortsReview is the read-only part of the review response that only a Knowledge Shorts video has: the
// sources used, the claims with their source and quote, and the claim check stored with the script. It
// is data for the owner to read. The check proves a quote is in its source, not that the claim is true.
type shortsReview struct {
	Sources    []shortsReviewSource   `json:"sources"`
	Claims     []pipeline.ShortsClaim `json:"claims"`
	ClaimBeats map[string][]int       `json:"claim_beats"`
	Beats      int                    `json:"beats"`
	ClaimCheck *pipeline.ClaimCheck   `json:"claim_check"`
	Unreadable string                 `json:"unreadable,omitempty"`
}

// loadShortsReview returns nil for any script that is not a Knowledge Shorts script, so a Fictional
// Series review response is unchanged. It reads the stored script (hash-checked) and the source rows
// named in the script's metadata. Nothing here writes, and no file path is returned.
func (s *Server) loadShortsReview(ctx context.Context, scriptID int64) (*shortsReview, error) {
	var rel, sum string
	var epID int64
	var meta map[string]any
	if err := s.DB.QueryRow(ctx, `SELECT storage_path, sha256, episode_id, meta FROM artifacts WHERE id = $1`, scriptID).Scan(&rel, &sum, &epID, &meta); err != nil {
		return nil, err
	}
	if meta["profile"] != pipeline.KnowledgeShortsID {
		return nil, nil
	}
	out := &shortsReview{Sources: []shortsReviewSource{}, Claims: []pipeline.ShortsClaim{}, ClaimBeats: map[string][]int{}}
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
	var ss pipeline.ShortsScript
	if err := json.Unmarshal(data, &ss); err != nil {
		out.Unreadable = "script file is not a shorts script"
		return out, nil
	}
	if ss.Claims != nil {
		out.Claims = ss.Claims
	}
	out.Beats, out.ClaimCheck = len(ss.Beats), ss.ClaimCheck
	for i, b := range ss.Beats {
		for _, id := range b.ClaimIDs {
			out.ClaimBeats[id] = append(out.ClaimBeats[id], i+1)
		}
	}
	used, _ := meta["sources"].([]any)
	for _, u := range used {
		m, _ := u.(map[string]any)
		aid, _ := m["artifact_id"].(float64)
		var src shortsReviewSource
		err := s.DB.QueryRow(ctx, `SELECT item_key, id, coalesce(meta->>'title', ''), version, sha256 FROM artifacts
			WHERE id = $1 AND type = 'source' AND episode_id = $2`, int64(aid), epID).Scan(&src.SourceID, &src.ArtifactID, &src.Title, &src.Version, &src.SHA256)
		if err != nil {
			key, _ := m["source_id"].(string)
			src = shortsReviewSource{SourceID: key, ArtifactID: int64(aid), Title: "(source row not found)"}
		}
		src.Title = strings.TrimSpace(src.Title)
		out.Sources = append(out.Sources, src)
	}
	return out, nil
}
