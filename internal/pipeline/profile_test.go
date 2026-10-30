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
	if got := pipeline.Profiles.IDs(); !reflect.DeepEqual(got, []string{"fictional_series", "knowledge_shorts"}) {
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
	if s.Runnable() || len(p.Stages(&pipeline.Deps{})) != 0 {
		t.Fatal("knowledge_shorts must not be runnable until its stages exist")
	}
	if !pipeline.FictionalSeriesSpec().Runnable() {
		t.Fatal("fictional_series must be runnable")
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
