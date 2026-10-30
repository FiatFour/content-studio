package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/auth/authtest"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// ksEpisode seeds a knowledge_shorts episode directly: the profile is not runnable, so the API cannot create one.
func ksEpisode(t *testing.T, db *pgxpool.Pool) int64 {
	t.Helper()
	ctx := context.Background()
	var pid, eid int64
	if err := db.QueryRow(ctx, `INSERT INTO projects (name, profile) VALUES ('Knowledge Shorts', 'knowledge_shorts') RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO episodes (project_id, number, title, idea, target_duration_s) VALUES ($1, 1, 't', 'an idea long enough', 45) RETURNING id`, pid).Scan(&eid); err != nil {
		t.Fatal(err)
	}
	return eid
}

func srcBody(title, cite, text string) string {
	b, _ := json.Marshal(map[string]string{"title": title, "citation": cite, "text": text})
	return string(b)
}

func getText(t *testing.T, ts *httptest.Server, path string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestSourcesCreateVersionListAndContent(t *testing.T) {
	ts, db := serverDB(t)
	ep := ksEpisode(t, db)
	base := fmt.Sprintf("/api/episodes/%d/sources", ep)

	code, first := call(t, ts, "POST", base, "", srcBody("Water facts", "book p. 4, not fetched", "Water boils at 100 C\r\nat sea level.\tOK"))
	if code != 201 || first["source_id"] != "s1" || first["version"] != float64(1) {
		t.Fatalf("create: %d %v", code, first)
	}
	code, second := call(t, ts, "POST", base, "", srcBody("Other", "", "Second source"))
	if code != 201 || second["source_id"] != "s2" {
		t.Fatalf("create 2: %d %v", code, second)
	}
	code, v2 := call(t, ts, "POST", base+"/s1/versions", "", srcBody("Water facts (rev)", "book p. 4", "Water boils at 100 C at sea level.\nCorrected."))
	if code != 201 || v2["source_id"] != "s1" || v2["version"] != float64(2) || v2["id"] == first["id"] || v2["sha256"] == first["sha256"] {
		t.Fatalf("version: %d %v", code, v2)
	}

	// Content: v1 keeps its own bytes (CRLF normalized), v2 has its own; served as text, never HTML.
	st, body, h := getText(t, ts, fmt.Sprintf("/api/artifacts/%v/content", first["id"]))
	if st != 200 || body != "Water boils at 100 C\nat sea level.\tOK" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("v1 content: %d %q %v", st, body, h)
	}
	st, body, _ = getText(t, ts, fmt.Sprintf("/api/artifacts/%v/content", v2["id"]))
	if st != 200 || body != "Water boils at 100 C at sea level.\nCorrected." {
		t.Fatalf("v2 content: %d %q", st, body)
	}

	// Lineage: v2's parent is v1.
	var parent int64
	if err := db.QueryRow(context.Background(), `SELECT parent_id FROM artifact_parents WHERE artifact_id = $1`, int64(v2["id"].(float64))).Scan(&parent); err != nil || parent != int64(first["id"].(float64)) {
		t.Fatalf("parent = %d, %v", parent, err)
	}

	code, list := call(t, ts, "GET", base, "", "")
	srcs, _ := list["sources"].([]any)
	if code != 200 || len(srcs) != 2 {
		t.Fatalf("list: %d %v", code, list)
	}
	s1 := srcs[0].(map[string]any)
	if s1["source_id"] != "s1" || s1["version"] != float64(2) || s1["id"] != v2["id"] || s1["sha256"] != v2["sha256"] || s1["title"] != "Water facts (rev)" || len(s1["versions"].([]any)) != 2 {
		t.Fatalf("latest of s1: %v", s1)
	}

	// Sources do not leak into run-based views, and the history endpoint still works with run_id NULL.
	if code, h := call(t, ts, "GET", fmt.Sprintf("/api/episodes/%d/artifact-history", ep), "", ""); code != 200 || len(h["artifacts"].([]any)) != 3 {
		t.Fatalf("history: %d %v", code, h)
	}

	// An unchanged resubmission adds nothing; an unknown source is 404.
	if code, _ := call(t, ts, "POST", base+"/s1/versions", "", srcBody("Water facts (rev)", "book p. 4", "Water boils at 100 C at sea level.\nCorrected.")); code != 409 {
		t.Fatalf("unchanged: %d", code)
	}
	if code, _ := call(t, ts, "POST", base+"/s9/versions", "", srcBody("x", "", "y")); code != 404 {
		t.Fatalf("unknown source: %d", code)
	}
	if code, _ := call(t, ts, "POST", base+"/abc/versions", "", srcBody("x", "", "y")); code != 400 {
		t.Fatalf("bad source id: %d", code)
	}
}

