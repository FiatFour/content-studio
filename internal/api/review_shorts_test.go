package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/pipeline"
)

// seedScriptFor stores a script artifact (with the given meta) in the video's run and makes the video descend from it.
func seedScriptFor(t *testing.T, db *pgxpool.Pool, st *artifacts.Store, v artifacts.Artifact, body []byte, meta map[string]any) artifacts.Artifact {
	t.Helper()
	ctx := context.Background()
	f, err := st.Write(artifacts.Loc{EpisodeID: v.EpisodeID, RunID: v.RunID, JobID: v.JobID, Attempt: 1}, artifacts.Script, "",
		func(w io.Writer) error { _, err := w.Write(body); return err })
	if err != nil {
		t.Fatal(err)
	}
	a := artifacts.Artifact{ProjectID: v.ProjectID, EpisodeID: v.EpisodeID, RunID: v.RunID, JobID: v.JobID, Type: artifacts.Script,
		StoragePath: f.RelPath, SHA256: f.SHA256, SizeBytes: f.Size, Meta: meta}
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := artifacts.Insert(ctx, tx, &a); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO artifact_parents (artifact_id, parent_id) VALUES ($1, $2)`, v.ID, a.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func seedSource(t *testing.T, db *pgxpool.Pool, st *artifacts.Store, v artifacts.Artifact, key, title, text string) artifacts.Artifact {
	t.Helper()
	ctx := context.Background()
	f, err := st.Write(artifacts.Loc{EpisodeID: v.EpisodeID, Sub: "sources/" + key + "/v1"}, artifacts.Source, key,
		func(w io.Writer) error { _, err := io.WriteString(w, text); return err })
	if err != nil {
		t.Fatal(err)
	}
	a := artifacts.Artifact{ProjectID: v.ProjectID, EpisodeID: v.EpisodeID, Type: artifacts.Source, ItemKey: key, StoragePath: f.RelPath,
		SHA256: f.SHA256, SizeBytes: f.Size, Provider: "owner", Meta: map[string]any{"title": title}}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return artifacts.Insert(ctx, tx, &a) }); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestReviewOfAKnowledgeShortsVideoListsSourcesAndClaims(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("shorts-video-bytes"), "qa_passed")
	src := seedSource(t, db, st, v, "s1", "Sea water facts", "sea water is about 35 g/l")
	cc := pipeline.ClaimCheck{OK: true, Claims: 1, Beats: 2, Failures: []pipeline.ClaimFailure{}}
	raw, err := json.Marshal(pipeline.ShortsScript{Title: "t", Language: "th",
		Beats:  []pipeline.ShortsBeat{{Narration: "hook", NoFactualClaim: true}, {Narration: "fact", ClaimIDs: []string{"c1"}}},
		Claims: []pipeline.ShortsClaim{{ID: "c1", Text: "salty", SourceID: "s1", Quote: "about 35 g/l"}}, ClaimCheck: &cc})
	if err != nil {
		t.Fatal(err)
	}
	seedScriptFor(t, db, st, v, raw, map[string]any{"profile": "knowledge_shorts",
		"sources": []any{map[string]any{"source_id": "s1", "artifact_id": src.ID, "version": 1, "sha256": src.SHA256}}})

	code, body := call(t, ts, "GET", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/review", "", "")
	if code != http.StatusOK {
		t.Fatalf("review: %d %v", code, body)
	}
	sh, _ := body["shorts"].(map[string]any)
	if sh == nil {
		t.Fatalf("no shorts block: %v", body)
	}
	srcs, _ := sh["sources"].([]any)
	s0, _ := srcs[0].(map[string]any)
	if len(srcs) != 1 || s0["source_id"] != "s1" || s0["title"] != "Sea water facts" || s0["version"].(float64) != 1 || s0["sha256"] != src.SHA256 {
		t.Fatalf("sources %v", srcs)
	}
	claims, _ := sh["claims"].([]any)
	c0, _ := claims[0].(map[string]any)
	if len(claims) != 1 || c0["id"] != "c1" || c0["source_id"] != "s1" || c0["quote"] != "about 35 g/l" {
		t.Fatalf("claims %v", claims)
	}
	if beats, _ := sh["claim_beats"].(map[string]any)["c1"].([]any); len(beats) != 1 || beats[0].(float64) != 2 {
		t.Fatalf("claim_beats %v", sh["claim_beats"])
	}
	if chk, _ := sh["claim_check"].(map[string]any); chk["ok"] != true {
		t.Fatalf("claim_check %v", sh["claim_check"])
	}
	// No file paths or secrets in the response; the decision still binds to the exact hash.
	all, _ := json.Marshal(body)
	for _, bad := range []string{st.Root, "storage_path", "/episodes/", "password", "token"} {
		if strings.Contains(string(all), bad) {
			t.Errorf("response contains %q", bad)
		}
	}
	path := "/api/videos/" + strconv.FormatInt(v.ID, 10) + "/decision"
	if code, _ := call(t, ts, "POST", path, "", `{"decision":"approved","sha256":"`+strings.Repeat("0", 64)+`"}`); code != http.StatusConflict {
		t.Fatalf("approve with a wrong hash: %d, want 409", code)
	}
	if code, _ := call(t, ts, "POST", path, "", `{"decision":"approved","sha256":"`+v.SHA256+`"}`); code != http.StatusCreated {
		t.Fatalf("approve with the exact hash: %d", code)
	}
}

func TestReviewOfAKnowledgeShortsVideoWithChangedScriptFileShowsNoClaims(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("shorts-video-bytes-2"), "qa_passed")
	a := seedScriptFor(t, db, st, v, []byte(`{"title":"t"}`), map[string]any{"profile": "knowledge_shorts"})
	p, _ := st.Abs(a.StoragePath)
	if err := os.WriteFile(p, []byte(`{"title":"changed"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, body := call(t, ts, "GET", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/review", "", "")
	sh, _ := body["shorts"].(map[string]any)
	if sh == nil || sh["unreadable"] == "" || len(sh["claims"].([]any)) != 0 {
		t.Fatalf("shorts block %v", sh)
	}
}

func TestReviewOfAFictionalSeriesVideoHasNoShortsBlock(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("fiction-video-bytes"), "qa_passed")
	seedScriptFor(t, db, st, v, []byte(`{"title":"t"}`), nil)
	code, body := call(t, ts, "GET", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/review", "", "")
	if code != http.StatusOK {
		t.Fatalf("review: %d", code)
	}
	if _, has := body["shorts"]; has {
		t.Fatalf("a fictional_series review must not have a shorts block: %v", body["shorts"])
	}
	if body["script_artifact_id"] == nil {
		t.Fatal("script_artifact_id should still be set")
	}
}
