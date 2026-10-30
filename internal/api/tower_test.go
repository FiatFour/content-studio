package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
	"github.com/fiat/content-studio/internal/store/storetest"
)

type towerFloor struct {
	Code           string               `json:"code"`
	Agent          string               `json:"agent"`
	State          string               `json:"state"`
	Episodes       []struct{ ID int64 } `json:"episodes"`
	TasksCompleted *int                 `json:"tasks_completed"`
	HandoffFrom    *string              `json:"handoff_from"`
	HandoffTo      *string              `json:"handoff_to"`
	Provider       *string              `json:"provider"`
	Model          *string              `json:"model"`
	LastLog        *string              `json:"last_log"`
}

type towerBody struct {
	Tower struct {
		Floors        []towerFloor `json:"floors"`
		ActiveEpisode *struct {
			Floor    string                          `json:"floor"`
			Pipeline []struct{ Stage, State string } `json:"pipeline"`
		} `json:"active_episode"`
		Today          []struct{ ID int64 } `json:"today"`
		AwaitingReview *struct {
			ArtifactID int64  `json:"artifact_id"`
			SHA256     string `json:"sha256"`
		} `json:"awaiting_review"`
		Continuity struct{ Available bool } `json:"continuity"`
	} `json:"tower"`
}

func fetchTower(t *testing.T, url string) (towerBody, string) {
	t.Helper()
	resp, err := http.Get(url + "/api/tower")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rawb, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, rawb)
	}
	var b towerBody
	if err := json.Unmarshal(rawb, &b); err != nil {
		t.Fatal(err)
	}
	return b, string(rawb)
}

