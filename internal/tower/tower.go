// Package tower derives the agent-tower view (docs/spec/ui.md, "Agent tower") from rows the jobs
// tables already hold. Everything here is pure: the API loads rows, Derive maps them to floors, and
// nothing is invented. A floor with no real backend says not_implemented; a value that is not
// recorded is nil. The mapping from job state to floor state is documented in docs/spec/ui.md.
package tower

import (
	"sort"
	"time"

	"github.com/fiat/content-studio/internal/jobs"
)

// FloorState is what a floor shows. It is derived only from job and run state.
type FloorState string

const (
	Idle           FloorState = "idle"
	Active         FloorState = "active"
	Done           FloorState = "done"
	Failed         FloorState = "failed"
	Waiting        FloorState = "waiting"
	NotImplemented FloorState = "not_implemented"
)

// Agent ids, in handoff vocabulary.
const (
	AgentGuardrails   = "guardrails"
	AgentOrchestrator = "orchestrator"
	AgentScript       = "script"
	AgentArt          = "art"
	AgentVoice        = "voice"
	AgentRender       = "render"
	AgentQA           = "qa"
	AgentReview       = "review"
	AgentExport       = "export"
)

// FloorDef is one floor of the building. Stages are the job stages worked on that floor.
type FloorDef struct {
	Code   string
	Agent  string
	Stages []string
}

// Floors lists the building bottom to top, as the spec draws it.
var Floors = []FloorDef{
	{Code: "B1", Agent: AgentGuardrails},
	{Code: "G", Agent: AgentOrchestrator},
	{Code: "1F", Agent: AgentScript, Stages: []string{"script"}},
	{Code: "2F", Agent: AgentArt, Stages: []string{"images"}},
	{Code: "3F", Agent: AgentVoice, Stages: []string{"tts", "subtitles"}},
	{Code: "4F", Agent: AgentRender, Stages: []string{"render"}},
	{Code: "5F", Agent: AgentQA, Stages: []string{"qa"}},
	{Code: "6F", Agent: AgentReview},
	{Code: "Roof", Agent: AgentExport},
}

// StageAgent maps a pipeline stage to the agent that does it.
var StageAgent = map[string]string{
	"script": AgentScript, "images": AgentArt, "tts": AgentVoice, "subtitles": AgentVoice,
	"render": AgentRender, "qa": AgentQA,
}

func floorOfStage(stage string) string {
	for _, f := range Floors {
		for _, s := range f.Stages {
			if s == stage {
				return f.Code
			}
		}
	}
	return ""
}

// FloorStateOf maps the states of one floor's jobs (across live runs) to the floor's state.
// total is how many jobs the floor has across those runs; the floor is done only when every one of
// them completed. Priority: failed, waiting, active, done, idle. See the table in docs/spec/ui.md.
func FloorStateOf(states []jobs.State, total int) FloorState {
	var failed, waiting, active, completed int
	for _, s := range states {
		switch s {
		case jobs.Failed:
			failed++
		case jobs.WaitingForResource, jobs.WaitingForHuman, jobs.RetryScheduled:
			waiting++
		case jobs.Running:
			active++
		case jobs.Completed:
			completed++
		}
	}
	switch {
	case failed > 0:
		return Failed
	case waiting > 0:
		return Waiting
	case active > 0:
		return Active
	case total > 0 && completed == total:
		return Done
	}
	return Idle
}

// Job is one stage job of a run. LastLog is already sanitized by the caller.
type Job struct {
	Stage         string
	Seq           int
	State         jobs.State
	ProgressDone  int
	ProgressTotal int
	Provider      string // "" when not recorded
	Model         string
	LastLog       string // sanitized error message or wait reason; "" when none
	UpdatedAt     time.Time
}

