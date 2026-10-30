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
	// PlannedStages lists stage names that already have an implementation but are not wired into
	// Stages because the profile is not finished. It is data only: it never makes a profile
	// runnable, so Runnable stays false until Stages is filled in (M5 T5/T6).
	PlannedStages []string
	// Visual is the look and voice of a profile whose script is not a character drama (nil for
	// Fictional Series). It never makes a profile runnable.
	Visual *VisualSpec
}

// ReviewConfig holds review settings a profile may tune. Approval itself is never configurable:
// export is of the exact approved version only, for every profile.
type ReviewConfig struct {
	// MaxAutoRewrites is how many automatic script rewrites a failed run may start (informational
	// for now: the queue enforces jobs.MaxAutoRewrites).
	MaxAutoRewrites int
}

// Runnable reports whether the profile has stages to run. A profile registered as a spec only (no
// stages) is not runnable: no run may be created for it.
func (s Spec) Runnable() bool { return len(s.Stages["full"]) > 0 }

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

// KnowledgeShortsID is the id of the Knowledge Shorts profile (decisions D15 to D17).
const KnowledgeShortsID = "knowledge_shorts"

// knowledgeShorts is registered as a spec only (M5 T2): its length range is known, but it has no
// stages, so it is not runnable. Later M5 tasks (T3 to T6) add its research, script, visual and QA stages.
type knowledgeShorts struct{}

func (knowledgeShorts) Spec() Spec { return knowledgeShortsSpec }

// Stages implements the planned stages: the Knowledge Shorts script stage (T4) and the shared tts,
// images, subtitles and render stages (T5) and the shared qa stage (T6). The shared stages read the
// Shorts script through the adapter in loadScript, and qa adds the Shorts rules for a Shorts script,
// so none of them is forked.
func (knowledgeShorts) Stages(d *Deps) map[string]jobs.Stage {
	return map[string]jobs.Stage{
		"script":    &shortsScriptStage{d},
		"tts":       &ttsStage{d},
		"images":    &imagesStage{d},
		"subtitles": &subtitlesStage{d},
		"render":    &renderStage{d},
		"qa":        &qaStage{d},
	}
}

// PlannedStages is the ordered stage list; Stages stays empty so the profile is still refused with
// 501 until T7 wires the multi-profile queue and worker and moves this list into Stages["full"].
// Resources already say which stages hold the GPU.
var knowledgeShortsSpec = Spec{
	ID: KnowledgeShortsID, MinDurationS: 30, MaxDurationS: 60, QARules: "qa-rules-shorts-v1",
	PlannedStages: []string{"script", "tts", "images", "subtitles", "render", "qa"},
	Resources:     map[string]string{"script": "gpu", "tts": "gpu", "images": "gpu"},
	Visual: &VisualSpec{
		StylePrefix: "flat vector infographic illustration, simple shapes, clean flat colors, minimal icons and diagrams, " +
			"plain light background, no people, vertical framing",
		Negative: "photo, realistic, 3d render, blurry, text, letters, numbers, caption, watermark, logo, signature, " +
			"face, person, character, deformed",
		NarratorVoice:   "calm clear adult narrator, neutral Thai, even pace",
		SourcesHeading:  "แหล่งข้อมูล",
		MaxSourceTitles: 4,
	},
}

// Profiles is the registry of every content profile the app knows.
var Profiles = NewRegistry(fictionalSeries{}, knowledgeShorts{})

// FictionalSeriesSpec returns the Fictional Series profile data.
func FictionalSeriesSpec() Spec { return fictionalSeriesSpec }

// FictionalSeries is the queue plan derived from the Fictional Series profile.
var FictionalSeries = fictionalSeriesSpec.Plan()
