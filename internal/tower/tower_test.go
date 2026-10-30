package tower

import (
	"testing"
	"time"

	"github.com/fiat/content-studio/internal/jobs"
)

func TestFloorStateOf(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []jobs.State
		total  int
		want   FloorState
	}{
		{"no jobs", nil, 0, Idle},
		{"queued only", []jobs.State{jobs.Queued}, 1, Idle},
		{"running", []jobs.State{jobs.Running}, 1, Active},
		{"completed", []jobs.State{jobs.Completed}, 1, Done},
		{"failed", []jobs.State{jobs.Failed}, 1, Failed},
		{"waiting for resource", []jobs.State{jobs.WaitingForResource}, 1, Waiting},
		{"waiting for human", []jobs.State{jobs.WaitingForHuman}, 1, Waiting},
		{"retry scheduled", []jobs.State{jobs.RetryScheduled}, 1, Waiting},
		{"cancelled is not work", []jobs.State{jobs.Cancelled}, 1, Idle},
		{"two stages one done one queued", []jobs.State{jobs.Completed, jobs.Queued}, 2, Idle},
		{"two stages both done", []jobs.State{jobs.Completed, jobs.Completed}, 2, Done},
		{"two stages done and running", []jobs.State{jobs.Completed, jobs.Running}, 2, Active},
		{"failed beats waiting", []jobs.State{jobs.WaitingForHuman, jobs.Failed}, 2, Failed},
		{"waiting beats running", []jobs.State{jobs.Running, jobs.WaitingForResource}, 2, Waiting},
	} {
		if got := FloorStateOf(tc.states, tc.total); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func run(id int64, state jobs.RunState, js ...Job) Run {
	return Run{ID: id, EpisodeID: id, Number: int(id), Title: "ep", State: state, CreatedAt: t0, Jobs: js}
}

func floor(t *testing.T, tw Tower, code string) Floor {
	t.Helper()
	for _, f := range tw.Floors {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no floor %s", code)
	return Floor{}
}

var plan = []string{"script", "tts", "images", "subtitles", "render", "qa"}

func jobsAt(states ...jobs.State) []Job {
	var out []Job
	for i, s := range states {
		out = append(out, Job{Stage: plan[i], Seq: i, State: s, UpdatedAt: t0})
	}
	return out
}

func TestDeriveEmptyIsIdleAndHonest(t *testing.T) {
	tw := Derive(Input{TodayStart: t0.Truncate(24 * time.Hour)})
	for i, code := range []string{"B1", "G", "1F", "2F", "3F", "4F", "5F", "6F", "Roof"} {
		if tw.Floors[i].Code != code {
			t.Fatalf("floor %d is %s", i, tw.Floors[i].Code)
		}
	}
	if s := floor(t, tw, "B1").State; s != NotImplemented {
		t.Fatalf("B1 is %s", s)
	}
	if tw.Continuity.Available {
		t.Fatal("continuity must say no data")
	}
	for _, c := range []string{"G", "1F", "2F", "3F", "4F", "5F", "6F", "Roof"} {
		if s := floor(t, tw, c).State; s != Idle {
			t.Errorf("%s is %s", c, s)
		}
	}
	if tw.ActiveEpisode != nil || tw.AwaitingReview != nil || len(tw.Today) != 0 {
		t.Fatalf("%+v", tw)
	}
}

func TestDeriveRunningMidPipeline(t *testing.T) {
	js := jobsAt(jobs.Completed, jobs.Running, jobs.Queued, jobs.Queued, jobs.Queued, jobs.Queued)
	js[1].Provider, js[1].Model = "local-tts", "voice-1"
	js[1].ProgressDone, js[1].ProgressTotal = 2, 5
	tw := Derive(Input{Runs: []Run{run(1, jobs.RunRunning, js...)}, Completed: map[string]int{"script": 4, "tts": 3}, TodayStart: t0.Add(-time.Hour)})
	if f := floor(t, tw, "1F"); f.State != Done || *f.TasksCompleted != 4 || *f.HandoffTo != AgentVoice || len(f.Episodes) != 0 {
		t.Errorf("1F %+v", f)
	}
	f3 := floor(t, tw, "3F")
	if f3.State != Active || len(f3.Episodes) != 1 || *f3.Provider != "local-tts" || *f3.Model != "voice-1" ||
		*f3.HandoffFrom != AgentScript || f3.HandoffTo != nil || f3.Progress.Done != 2 || *f3.TasksCompleted != 3 {
		t.Errorf("3F %+v", f3)
	}
	if floor(t, tw, "G").State != Active || floor(t, tw, "2F").State != Idle {
		t.Error("G active, 2F idle expected")
	}
	ae := tw.ActiveEpisode
	if ae == nil || *ae.Floor != "3F" || ae.Stages.Done != 1 || ae.Stages.Total != 6 || len(ae.Pipeline) != 8 || ae.Current.Total != 5 {
		t.Fatalf("%+v", ae)
	}
	if last := ae.Pipeline[7]; last.Stage != "export" {
		t.Errorf("last stage is %s, never publish", last.Stage)
	}
}

func TestDeriveFailedWaitingAndReview(t *testing.T) {
	d := jobs.Completed
	failedJobs := jobsAt(d, d, jobs.Failed, jobs.Queued, jobs.Queued, jobs.Queued)
	failedJobs[2].LastLog = "boom"
	failed := run(1, jobs.RunFailed, failedJobs...)
	waiting := run(2, jobs.RunRunning, jobsAt(d, d, jobs.WaitingForResource, jobs.Queued, jobs.Queued, jobs.Queued)...)
	ready := run(3, jobs.RunReadyForReview, jobsAt(d, d, d, d, d, d)...)
	decided := run(4, jobs.RunReadyForReview, jobsAt(d, d, d, d, d, d)...)
	cancelled := run(5, jobs.RunCancelled, jobsAt(d, jobs.Cancelled)...)
	clip := Clip{ArtifactID: 9, EpisodeID: 3, Number: 3, Title: "ep", Version: 1, SHA256: "abc", CreatedAt: t0}
	tw := Derive(Input{Runs: []Run{failed, waiting, ready, decided, cancelled}, Awaiting: []Clip{clip}, Approvals: 2, TodayStart: t0.Add(-time.Hour)})

	// tts (seq 1) is before images in the real order, so images (2F) holds both the failed and the waiting job.
	if f := floor(t, tw, "2F"); f.State != Failed || *f.LastLog != "boom" || len(f.Episodes) != 2 {
		t.Errorf("2F %+v", f)
	}
	if f := floor(t, tw, "1F"); f.State != Done {
		t.Errorf("1F %s", f.State)
	}
	if f := floor(t, tw, "6F"); f.State != Waiting || len(f.Episodes) != 1 || f.Episodes[0].ID != 3 || *f.TasksCompleted != 2 {
		t.Errorf("6F %+v", f)
	}
	if tw.AwaitingReview == nil || tw.AwaitingReview.ArtifactID != 9 || tw.AwaitingReview.SHA256 != "abc" {
		t.Errorf("%+v", tw.AwaitingReview)
	}
	// No job is running, so the pipeline panel falls back to the failed run (episode 1, first on a tie);
	// the ready-for-review clip is not an active episode.
	if ae := tw.ActiveEpisode; ae == nil || ae.ID != 1 || ae.Pipeline[2].State != "failed" {
		t.Errorf("active episode %+v", ae)
	}
	// Cancelled and decided runs are not live but were touched today: listed, with no floor.
	got := map[int64]*string{}
	for _, e := range tw.Today {
		got[e.ID] = e.Floor
	}
	if len(got) != 5 || got[4] != nil || got[5] != nil || *got[3] != "6F" || *got[1] != "2F" {
		t.Errorf("today floors %v", got)
	}
}

func TestUnreachedStagesKeepAFloorFromBeingDone(t *testing.T) {
	// Jobs are created as the run advances: images is running, subtitles has no job yet.
	r := run(1, jobs.RunRunning, jobsAt(jobs.Completed, jobs.Completed, jobs.Running)...)
	r.Stages = plan
	tw := Derive(Input{Runs: []Run{r}, TodayStart: t0.Add(-time.Hour)})
	if s := floor(t, tw, "3F").State; s != Idle {
		t.Errorf("3F with subtitles unreached is %s, want idle", s)
	}
	if ae := tw.ActiveEpisode; ae == nil || ae.Stages.Total != 6 || len(ae.Pipeline) != 8 || ae.Pipeline[3].State != "pending" {
		t.Errorf("%+v", ae)
	}
	if f := floor(t, tw, "2F"); f.State != Active || *f.HandoffFrom != AgentVoice {
		t.Errorf("2F %+v", f)
	}
}

func TestOldRunsDropOut(t *testing.T) {
	old := run(1, jobs.RunCancelled, jobsAt(jobs.Completed)...)
	old.CreatedAt = t0.Add(-72 * time.Hour)
	old.Jobs[0].UpdatedAt = old.CreatedAt
	tw := Derive(Input{Runs: []Run{old}, TodayStart: t0.Truncate(24 * time.Hour)})
	if len(tw.Today) != 0 {
		t.Fatalf("old run listed: %+v", tw.Today)
	}
}

func TestRoofDoneOnlyAfterRealExport(t *testing.T) {
	if s := floor(t, Derive(Input{}), "Roof").State; s != Idle {
		t.Fatal(s)
	}
	if f := floor(t, Derive(Input{ExportedToday: true, Exports: 3}), "Roof"); f.State != Done || *f.TasksCompleted != 3 {
		t.Fatalf("%+v", f)
	}
}