// Run is the latest run of one episode.
type Run struct {
	ID        int64
	EpisodeID int64
	Number    int
	Title     string
	State     jobs.RunState
	CreatedAt time.Time
	// Stages is the run's plan, in order. Jobs are created one at a time as the run advances, so a
	// stage not reached yet has no job. Empty means "the stages of the jobs that exist".
	Stages []string
	Jobs   []Job // any order
}

func (r Run) plan() []string {
	if len(r.Stages) > 0 {
		return r.Stages
	}
	var out []string
	for _, j := range sorted(r.Jobs) {
		out = append(out, j.Stage)
	}
	return out
}

func (r Run) job(stage string) *Job {
	for i := range r.Jobs {
		if r.Jobs[i].Stage == stage {
			return &r.Jobs[i]
		}
	}
	return nil
}

// Clip is a QA-passed video version nobody has decided on yet.
type Clip struct {
	ArtifactID int64
	EpisodeID  int64
	Number     int
	Title      string
	Version    int
	SHA256     string
	CreatedAt  time.Time
}

// Input is everything Derive needs.
type Input struct {
	Runs          []Run // latest run per episode
	Awaiting      []Clip
	Completed     map[string]int // completed jobs per stage, all time
	Approvals     int            // decisions recorded
	Exports       int
	ExportedToday bool
	TodayStart    time.Time
}

type EpisodeRef struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
}

type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

type Floor struct {
	Code           string       `json:"code"`
	Agent          string       `json:"agent"`
	State          FloorState   `json:"state"`
	Episodes       []EpisodeRef `json:"episodes"`
	TasksCompleted *int         `json:"tasks_completed"` // nil: not recorded for this agent
	HandoffFrom    *string      `json:"handoff_from"`
	HandoffTo      *string      `json:"handoff_to"`
	Provider       *string      `json:"provider"`
	Model          *string      `json:"model"`
	LastLog        *string      `json:"last_log"`
	Progress       *Progress    `json:"progress"` // the shown job's own progress, when it has one
	Note           string       `json:"note,omitempty"`
}

type StageStatus struct {
	Stage    string    `json:"stage"`
	Agent    string    `json:"agent"`
	Floor    string    `json:"floor"`
	State    string    `json:"state"` // a job state, or "pending" when there is no job
	Progress *Progress `json:"progress"`
}

type EpisodeStatus struct {
	EpisodeRef
	RunID    int64         `json:"run_id"`
	RunState string        `json:"run_state"`
	Floor    *string       `json:"floor"`   // where its crate stands; nil when nowhere
	Stages   Progress      `json:"stages"`  // completed stage jobs of the run
	Current  *Progress     `json:"current"` // the working job's own progress
	Pipeline []StageStatus `json:"pipeline,omitempty"`
}

