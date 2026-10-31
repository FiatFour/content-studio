package pipeline_test

import (
	"reflect"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/pipeline"
)

func TestRegistryLookup(t *testing.T) {
	p, ok := pipeline.Profiles.Get("fictional_series")
	if !ok || p.Spec().ID != "fictional_series" {
		t.Fatalf("fictional_series not found: %v %v", p, ok)
	}
	if _, ok := pipeline.Profiles.Get("nope"); ok {
		t.Fatal("unknown id must not be found")
	}
	if got := pipeline.Profiles.IDs(); !reflect.DeepEqual(got, []string{"fictional_series", "history_mystery", "knowledge_shorts"}) {
		t.Fatalf("ids = %v", got)
	}
}

func TestKnowledgeShortsIsASpecOnly(t *testing.T) {
	p, ok := pipeline.Profiles.Get(pipeline.KnowledgeShortsID)
	if !ok {
		t.Fatal("knowledge_shorts not registered")
	}
	s := p.Spec()
	if s.MinDurationS != 30 || s.MaxDurationS != 60 {
		t.Fatalf("duration range %d-%d, want 30-60", s.MinDurationS, s.MaxDurationS)
	}
	// T7 edit: this asserted the profile was not runnable (no stages). It has six stages now.
	if !s.Runnable() || len(s.Stages["full"]) != 6 || len(s.Stages["rerender"]) != 2 {
		t.Fatal("knowledge_shorts must be runnable with its stages")
	}
	if !pipeline.FictionalSeriesSpec().Runnable() {
		t.Fatal("fictional_series must be runnable")
	}
}

func TestHistoryMysteryIsASpecOnly(t *testing.T) {
	p, ok := pipeline.Profiles.Get(pipeline.HistoryMysteryID)
	if !ok {
		t.Fatal("history_mystery not registered")
	}
	s := p.Spec()
	if s.MinDurationS != 60 || s.MaxDurationS != 120 {
		t.Fatalf("duration range %d-%d, want 60-120", s.MinDurationS, s.MaxDurationS)
	}
	if s.Runnable() || len(p.Stages(nil)) != 0 {
		t.Fatal("history_mystery must not be runnable")
	}
	if _, ok := pipeline.RunnablePlans()[pipeline.HistoryMysteryID]; ok {
		t.Fatal("history_mystery must have no runnable plan")
	}
	if _, ok := (&pipeline.Deps{}).RunnableStages()[pipeline.HistoryMysteryID]; ok {
		t.Fatal("history_mystery must have no runnable stages")
	}
}

// The values below are the ones the jobs.Plan literal held before the Profile abstraction.
func TestFictionalSeriesMatchesPreviousPlan(t *testing.T) {
	want := jobs.Plan{
		Profile:   "fictional_series",
		Full:      []string{"script", "tts", "images", "subtitles", "render", "qa"},
		Rerender:  []string{"render", "qa"},
		Resources: map[string]string{"script": "gpu", "tts": "gpu", "images": "gpu"},
	}
	if !reflect.DeepEqual(pipeline.FictionalSeries, want) {
		t.Fatalf("plan = %+v, want %+v", pipeline.FictionalSeries, want)
	}
	s := pipeline.FictionalSeriesSpec()
	if !reflect.DeepEqual(s.Plan(), want) {
		t.Fatalf("Spec.Plan() = %+v, want %+v", s.Plan(), want)
	}
	if s.MinDurationS != 60 || s.MaxDurationS != 120 {
		t.Fatalf("duration range %d-%d, want 60-120", s.MinDurationS, s.MaxDurationS)
	}
	if !reflect.DeepEqual(s.StagesFor("full"), want.Full) || !reflect.DeepEqual(s.StagesFor("rerender"), want.Rerender) {
		t.Fatal("StagesFor does not match the plan")
	}
}

func TestFictionalSeriesImplementsEveryStage(t *testing.T) {
	p, _ := pipeline.Profiles.Get(pipeline.FictionalSeriesID)
	impl := p.Stages(&pipeline.Deps{})
	for _, kind := range []string{"full", "rerender"} {
		for _, st := range p.Spec().StagesFor(kind) {
			if impl[st] == nil {
				t.Errorf("stage %q has no implementation", st)
			}
		}
	}
}
