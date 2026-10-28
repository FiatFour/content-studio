package api_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fiat/content-studio/internal/api"
	"github.com/fiat/content-studio/internal/artifacts"
	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/review"
	"github.com/fiat/content-studio/internal/store/storetest"
)

// seedVideo stores a fake mp4 version with the given QA status, the same way the pipeline does.
func seedVideo(t *testing.T, db *pgxpool.Pool, st *artifacts.Store, content []byte, status string) artifacts.Artifact {
	t.Helper()
	ctx := context.Background()
	project, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: jobs.Plan{Full: []string{"render"}}, MaxAttempts: 1}
	runID, err := q.CreateRun(ctx, ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err := q.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file, err := st.Write(artifacts.Loc{EpisodeID: ep, RunID: runID, JobID: j.ID, Attempt: 1}, artifacts.Video, "",
		func(w io.Writer) error { _, err := w.Write(content); return err })
	if err != nil {
		t.Fatal(err)
	}
	a := artifacts.Artifact{ProjectID: project, EpisodeID: ep, RunID: runID, JobID: j.ID, Type: artifacts.Video, StoragePath: file.RelPath, SHA256: file.SHA256, SizeBytes: file.Size}
	err = q.Complete(ctx, j, func(tx pgx.Tx) error {
		if err := artifacts.Insert(ctx, tx, &a); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE artifacts SET validation_status = $1 WHERE id = $2`, status, a.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// reviewServer is serverDB that also returns the store the server reads from, so tests can seed artifacts.
func reviewServer(t *testing.T) (*httptest.Server, *pgxpool.Pool, *artifacts.Store) {
	t.Helper()
	db := storetest.New(t)
	st := &artifacts.Store{Root: t.TempDir()}
	s := &api.Server{DB: db, Queue: &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}, Store: st,
		Review: &review.Service{DB: db, Store: st, ExportDir: t.TempDir()},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewServer(s.Echo())
	t.Cleanup(ts.Close)
	return ts, db, st
}

func TestDecisionApproveWithRightHashThenWrongHash(t *testing.T) {
	ts, db, st := reviewServer(t)
	content := []byte("fake-mp4-bytes-approve")
	v := seedVideo(t, db, st, content, "qa_passed")
	path := "/api/videos/" + strconv.FormatInt(v.ID, 10) + "/decision"

	wrong := strings.Repeat("0", 64)
	code, body := call(t, ts, "POST", path, "", `{"decision":"approved","sha256":"`+wrong+`"}`)
	if code != http.StatusConflict {
		t.Fatalf("approve with a wrong sha256: %d %v, want 409", code, body)
	}
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM approvals WHERE artifact_id = $1`, v.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused approval left %d rows (err %v)", n, err)
	}

	code, body = call(t, ts, "POST", path, "", `{"decision":"approved","sha256":"`+v.SHA256+`"}`)
	if code != http.StatusCreated || body["decision"] != "approved" || body["sha256"] != v.SHA256 {
		t.Fatalf("approve with the right sha256: %d %v", code, body)
	}
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM approvals WHERE artifact_id = $1 AND decision = 'approved' AND sha256 = $2`, v.ID, v.SHA256).Scan(&n); err != nil || n != 1 {
		t.Fatalf("approval rows %d (err %v), want 1", n, err)
	}
}

func TestDecisionRefusesAVideoThatDidNotPassQA(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("bad-video"), "qa_failed")
	code, body := call(t, ts, "POST", "/api/videos/"+strconv.FormatInt(v.ID, 10)+"/decision", "", `{"decision":"approved","sha256":"`+v.SHA256+`"}`)
	if code != http.StatusConflict {
		t.Fatalf("approving a qa_failed video: %d %v, want 409", code, body)
	}
}

func TestExportRefusedBeforeApprovalThenCopiesApprovedBytes(t *testing.T) {
	ts, db, st := reviewServer(t)
	content := []byte("fake-mp4-bytes-export-0123456789")
	v := seedVideo(t, db, st, content, "qa_passed")
	id := strconv.FormatInt(v.ID, 10)

	code, body := call(t, ts, "POST", "/api/videos/"+id+"/export", "", "{}")
	if code != http.StatusConflict {
		t.Fatalf("export before approval: %d %v, want 409", code, body)
	}
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM exports WHERE artifact_id = $1`, v.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused export left %d rows (err %v)", n, err)
	}

	if code, body := call(t, ts, "POST", "/api/videos/"+id+"/decision", "", `{"decision":"approved","sha256":"`+v.SHA256+`"}`); code != http.StatusCreated {
		t.Fatalf("approve: %d %v", code, body)
	}
	code, body = call(t, ts, "POST", "/api/videos/"+id+"/export", "", "{}")
	if code != http.StatusCreated || body["sha256"] != v.SHA256 {
		t.Fatalf("export after approval: %d %v, want 201 with the approved sha256", code, body)
	}
	name, _ := body["file_name"].(string)
	if name == "" || strings.Contains(name, "/") {
		t.Fatalf("file_name %q should be a bare file name", name)
	}

	// The exported file on disk hashes to the approved sha256.
	var exportPath, exportSHA string
	if err := db.QueryRow(context.Background(), `SELECT export_path, sha256 FROM exports WHERE artifact_id = $1`, v.ID).Scan(&exportPath, &exportSHA); err != nil {
		t.Fatal(err)
	}
	got, err := artifacts.HashFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != v.SHA256 || exportSHA != v.SHA256 {
		t.Fatalf("exported file sha256 %s, recorded %s, approved %s", got, exportSHA, v.SHA256)
	}

	// A second export of the same version is refused instead of overwriting the first.
	if code, body := call(t, ts, "POST", "/api/videos/"+id+"/export", "", "{}"); code != http.StatusConflict {
		t.Fatalf("second export: %d %v, want 409", code, body)
	}
}

func TestExportRefusedAfterRejection(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("rejected-bytes"), "qa_passed")
	id := strconv.FormatInt(v.ID, 10)
	if code, body := call(t, ts, "POST", "/api/videos/"+id+"/decision", "", `{"decision":"rejected","notes":"no","sha256":"`+v.SHA256+`"}`); code != http.StatusCreated {
		t.Fatalf("reject: %d %v", code, body)
	}
	if code, body := call(t, ts, "POST", "/api/videos/"+id+"/export", "", "{}"); code != http.StatusConflict {
		t.Fatalf("export of a rejected video: %d %v, want 409", code, body)
	}
}

func TestArtifactContentServesTheExactStoredVideoBytes(t *testing.T) {
	ts, db, st := reviewServer(t)
	content := bytes.Repeat([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 0xff, 0x7f}, 50)
	v := seedVideo(t, db, st, content, "qa_passed")

	resp, err := http.Get(ts.URL + "/api/artifacts/" + strconv.FormatInt(v.ID, 10) + "/content")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("Content-Type %q, want video/mp4", ct)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("served %d bytes, stored %d; contents differ", len(got), len(content))
	}

	// Range requests (what a <video> element sends) return exactly the requested slice.
	req, _ := http.NewRequest("GET", ts.URL+"/api/artifacts/"+strconv.FormatInt(v.ID, 10)+"/content", nil)
	req.Header.Set("Range", "bytes=10-19")
	rr, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Body.Close()
	part, _ := io.ReadAll(rr.Body)
	if rr.StatusCode != http.StatusPartialContent || !bytes.Equal(part, content[10:20]) {
		t.Fatalf("range: %d, %d bytes", rr.StatusCode, len(part))
	}
}