type ClipStatus struct {
	ArtifactID int64 `json:"artifact_id"`
	EpisodeRef
	Version   int       `json:"version"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

type Continuity struct {
	Available bool   `json:"available"`
	Note      string `json:"note"`
}

type Tower struct {
	Floors         []Floor         `json:"floors"`
	ActiveEpisode  *EpisodeStatus  `json:"active_episode"`
	Today          []EpisodeStatus `json:"today"`
	AwaitingReview *ClipStatus     `json:"awaiting_review"`
	Continuity     Continuity      `json:"continuity"`
}

func ptr[T any](v T) *T { return &v }

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// live reports whether a run belongs on the floors: it is running, failed (needs the owner), or
// finished with a clip nobody has decided on. Cancelled runs and decided clips drop out.
func live(r Run, awaiting map[int64]bool) bool {
	switch r.State {
	case jobs.RunRunning, jobs.RunFailed:
		return true
	case jobs.RunReadyForReview:
		return awaiting[r.EpisodeID]
	}
	return false
}

func sorted(js []Job) []Job {
	out := append([]Job(nil), js...)
	sort.Slice(out, func(i, k int) bool { return out[i].Seq < out[k].Seq })
	return out
}

// rank orders jobs by how much a floor should show them: the highest wins.
func rank(s jobs.State) int {
	switch s {
	case jobs.Failed:
		return 6
	case jobs.WaitingForResource, jobs.WaitingForHuman, jobs.RetryScheduled:
		return 5
	case jobs.Running:
		return 4
	case jobs.Completed:
		return 3
	case jobs.Queued:
		return 2
	}
	return 1
}

// firstOpen is the first job (by seq) that has not completed: where the run's crate waits.
func firstOpen(js []Job) *Job {
	for i := range js {
		if js[i].State != jobs.Completed {
			return &js[i]
		}
	}
	return nil
}

func progressOf(j Job) *Progress {
	if j.ProgressTotal <= 0 {
		return nil
	}
	return &Progress{j.ProgressDone, j.ProgressTotal}
}

// Derive builds the tower from the loaded rows.
func Derive(in Input) Tower {
	awaiting := map[int64]bool{}
	for _, c := range in.Awaiting {
		awaiting[c.EpisodeID] = true
	}
	var liveRuns []Run
	for _, r := range in.Runs {
		if live(r, awaiting) {
			r.Jobs = sorted(r.Jobs)
			liveRuns = append(liveRuns, r)
		}
	}
	sort.Slice(liveRuns, func(i, k int) bool { return liveRuns[i].EpisodeID < liveRuns[k].EpisodeID })

	t := Tower{Continuity: Continuity{Available: false,
		Note: "The canon checks run inside the script stage and keep no per-run record, so there is nothing to show."}}

	for _, def := range Floors {
		f := Floor{Code: def.Code, Agent: def.Agent, Episodes: []EpisodeRef{}}
		switch def.Code {
		case "B1":
			f.State = NotImplemented
			f.Note = "Cost guard and vault have no backend: no cloud provider is configured (D10)."
		case "G":
			f.State = Idle
			for _, r := range liveRuns {
				if r.State == jobs.RunRunning {
					f.State = Active
				}
			}
			f.Note = "Derived: active while any run is running. The orchestrator has no job of its own."
		case "6F":
			f.State = Idle
			for _, c := range in.Awaiting {
				f.State = Waiting
				f.Episodes = append(f.Episodes, EpisodeRef{c.EpisodeID, c.Number, c.Title})
			}
			if f.State == Waiting {
				f.HandoffFrom, f.HandoffTo = ptr(AgentQA), ptr(AgentExport)
			}
			f.TasksCompleted = ptr(in.Approvals)
		case "Roof":
			f.State = Idle
			if in.ExportedToday {
				f.State = Done
			}
			f.TasksCompleted = ptr(in.Exports)
		default:
			deriveWorkFloor(&f, def, liveRuns, in.Completed)
		}
		t.Floors = append(t.Floors, f)
	}

	// Today's episodes: every live run plus any run touched since midnight.
	today := []EpisodeStatus{}
	var active *EpisodeStatus
	var activeAt time.Time
	for _, r := range in.Runs {
		isLive := live(r, awaiting)
		touched := !r.CreatedAt.Before(in.TodayStart)
		for _, j := range r.Jobs {
			if !j.UpdatedAt.Before(in.TodayStart) {
				touched = true
			}
		}
		if !isLive && !touched {
			continue
		}
		r.Jobs = sorted(r.Jobs)
		es := episodeStatus(r, isLive, awaiting[r.EpisodeID])
		today = append(today, es)
		if r.State != jobs.RunRunning {
			continue
		}
		for _, j := range r.Jobs {
			if j.State == jobs.Running && (active == nil || j.UpdatedAt.After(activeAt)) {
				e := es
				e.Pipeline = pipeline(r, awaiting[r.EpisodeID])
				active, activeAt = &e, j.UpdatedAt
			}
		}
	}
	sort.Slice(today, func(i, k int) bool { return today[i].ID < today[k].ID })
	t.Today, t.ActiveEpisode = today, active

	if len(in.Awaiting) > 0 {
		c := in.Awaiting[0]
		for _, o := range in.Awaiting[1:] {
			if o.CreatedAt.After(c.CreatedAt) {
				c = o
			}
		}
		t.AwaitingReview = &ClipStatus{ArtifactID: c.ArtifactID, EpisodeRef: EpisodeRef{c.EpisodeID, c.Number, c.Title},
			Version: c.Version, SHA256: c.SHA256, CreatedAt: c.CreatedAt}
	}
	return t
}

func deriveWorkFloor(f *Floor, def FloorDef, liveRuns []Run, completed map[string]int) {
	var states []jobs.State
	var best *Job
	var bestRun *Run
	total, n := 0, 0
	for _, s := range def.Stages {
		n += completed[s]
	}
	f.TasksCompleted = ptr(n)
	for i := range liveRuns {
		r := &liveRuns[i]
		for _, s := range r.plan() {
			if floorOfStage(s) != def.Code {
				continue
			}
			total++ // a planned stage with no job yet counts: the floor is not done without it
			if j := r.job(s); j != nil {
				states = append(states, j.State)
				if best == nil || rank(j.State) > rank(best.State) ||
					(rank(j.State) == rank(best.State) && j.UpdatedAt.After(best.UpdatedAt)) {
					best, bestRun = j, r
				}
			}
		}
		// The crate stands on the floor of the first stage that has not completed.
		if o := firstOpen(r.Jobs); o != nil && floorOfStage(o.Stage) == def.Code {
			f.Episodes = append(f.Episodes, EpisodeRef{r.EpisodeID, r.Number, r.Title})
		}
	}
	f.State = FloorStateOf(states, total)
	if best == nil {
		return
	}
	f.Provider, f.Model = nonEmpty(best.Provider), nonEmpty(best.Model)
	f.LastLog = nonEmpty(best.LastLog)
	f.Progress = progressOf(*best)
	// Handoff comes from the run's real job order: the job before and the job after the one shown.
	from, to := AgentOrchestrator, AgentReview
	plan := bestRun.plan()
	for k, s := range plan {
		if s == best.Stage {
			if k > 0 {
				from = StageAgent[plan[k-1]]
			}
			if k+1 < len(plan) {
				to = StageAgent[plan[k+1]]
			}
		}
	}
	f.HandoffFrom = ptr(from)
	if best.State == jobs.Completed {
		f.HandoffTo = ptr(to)
	}
}

func episodeStatus(r Run, isLive, awaiting bool) EpisodeStatus {
	es := EpisodeStatus{EpisodeRef: EpisodeRef{r.EpisodeID, r.Number, r.Title}, RunID: r.ID, RunState: string(r.State)}
	es.Stages.Total = len(r.plan())
	for _, j := range r.Jobs {
		if j.State == jobs.Completed {
			es.Stages.Done++
		}
		if j.State == jobs.Running {
			es.Current = progressOf(j)
		}
	}
	if isLive {
		if o := firstOpen(r.Jobs); o != nil {
			if fl := floorOfStage(o.Stage); fl != "" {
				es.Floor = &fl
			}
		} else if awaiting {
			es.Floor = ptr("6F")
		}
	}
	return es
}

func pipeline(r Run, awaiting bool) []StageStatus {
	var out []StageStatus
	for _, s := range r.plan() {
		st := StageStatus{Stage: s, Agent: StageAgent[s], Floor: floorOfStage(s), State: "pending"}
		if j := r.job(s); j != nil {
			st.State, st.Progress = string(j.State), progressOf(*j)
		}
		out = append(out, st)
	}
	review := "pending"
	if awaiting {
		review = string(jobs.WaitingForHuman)
	}
	return append(out,
		StageStatus{Stage: "review", Agent: AgentReview, Floor: "6F", State: review},
		StageStatus{Stage: "export", Agent: AgentExport, Floor: "Roof", State: "pending"})
}
