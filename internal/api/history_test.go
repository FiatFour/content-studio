package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/artifacts"
)

// seedJSON stores a script version in the same episode/run/job as base, with parents.
func seedJSON(t *testing.T, db *pgxpool.Pool, st *artifacts.Store, base artifacts.Artifact, content string, parents ...int64) artifacts.Artifact {
	t.Helper()
	ctx := context.Background()
	loc := artifacts.Loc{EpisodeID: base.EpisodeID, RunID: base.RunID, JobID: base.JobID, Attempt: nextAttempt()}
	file, err := st.Write(loc, artifacts.Script, "", func(w io.Writer) error { _, err := io.WriteString(w, content); return err })
	if err != nil {
		t.Fatal(err)
	}
	a := artifacts.Artifact{ProjectID: base.ProjectID, EpisodeID: base.EpisodeID, RunID: base.RunID, JobID: base.JobID, Type: artifacts.Script,
		StoragePath: file.RelPath, SHA256: file.SHA256, SizeBytes: file.Size, Parents: parents}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifacts.Insert(ctx, tx, &a); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return a
}

var attemptSeq int

// nextAttempt gives each seeded file its own directory, as separate job attempts do.
func nextAttempt() int { attemptSeq++; return 100 + attemptSeq }

func getRaw(t *testing.T, ts interface{ Client() *http.Client }, url string) (int, []byte) {
	t.Helper()
	resp, err := ts.Client().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestArtifactHistoryShowsLineageAndApprovalPerExactHash(t *testing.T) {
	ts, db, st := reviewServer(t)
	vid := seedVideo(t, db, st, []byte("mp4"), "qa_passed")
	s1 := seedJSON(t, db, st, vid, `{"title":"one"}`)
	s2 := seedJSON(t, db, st, vid, `{"title":"two"}`, s1.ID)
	ctx := context.Background()
	// s1 approved against its real hash; s2 has a decision recorded against a different hash only.
	if _, err := db.Exec(ctx, `INSERT INTO approvals (artifact_id, sha256, decision, reviewer) VALUES ($1, $2, 'approved', 'owner')`, s1.ID, s1.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO approvals (artifact_id, sha256, decision, reviewer) VALUES ($1, $2, 'approved', 'owner')`, s2.ID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	code, raw := getRaw(t, ts, ts.URL+"/api/episodes/"+strconv.FormatInt(vid.EpisodeID, 10)+"/artifact-history?type=script")
	if code != 200 {
		t.Fatalf("status %d %s", code, raw)
	}
	var got struct {
		Artifacts []struct {
			ID       int64   `json:"id"`
			Version  int     `json:"version"`
			SHA256   string  `json:"sha256"`
			Parents  []int64 `json:"parents"`
			Approval string  `json:"approval"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Artifacts) != 2 {
		t.Fatalf("type filter: %d rows, want 2", len(got.Artifacts))
	}
	n2, n1 := got.Artifacts[0], got.Artifacts[1] // newest first
	if n1.ID != s1.ID || n1.Approval != "approved" || len(n1.Parents) != 0 {
		t.Fatalf("v1: %+v", n1)
	}
	if n2.ID != s2.ID || n2.Approval != "none" || len(n2.Parents) != 1 || n2.Parents[0] != s1.ID || n2.Version != 2 || n2.SHA256 != s2.SHA256 {
		t.Fatalf("v2 must not count a decision for another hash: %+v", n2)
	}
	if code, _ := getRaw(t, ts, ts.URL+"/api/episodes/999999/artifact-history"); code != 404 {
		t.Fatalf("unknown episode: %d", code)
	}
}

func TestCompareJSONStructuredDiffAndGuards(t *testing.T) {
	ts, db, st := reviewServer(t)
	vid := seedVideo(t, db, st, []byte("mp4"), "qa_passed")
	s1 := seedJSON(t, db, st, vid, `{"title":"one","shots":[{"n":1},{"n":2}],"gone":true,"html":"<b>x</b>"}`)
	s2 := seedJSON(t, db, st, vid, `{"title":"two","shots":[{"n":1}],"new":1,"html":"<b>x</b>"}`, s1.ID)
	q := func(a, b int64) string {
		return ts.URL + "/api/artifacts/compare?" + url.Values{"a": {strconv.FormatInt(a, 10)}, "b": {strconv.FormatInt(b, 10)}}.Encode()
	}
	code, raw := getRaw(t, ts, q(s1.ID, s2.ID))
	if code != 200 {
		t.Fatalf("status %d %s", code, raw)
	}
	var got struct {
		Kind      string `json:"kind"`
		Identical bool   `json:"identical"`
		Diffs     []struct{ Path, Op, Before, After string }
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"/title": "changed", "/shots/1": "removed", "/gone": "removed", "/new": "added"}
	if got.Kind != "json" || got.Identical || len(got.Diffs) != len(want) {
		t.Fatalf("diff: %s", raw)
	}
	for _, d := range got.Diffs {
		if want[d.Path] != d.Op {
			t.Fatalf("unexpected diff %+v in %s", d, raw)
		}
	}
	// Same version against itself is identical with no diffs.
	_, raw = getRaw(t, ts, q(s1.ID, s1.ID))
	if !strings.Contains(string(raw), `"identical":true`) || strings.Contains(string(raw), `"path"`) {
		t.Fatalf("self compare: %s", raw)
	}
	// Different types and bad ids are refused.
	if code, _ := getRaw(t, ts, q(s1.ID, vid.ID)); code != 400 {
		t.Fatalf("cross-type compare: %d", code)
	}
	if code, _ := getRaw(t, ts, ts.URL+"/api/artifacts/compare?a=x&b=1"); code != 400 {
		t.Fatalf("bad id: %d", code)
	}
	// Another episode's artifact is refused.
	other := seedVideo(t, db, st, []byte("mp4-other"), "qa_passed")
	other2 := seedJSON(t, db, st, other, `{}`)
	if code, _ := getRaw(t, ts, q(s1.ID, other2.ID)); code != 400 {
		t.Fatalf("cross-episode compare: %d", code)
	}
}

func TestCompareVideoIsMetadataOnly(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("mp4"), "qa_passed")
	code, raw := getRaw(t, ts, ts.URL+"/api/artifacts/compare?a="+strconv.FormatInt(v.ID, 10)+"&b="+strconv.FormatInt(v.ID, 10))
	if code != 200 || !strings.Contains(string(raw), `"kind":"video"`) {
		t.Fatalf("%d %s", code, raw)
	}
}
