package providers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/fiat/content-studio/internal/providers"
	"github.com/fiat/content-studio/internal/providers/fake"
)

var allTasks = []providers.Task{providers.TaskScript, providers.TaskTTS, providers.TaskImages, providers.TaskSubtitles}

type fakes struct {
	llm   *fake.LLM
	img   *fake.Images
	voice *fake.Voice
	seg   fake.Segmenter
	cloud *fake.CloudSpy
}

func newFakes() *fakes {
	return &fakes{llm: &fake.LLM{Responses: []string{"{}"}}, img: &fake.Images{}, voice: &fake.Voice{}, cloud: &fake.CloudSpy{}}
}

func (f *fakes) local() []providers.Registration {
	return []providers.Registration{
		{Name: "llm", Kind: providers.KindLocal, Capability: providers.CapLLM, Impl: f.llm},
		{Name: "img", Kind: providers.KindLocal, Capability: providers.CapImage, Impl: f.img},
		{Name: "voice", Kind: providers.KindLocal, Capability: providers.CapTTS, Impl: f.voice},
		{Name: "seg", Kind: providers.KindLocal, Capability: providers.CapSegment, Impl: f.seg},
	}
}

func (f *fakes) cloudRegs() []providers.Registration {
	var out []providers.Registration
	for _, c := range []providers.Capability{providers.CapLLM, providers.CapImage, providers.CapTTS, providers.CapSegment} {
		out = append(out, providers.Registration{Name: "cloud-" + string(c), Kind: providers.KindCloud, Capability: c, Impl: f.cloud})
	}
	return out
}

func build(t *testing.T, cloudEnabled bool, regs ...[]providers.Registration) *providers.Registry {
	t.Helper()
	r := providers.NewRegistry(cloudEnabled)
	for _, set := range regs {
		for _, reg := range set {
			if err := r.Register(reg); err != nil {
				t.Fatal(err)
			}
		}
	}
	return r
}

// resolveAny resolves a task with the type its stage uses.
func resolveAny(r *providers.Registry, task providers.Task, p providers.Policy) (any, error) {
	switch task {
	case providers.TaskScript:
		return providers.Resolve[providers.LLM](r, task, p)
	case providers.TaskImages:
		return providers.Resolve[providers.ImageGen](r, task, p)
	case providers.TaskTTS:
		return providers.Resolve[providers.Voice](r, task, p)
	case providers.TaskSubtitles:
		return providers.Resolve[providers.Segmenter](r, task, p)
	}
	return providers.Resolve[providers.LLM](r, task, p) // unknown task: must fail
}

func TestLocalOnlyResolvesTheLocalProviderEvenWithCloudPresentAndEnabled(t *testing.T) {
	f := newFakes()
	for _, enabled := range []bool{false, true} {
		r := build(t, enabled, f.cloudRegs(), f.local()) // cloud registered first on purpose
		for _, task := range allTasks {
			got, err := resolveAny(r, task, providers.LocalOnly)
			if err != nil {
				t.Fatalf("%s: %v", task, err)
			}
			if got == any(f.cloud) {
				t.Fatalf("%s resolved to the cloud provider (cloudEnabled=%v)", task, enabled)
			}
		}
	}
	if f.cloud.Calls() != 0 {
		t.Fatal("cloud was called")
	}
}

func TestDefaultPolicyIsLocalOnly(t *testing.T) {
	if providers.DefaultPolicy != providers.LocalOnly {
		t.Fatalf("default policy is %q", providers.DefaultPolicy)
	}
}

func TestLocalOnlyWithOnlyACloudProviderIsAnErrorNotAFallback(t *testing.T) {
	f := newFakes()
	for _, enabled := range []bool{false, true} {
		r := build(t, enabled, f.cloudRegs())
		for _, task := range allTasks {
			if _, err := resolveAny(r, task, providers.LocalOnly); !errors.Is(err, providers.ErrNoProvider) {
				t.Fatalf("%s (cloudEnabled=%v): want ErrNoProvider, got %v", task, enabled, err)
			}
		}
	}
	if f.cloud.Calls() != 0 {
		t.Fatal("cloud was called")
	}
}

