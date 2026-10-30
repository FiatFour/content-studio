package pipeline

import (
	"sort"

	"github.com/fiat/content-studio/internal/jobs"
)

// Spec is what a content profile defines, as plain data. It is not a workflow engine: the queue
// and the stage code stay shared, and a profile only says which stages run, what they hold, and
// which schema, duration range, QA rule set and review settings apply.
type Spec struct {
	ID string
	// Stages is the ordered stage list for each run kind ("full", "rerender").
	Stages map[string][]string
	// Resources maps a stage to the limited resource it holds while running (for example "gpu").
	Resources map[string]string
	// ScriptSchema names the JSON schema a script must satisfy (embedded in the pipeline package).
	ScriptSchema string
	// MinDurationS and MaxDurationS bound an episode's target length in seconds.
	MinDurationS, MaxDurationS int
	// QARules names the QA rule set (it matches the model label recorded on the QA report).
	QARules string
	Review  ReviewConfig
}

// ReviewConfig holds review settings a profile may tune. Approval itself is never configurable:
// export is of the exact approved version only, for every profile.
type ReviewConfig struct {
	// MaxAutoRewrites is how many automatic script rewrites a failed run may start (informational
	// for now: the queue enforces jobs.MaxAutoRewrites).
	MaxAutoRewrites int
}

// StagesFor returns the stage list for a run kind. "rerender" has its own list; every other kind uses "full".
func (s Spec) StagesFor(kind string) []string {
	if kind == "rerender" {
		return s.Stages["rerender"]
	}
	return s.Stages["full"]
}

// Plan derives the queue's plan from the profile.
func (s Spec) Plan() jobs.Plan {
	return jobs.Plan{Profile: s.ID, Full: s.Stages["full"], Rerender: s.Stages["rerender"], Resources: s.Resources}
}

// Profile is a content profile: its data plus the implementations of its stages.
type Profile interface {
	Spec() Spec
	// Stages wires every stage in Spec().Stages to its implementation.
	Stages(d *Deps) map[string]jobs.Stage
}

// Registry looks profiles up by id.
type Registry struct{ byID map[string]Profile }

func NewRegistry(ps ...Profile) *Registry {
	r := &Registry{byID: map[string]Profile{}}
	for _, p := range ps {
		id := p.Spec().ID
		if _, dup := r.byID[id]; dup {
			panic("pipeline: duplicate profile id " + id)
		}
		r.byID[id] = p
	}
	return r
}

// Get returns the profile with this id.
func (r *Registry) Get(id string) (Profile, bool) {
	p, ok := r.byID[id]
	return p, ok
}

// IDs lists the registered profile ids in sorted order.
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// FictionalSeriesID is the id stored in projects.profile and workflow_runs.profile.
const FictionalSeriesID = "fictional_series"

type fictionalSeries struct{}

func (fictionalSeries) Spec() Spec { return fictionalSeriesSpec }

func (fictionalSeries) Stages(d *Deps) map[string]jobs.Stage {
	return map[string]jobs.Stage{
		"script":    &scriptStage{d},
		"images":    &imagesStage{d},
		"tts":       &ttsStage{d},
		"subtitles": &subtitlesStage{d},
		"render":    &renderStage{d},
		"qa":        &qaStage{d},
	}
}

var fictionalSeriesSpec = Spec{
	ID: FictionalSeriesID,
	Stages: map[string][]string{
		"full":     {"script", "tts", "images", "subtitles", "render", "qa"},
		"rerender": {"render", "qa"},
	},
	// The LLM, image model and voice model each need the GPU; render and qa use CPU (ffmpeg).
	Resources:    map[string]string{"script": "gpu", "tts": "gpu", "images": "gpu"},
	ScriptSchema: "script.schema.json",
	MinDurationS: 60,
	MaxDurationS: 120,
	QARules:      "qa-rules-v1",
	Review:       ReviewConfig{MaxAutoRewrites: jobs.MaxAutoRewrites},
}

// Profiles is the registry of every content profile the app knows.
var Profiles = NewRegistry(fictionalSeries{})

// FictionalSeriesSpec returns the Fictional Series profile data.
func FictionalSeriesSpec() Spec { return fictionalSeriesSpec }

// FictionalSeries is the queue plan derived from the Fictional Series profile.
var FictionalSeries = fictionalSeriesSpec.Plan()