func (b towerBody) floor(t *testing.T, code string) towerFloor {
	t.Helper()
	for _, f := range b.Tower.Floors {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no floor %s", code)
	return towerFloor{}
}

func TestTowerFollowsRealJobState(t *testing.T) {
	ts, db := serverDB(t)
	ctx := context.Background()

	// Empty database: everything idle, B1 not implemented, nothing invented.
	b, _ := fetchTower(t, ts.URL)
	if len(b.Tower.Floors) != 9 || b.floor(t, "B1").State != "not_implemented" || b.floor(t, "1F").State != "idle" ||
		b.Tower.ActiveEpisode != nil || b.Tower.AwaitingReview != nil || b.Tower.Continuity.Available {
		t.Fatalf("empty: %+v", b.Tower)
	}
	if tc := b.floor(t, "1F").TasksCompleted; tc == nil || *tc != 0 {
		t.Fatalf("tasks completed must be a real 0, got %v", tc)
	}

	_, ep := storetest.Episode(t, db)
	q := &jobs.Queue{DB: db, Plan: pipeline.FictionalSeries, MaxAttempts: 3}
	runID, err := q.CreateRun(ctx, ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	setJob := func(stage, state string, extra ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE jobs SET state = $1 WHERE run_id = $2 AND stage = $3`, state, runID, stage); err != nil {
			t.Fatal(err)
		}
	}

	// Queued only: the crate waits on 1F but nothing works there.
	b, _ = fetchTower(t, ts.URL)
	if f := b.floor(t, "1F"); f.State != "idle" || len(f.Episodes) != 1 {
		t.Fatalf("queued: %+v", f)
	}

	// script completed, tts running with a recorded provider and model.
	setJob("script", "completed")
	// Jobs are created one at a time as a run advances, so tts exists only once script completed.
	if _, err := db.Exec(ctx, `INSERT INTO jobs (run_id, stage, seq, state, provider, model, progress_done, progress_total, lease_owner, lease_expires_at)
			VALUES ($1, 'tts', 1, 'running', 'local-tts', 'voice-1', 1, 4, 'w', now() + interval '1 minute')`, runID); err != nil {
		t.Fatal(err)
	}
	b, _ = fetchTower(t, ts.URL)
	f := b.floor(t, "3F")
	if f.State != "active" || f.Provider == nil || *f.Provider != "local-tts" || *f.Model != "voice-1" || len(f.Episodes) != 1 ||
		f.HandoffFrom == nil || *f.HandoffFrom != "script" {
		t.Fatalf("running: %+v", f)
	}
	if b.floor(t, "1F").State != "done" || *b.floor(t, "1F").TasksCompleted != 1 || b.floor(t, "G").State != "active" {
		t.Fatalf("running upstream: %+v", b.Tower.Floors)
	}
	if ae := b.Tower.ActiveEpisode; ae == nil || ae.Floor != "3F" || len(ae.Pipeline) != 8 || ae.Pipeline[1].State != "running" {
		t.Fatalf("active episode: %+v", ae)
	}
	if len(b.Tower.Today) != 1 {
		t.Fatalf("today: %+v", b.Tower.Today)
	}

	// waiting_for_resource with a wait reason.
	if _, err := db.Exec(ctx, `UPDATE jobs SET state = 'waiting_for_resource', wait_reason = 'gpu is busy', lease_owner = NULL, lease_expires_at = NULL
		WHERE run_id = $1 AND stage = 'tts'`, runID); err != nil {
		t.Fatal(err)
	}
	b, _ = fetchTower(t, ts.URL)
	if f := b.floor(t, "3F"); f.State != "waiting" || f.LastLog == nil || *f.LastLog != "gpu is busy" {
		t.Fatalf("waiting: %+v", f)
	}

	// failed with an absolute path and a credential in the stored error text.
	secret := "sk-abcdef0123456789abcdef"
	if _, err := db.Exec(ctx, `UPDATE jobs SET state = 'failed', error_class = 'internal',
			error_message = $1 WHERE run_id = $2 AND stage = 'tts'`,
		"open /home/fiat/secret/dir/voice.wav: denied; Authorization: Bearer "+secret+" api_key="+secret, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE workflow_runs SET state = 'failed' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	b, raw := fetchTower(t, ts.URL)
	f = b.floor(t, "3F")
	if f.State != "failed" || f.LastLog == nil || !strings.Contains(*f.LastLog, "voice.wav") {
		t.Fatalf("failed: %+v", f)
	}
	for _, bad := range []string{"/home/", "fiat/secret", secret, "Bearer abc"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("response leaks %q: %s", bad, raw)
		}
	}
	if len(*f.LastLog) > 160 {
		t.Fatalf("log line not capped: %d", len(*f.LastLog))
	}
	// A failed run still shows its pipeline, with the failed stage marked and later stages pending.
	if ae := b.Tower.ActiveEpisode; ae == nil || ae.Floor != "3F" || len(ae.Pipeline) != 8 ||
		ae.Pipeline[0].State != "completed" || ae.Pipeline[1].State != "failed" || ae.Pipeline[2].State != "pending" {
		t.Fatalf("failed run pipeline: %+v", ae)
	}
}

func TestTowerShowsClipAwaitingReview(t *testing.T) {
	ts, db, st := reviewServer(t)
	v := seedVideo(t, db, st, []byte("fake-mp4-tower"), "qa_passed")
	b, _ := fetchTower(t, ts.URL)
	if a := b.Tower.AwaitingReview; a == nil || a.ArtifactID != v.ID || a.SHA256 != v.SHA256 {
		t.Fatalf("awaiting: %+v", b.Tower.AwaitingReview)
	}
	f := b.floor(t, "6F")
	if f.State != "waiting" || len(f.Episodes) != 1 {
		t.Fatalf("6F: %+v", f)
	}

	// Once decided, the clip leaves the review floor.
	if _, err := db.Exec(context.Background(), `INSERT INTO approvals (artifact_id, sha256, decision, reviewer) VALUES ($1, $2, 'rejected', 'owner')`, v.ID, v.SHA256); err != nil {
		t.Fatal(err)
	}
	b, _ = fetchTower(t, ts.URL)
	if b.Tower.AwaitingReview != nil || b.floor(t, "6F").State != "idle" || *b.floor(t, "6F").TasksCompleted != 1 {
		t.Fatalf("after decision: %+v", b.Tower)
	}
}
