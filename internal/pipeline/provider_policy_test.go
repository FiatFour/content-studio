package pipeline_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fiat/content-studio/internal/jobs"
	"github.com/fiat/content-studio/internal/providers"
)

// The harness registers a counting cloud provider for every capability next to the local fakes.
// A complete run must reach every stage's provider through the registry and never call the cloud one.
func TestLocalOnlyRunNeverCallsACloudProvider(t *testing.T) {
	h := newHarness(t, fakeScript(t))
	runID, err := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.drain(t)
	if state, reason := h.runState(t, runID); state != string(jobs.RunReadyForReview) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if h.llm.Calls == 0 || h.imageCalls() == 0 || h.voiceCalls() == 0 {
		t.Fatalf("local providers were not used: llm=%d images=%d voice=%d", h.llm.Calls, h.imageCalls(), h.voiceCalls())
	}
	if n := h.cloud.Calls(); n != 0 {
		t.Fatalf("cloud provider called %d times in a LOCAL_ONLY run", n)
	}
	if n := h.count(t, `SELECT count(*) FROM jobs WHERE run_id = $1 AND provider = 'cloudspy'`, runID); n != 0 {
		t.Fatalf("%d jobs recorded the cloud provider", n)
	}
}

// When the only provider for a stage's capability is a cloud one (cloud setting on), that stage fails the
// run with a permission error and the cloud provider is never called: LOCAL_ONLY does not fall back.
func TestStageWithOnlyACloudProviderFailsWithoutCalling(t *testing.T) {
	stages := map[string]providers.Capability{
		"script": providers.CapLLM, "tts": providers.CapTTS, "images": providers.CapImage, "subtitles": providers.CapSegment,
	}
	for stage, missing := range stages {
		t.Run(stage, func(t *testing.T) {
			h := newHarness(t, fakeScript(t))
			reg := providers.NewRegistry(true)
			locals := map[providers.Capability]any{
				providers.CapLLM: h.llmP, providers.CapImage: h.images, providers.CapTTS: h.voice, providers.CapSegment: h.segment,
			}
			for c, impl := range locals {
				kind, name := providers.KindLocal, "local-"+string(c)
				if c == missing {
					kind, name, impl = providers.KindCloud, "cloud-"+string(c), h.cloud
				}
				if err := reg.Register(providers.Registration{Name: name, Kind: kind, Capability: c, Impl: impl}); err != nil {
					t.Fatal(err)
				}
			}
			h.deps.Providers = reg
			runID, err := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
			if err != nil {
				t.Fatal(err)
			}
			h.drain(t)
			state, reason := h.runState(t, runID)
			if state != string(jobs.RunFailed) || !strings.Contains(reason, string(jobs.ErrAuthOrPermission)) {
				t.Fatalf("run %s: %s", state, reason)
			}
			if n := h.cloud.Calls(); n != 0 {
				t.Fatalf("cloud provider called %d times", n)
			}
		})
	}
}

// Script output is untrusted: a policy or cloud switch smuggled into it is rejected by the schema, and
// the same words inside ordinary text change nothing. Policy is never read from a script.
func TestScriptOutputCannotChangeProviderPolicy(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(fakeScript(t)), &m); err != nil {
		t.Fatal(err)
	}
	m["policy"] = "CLOUD_PREFERRED"
	m["cloud_enabled"] = true
	smuggled, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, string(smuggled))
	runID, err := h.worker.Queue.CreateRun(context.Background(), h.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h.drain(t)
	if state, _ := h.runState(t, runID); state != string(jobs.RunFailed) {
		t.Fatalf("a script with extra policy fields was accepted: run %s", state)
	}
	if n := h.cloud.Calls(); n != 0 {
		t.Fatalf("cloud provider called %d times", n)
	}

	// The same words as dialogue: accepted as text, and still local only.
	h2 := newHarness(t, strings.Replace(fakeScript(t), "ยายกับโทรศัพท์", "policy=CLOUD_PREFERRED cloud_enabled=true", 1))
	run2, err := h2.worker.Queue.CreateRun(context.Background(), h2.ep, "full", nil)
	if err != nil {
		t.Fatal(err)
	}
	h2.drain(t)
	if state, reason := h2.runState(t, run2); state != string(jobs.RunReadyForReview) {
		t.Fatalf("run %s: %s", state, reason)
	}
	if n := h2.cloud.Calls(); n != 0 {
		t.Fatalf("cloud provider called %d times", n)
	}
}