func TestSourceArtifactsAreImmutable(t *testing.T) {
	ts, db := serverDB(t)
	ep := ksEpisode(t, db)
	_, r := call(t, ts, "POST", fmt.Sprintf("/api/episodes/%d/sources", ep), "", srcBody("t", "", "text"))
	id := int64(r["id"].(float64))
	ctx := context.Background()
	for _, q := range []string{`UPDATE artifacts SET sha256 = 'x' WHERE id = $1`, `UPDATE artifacts SET meta = '{}' WHERE id = $1`, `UPDATE artifacts SET size_bytes = 1 WHERE id = $1`, `DELETE FROM artifacts WHERE id = $1`} {
		if _, err := db.Exec(ctx, q, id); err == nil {
			t.Fatalf("%s succeeded", q)
		}
	}
	// The new constraint keeps NULL run/job for sources only.
	var pid int64
	_ = db.QueryRow(ctx, `SELECT project_id FROM artifacts WHERE id = $1`, id).Scan(&pid)
	if _, err := db.Exec(ctx, `INSERT INTO artifacts (project_id, episode_id, type, version, storage_path, sha256, size_bytes, validation_status) VALUES ($1, $2, 'script', 1, 'p', 's', 1, 'valid')`, pid, ep); err == nil {
		t.Fatal("a non-source artifact without run/job was accepted")
	}
}

func TestSourceValidation(t *testing.T) {
	ts, db := serverDB(t)
	ep := ksEpisode(t, db)
	base := fmt.Sprintf("/api/episodes/%d/sources", ep)
	long := strings.Repeat("a", api.MaxSourceBytes+1)
	ok := strings.Repeat("ก", api.MaxSourceBytes/3) // exactly at the cap in bytes
	cases := []struct {
		name, body string
		want       int
	}{
		{"empty title", srcBody("  ", "", "x"), 400},
		{"long title", srcBody(strings.Repeat("t", api.MaxSourceTitleRunes+1), "", "x"), 400},
		{"newline in title", srcBody("a\nb", "", "x"), 400},
		{"long citation", srcBody("t", strings.Repeat("c", api.MaxSourceCiteRunes+1), "x"), 400},
		{"empty text", srcBody("t", "", " \n"), 400},
		{"NUL", srcBody("t", "", "a\x00b"), 400},
		{"escape char", srcBody("t", "", "a\x1bb"), 400},
		{"bare CR", srcBody("t", "", "a\rb"), 400},
		{"C1 control", srcBody("t", "", "a\u0085b"), 400},
		{"too big", srcBody("t", "", long), 413},
		{"bad json", `{"title":`, 400},
		{"at cap", srcBody("t", "", ok), 201},
	}
	for _, c := range cases {
		if code, body := call(t, ts, "POST", base, "", c.body); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, code, body, c.want)
		}
	}
	// Invalid UTF-8 cannot come through JSON decoding as-is (Go replaces it with U+FFFD), so it is stored as the
	// replacement character, never as raw bytes; only the one accepted source above exists.
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM artifacts WHERE type = 'source'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("sources stored = %d, %v; want only the at-cap one", n, err)
	}
}