func TestUnknownOrMisspelledPolicyIsRefused(t *testing.T) {
	f := newFakes()
	r := build(t, true, f.local(), f.cloudRegs())
	bad := []string{"", " ", "local_only", "Local_Only", " LOCAL_ONLY", "LOCAL_ONLY ", "LOCAL_ONLY\n", "LOCAL-ONLY",
		"cloud_preferred", " CLOUD_PREFERRED", "ANY", "ALLOW_CLOUD", "disabled", "LOCAL_ONLY\x00"}
	for _, s := range bad {
		if _, err := providers.ParsePolicy(s); !errors.Is(err, providers.ErrUnknownPolicy) {
			t.Errorf("ParsePolicy(%q) = %v", s, err)
		}
		for _, task := range allTasks {
			if _, err := resolveAny(r, task, providers.Policy(s)); !errors.Is(err, providers.ErrUnknownPolicy) {
				t.Errorf("Resolve(%s, %q) = %v, want ErrUnknownPolicy", task, s, err)
			}
		}
	}
	for _, s := range []string{"LOCAL_ONLY", "LOCAL_FIRST_CLOUD_FALLBACK", "CLOUD_PREFERRED", "DISABLED"} {
		if _, err := providers.ParsePolicy(s); err != nil {
			t.Errorf("ParsePolicy(%q): %v", s, err)
		}
	}
	if f.cloud.Calls() != 0 {
		t.Fatal("cloud was called")
	}
}

func TestUnknownTaskIsRefused(t *testing.T) {
	f := newFakes()
	r := build(t, true, f.local(), f.cloudRegs())
	for _, task := range []providers.Task{"", "publish", "Script", " script", "script "} {
		if _, err := resolveAny(r, task, providers.LocalOnly); !errors.Is(err, providers.ErrUnknownTask) {
			t.Errorf("task %q: %v", task, err)
		}
	}
}

func TestDisabledPolicyRunsNothing(t *testing.T) {
	f := newFakes()
	r := build(t, true, f.local(), f.cloudRegs())
	for _, task := range allTasks {
		if _, err := resolveAny(r, task, providers.Disabled); !errors.Is(err, providers.ErrTaskDisabled) {
			t.Errorf("%s: %v", task, err)
		}
	}
}

func TestEmptyRegistryResolvesNothing(t *testing.T) {
	r := providers.NewRegistry(false)
	for _, task := range allTasks {
		if _, err := resolveAny(r, task, providers.LocalOnly); !errors.Is(err, providers.ErrNoProvider) {
			t.Errorf("%s: %v", task, err)
		}
	}
}

func TestCloudPoliciesFailClosed(t *testing.T) {
	f := newFakes()
	for _, p := range []providers.Policy{providers.LocalFirstCloudFallback, providers.CloudPreferred} {
		// Cloud setting off: refused as disabled, whether or not a local provider exists (CLOUD_PREFERRED),
		// and LOCAL_FIRST with no local provider.
		r := build(t, false, f.cloudRegs())
		for _, task := range allTasks {
			if _, err := resolveAny(r, task, p); !errors.Is(err, providers.ErrCloudDisabled) {
				t.Errorf("%s %s cloud off: %v", p, task, err)
			}
		}
		// Cloud setting on: there is no budget check, so it is still refused.
		r = build(t, true, f.cloudRegs())
		for _, task := range allTasks {
			if _, err := resolveAny(r, task, p); !errors.Is(err, providers.ErrBudgetNotReady) {
				t.Errorf("%s %s cloud on: %v", p, task, err)
			}
		}
		// Cloud on but nothing registered: no provider.
		r = build(t, true)
		for _, task := range allTasks {
			if _, err := resolveAny(r, task, p); !errors.Is(err, providers.ErrNoProvider) {
				t.Errorf("%s %s empty: %v", p, task, err)
			}
		}
	}
	// Even with local providers present, CLOUD_PREFERRED never succeeds while the budget is missing.
	r := build(t, true, f.local(), f.cloudRegs())
	for _, task := range allTasks {
		if _, err := resolveAny(r, task, providers.CloudPreferred); !errors.Is(err, providers.ErrBudgetNotReady) {
			t.Errorf("CLOUD_PREFERRED with local present %s: %v", task, err)
		}
	}
	// LOCAL_FIRST uses the local provider when there is one.
	for _, task := range allTasks {
		got, err := resolveAny(r, task, providers.LocalFirstCloudFallback)
		if err != nil || got == any(f.cloud) {
			t.Errorf("LOCAL_FIRST %s: %v %v", task, got, err)
		}
	}
	if f.cloud.Calls() != 0 {
		t.Fatal("cloud was called")
	}
}