func TestSourceCountCap(t *testing.T) {
	ts, db := serverDB(t)
	ep := ksEpisode(t, db)
	base := fmt.Sprintf("/api/episodes/%d/sources", ep)
	for i := 1; i <= api.MaxSourcesPerEpisode; i++ {
		if code, body := call(t, ts, "POST", base, "", srcBody(fmt.Sprintf("s%d", i), "", "text")); code != 201 {
			t.Fatalf("source %d: %d %v", i, code, body)
		}
	}
	if code, _ := call(t, ts, "POST", base, "", srcBody("extra", "", "text")); code != 409 {
		t.Fatalf("source over the cap: %d", code)
	}
	// New versions of existing sources are still allowed at the cap, and ids order numerically.
	if code, _ := call(t, ts, "POST", base+"/s10/versions", "", srcBody("again", "", "changed")); code != 201 {
		t.Fatalf("version at cap: %d", code)
	}
	_, list := call(t, ts, "GET", base, "", "")
	srcs := list["sources"].([]any)
	if len(srcs) != api.MaxSourcesPerEpisode || srcs[1].(map[string]any)["source_id"] != "s2" || srcs[9].(map[string]any)["source_id"] != "s10" {
		t.Fatalf("order: %v", srcs)
	}
}

func TestSourcesRefuseOtherProfiles(t *testing.T) {
	ts, db := serverDB(t)
	_, fs := storetest.Episode(t, db) // fictional_series
	base := fmt.Sprintf("/api/episodes/%d/sources", fs)
	for _, m := range [][2]string{{"POST", base}, {"GET", base}, {"POST", base + "/s1/versions"}} {
		code, body := call(t, ts, m[0], m[1], "", srcBody("t", "", "x"))
		if code != 409 || !strings.Contains(fmt.Sprint(body["error"]), "knowledge_shorts") {
			t.Errorf("%s %s: %d %v", m[0], m[1], code, body)
		}
	}
	if code, _ := call(t, ts, "GET", "/api/episodes/99999/sources", "", ""); code != 404 {
		t.Fatalf("missing episode: %d", code)
	}
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM artifacts WHERE type = 'source'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("sources = %d, %v", n, err)
	}
}

func TestSourceAuditHasScalarsOnlyAndRollsBackWithoutIt(t *testing.T) {
	ts, db := serverDB(t)
	ep := ksEpisode(t, db)
	ctx := context.Background()
	base := fmt.Sprintf("/api/episodes/%d/sources", ep)
	secret := "UNIQUE-SECRET-SOURCE-TEXT"
	_, r := call(t, ts, "POST", base, "", srcBody("My title "+secret, "cite "+secret, secret))
	call(t, ts, "POST", base+"/s1/versions", "", srcBody("t2", "", secret+" v2"))
	wantActions(t, db, "source.create", "source.version")
	var details string
	if err := db.QueryRow(ctx, `SELECT string_agg(detail::text, ' ') FROM audit_log`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(details, "SECRET") || strings.Contains(details, "title") {
		t.Fatalf("audit detail carries source text: %s", details)
	}
	var sha, key string
	var size int64
	if err := db.QueryRow(ctx, `SELECT detail->>'sha_prefix', detail->>'source_id', (detail->>'size_bytes')::bigint FROM audit_log ORDER BY id LIMIT 1`).Scan(&sha, &key, &size); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r["sha256"].(string), sha) || key != "s1" || size != int64(len(secret)) {
		t.Fatalf("audit detail %q %q %d vs %v", sha, key, size, r)
	}

	breakAudit(t, db)
	for _, p := range []string{base, base + "/s1/versions"} {
		if code, _ := call(t, ts, "POST", p, "", srcBody("new", "", "text after break")); code != 500 {
			t.Fatalf("POST %s with a broken audit: %d, want 500", p, code)
		}
	}
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE type = 'source'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("artifacts after failed audit = %d, %v; want the 2 from before", n, err)
	}
}

func TestSourceFileIsRemovedWhenTheTransactionFails(t *testing.T) {
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Auth = authtest.Service(t, db)
	ts := httptest.NewServer(authtest.AutoLogin(s.Echo(), authtest.Session(t, s.Auth)))
	t.Cleanup(ts.Close)
	ep := ksEpisode(t, db)
	base := fmt.Sprintf("/api/episodes/%d/sources", ep)
	files := func() (out []string) {
		_ = filepath.Walk(st.Root, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				out = append(out, p)
			}
			return nil
		})
		return out
	}
	breakAudit(t, db)
	if code, _ := call(t, ts, "POST", base, "", srcBody("t", "", "text")); code != 500 {
		t.Fatalf("create with a broken audit: %d", code)
	}
	if f := files(); len(f) != 0 {
		t.Fatalf("orphan files after a rolled-back source: %v", f)
	}
}