func TestCloudIsOffUnlessConfigured(t *testing.T) {
	f := newFakes()
	r := providers.NewRegistry(false) // the zero configuration
	for _, reg := range f.cloudRegs() {
		if err := r.Register(reg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := providers.Resolve[providers.LLM](r, providers.TaskScript, providers.CloudPreferred); !errors.Is(err, providers.ErrCloudDisabled) {
		t.Fatalf("got %v", err)
	}
}

func TestKindComesFromTheRegistry(t *testing.T) {
	// The spy knows nothing about kinds, so a provider cannot claim to be local. Registered as cloud,
	// LOCAL_ONLY cannot reach it however it is named.
	f := newFakes()
	r := build(t, true, []providers.Registration{{Name: "local-llm-trust-me", Kind: providers.KindCloud, Capability: providers.CapLLM, Impl: f.cloud}})
	if _, err := providers.Resolve[providers.LLM](r, providers.TaskScript, providers.LocalOnly); !errors.Is(err, providers.ErrNoProvider) {
		t.Fatalf("got %v", err)
	}
	if err := providers.NewRegistry(false).Register(providers.Registration{Name: "x", Kind: "Local", Capability: providers.CapLLM, Impl: f.llm}); err == nil {
		t.Fatal("kind with different case accepted")
	}
	if err := providers.NewRegistry(false).Register(providers.Registration{Name: "x", Kind: "", Capability: providers.CapLLM, Impl: f.llm}); err == nil {
		t.Fatal("empty kind accepted")
	}
}

func TestRegistrationIsIdempotentAndValidated(t *testing.T) {
	f := newFakes()
	r := providers.NewRegistry(false)
	reg := providers.Registration{Name: "llm", Kind: providers.KindLocal, Capability: providers.CapLLM, Impl: f.llm}
	for i := 0; i < 3; i++ {
		if err := r.Register(reg); err != nil {
			t.Fatal(err)
		}
	}
	other := &fake.LLM{}
	if err := r.Register(providers.Registration{Name: "llm", Kind: providers.KindLocal, Capability: providers.CapLLM, Impl: other}); !errors.Is(err, providers.ErrConflictingName) {
		t.Fatalf("different impl under same name: %v", err)
	}
	if err := r.Register(providers.Registration{Name: "llm", Kind: providers.KindCloud, Capability: providers.CapLLM, Impl: f.llm}); !errors.Is(err, providers.ErrConflictingName) {
		t.Fatalf("a local name re-registered as cloud: %v", err)
	}
	for name, bad := range map[string]providers.Registration{
		"empty name":      {Kind: providers.KindLocal, Capability: providers.CapLLM, Impl: f.llm},
		"wrong interface": {Name: "a", Kind: providers.KindLocal, Capability: providers.CapImage, Impl: f.llm},
		"unknown cap":     {Name: "b", Kind: providers.KindLocal, Capability: "video", Impl: f.llm},
		"nil impl":        {Name: "c", Kind: providers.KindLocal, Capability: providers.CapLLM},
	} {
		if err := r.Register(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// One registration only: the second LLM did not displace the first.
	got, err := providers.Resolve[providers.LLM](r, providers.TaskScript, providers.LocalOnly)
	if err != nil || got != providers.LLM(f.llm) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestResolvedLocalProvidersStillWork(t *testing.T) {
	f := newFakes()
	r := build(t, false, f.local())
	llm, err := providers.Resolve[providers.LLM](r, providers.TaskScript, providers.LocalOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.GenerateJSON(context.Background(), "p", nil, 1); err != nil || f.llm.Calls != 1 {
		t.Fatalf("err=%v calls=%d", err, f.llm.Calls)
	}
}
