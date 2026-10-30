package providers

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// Kind says where a provider runs. It is declared at registration and kept by the registry;
// a provider cannot report its own kind, so it cannot lie about it.
type Kind string

const (
	KindLocal Kind = "local"
	// KindCloud exists so policy can be enforced and tested. No cloud adapter exists (D10 is open).
	KindCloud Kind = "cloud"
)

// Capability is what a provider can do. The set is closed: Register refuses anything else.
type Capability string

const (
	CapLLM   Capability = "llm"
	CapImage Capability = "image"
	CapTTS   Capability = "tts"
	// CapSegment is Thai word segmentation, which M1 serves from the local thaivoice service.
	CapSegment Capability = "segment"
)

// Task is a pipeline stage that needs a provider. Each task needs exactly one capability.
type Task string

const (
	TaskScript    Task = "script"
	TaskTTS       Task = "tts"
	TaskImages    Task = "images"
	TaskSubtitles Task = "subtitles"
)

var taskCapability = map[Task]Capability{
	TaskScript:    CapLLM,
	TaskTTS:       CapTTS,
	TaskImages:    CapImage,
	TaskSubtitles: CapSegment,
}

// Policy decides which kinds of provider a task may reach (providers-budget.md, Task policies).
type Policy string

const (
	LocalOnly               Policy = "LOCAL_ONLY"
	LocalFirstCloudFallback Policy = "LOCAL_FIRST_CLOUD_FALLBACK"
	CloudPreferred          Policy = "CLOUD_PREFERRED"
	Disabled                Policy = "DISABLED"

	// DefaultPolicy applies to every task.
	DefaultPolicy = LocalOnly
)

// ParsePolicy accepts only the exact policy names. Case and whitespace variants are refused,
// so a policy never gets "close enough" to a more permissive one.
func ParsePolicy(s string) (Policy, error) {
	switch p := Policy(s); p {
	case LocalOnly, LocalFirstCloudFallback, CloudPreferred, Disabled:
		return p, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownPolicy, s)
}

var (
	ErrUnknownTask     = errors.New("unknown task")
	ErrUnknownPolicy   = errors.New("unknown policy")
	ErrTaskDisabled    = errors.New("task is disabled by policy")
	ErrNoProvider      = errors.New("no provider is allowed for this task")
	ErrCloudDisabled   = errors.New("cloud providers are disabled")
	ErrBudgetNotReady  = errors.New("budget check not implemented: cloud request refused")
	ErrConflictingName = errors.New("provider name is already registered with a different definition")
)

// Registration declares one provider.
type Registration struct {
	Name       string
	Kind       Kind
	Capability Capability
	// Impl must implement the interface of Capability (LLM, ImageGen, Voice or Segmenter).
	Impl any
}

// Registry holds every provider and is the only way a stage obtains one (see Resolve).
// Cloud use is off unless NewRegistry is told otherwise; that flag is backend configuration
// and nothing a model produces can reach it.
type Registry struct {
	cloudEnabled bool

	mu      sync.RWMutex
	entries []Registration // registration order decides which provider wins
}

func NewRegistry(cloudEnabled bool) *Registry { return &Registry{cloudEnabled: cloudEnabled} }

// Register adds a provider. Registering the identical definition again is a no-op;
// reusing a name for a different definition is an error.
func (r *Registry) Register(reg Registration) error {
	if reg.Name == "" {
		return errors.New("provider name is empty")
	}
	if reg.Kind != KindLocal && reg.Kind != KindCloud {
		return fmt.Errorf("provider %q: unknown kind %q", reg.Name, reg.Kind)
	}
	if !implements(reg.Capability, reg.Impl) {
		return fmt.Errorf("provider %q does not implement capability %q", reg.Name, reg.Capability)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.Name != reg.Name {
			continue
		}
		if e.Kind == reg.Kind && e.Capability == reg.Capability && sameImpl(e.Impl, reg.Impl) {
			return nil
		}
		return fmt.Errorf("%w: %q", ErrConflictingName, reg.Name)
	}
	r.entries = append(r.entries, reg)
	return nil
}

func implements(c Capability, impl any) bool {
	switch c {
	case CapLLM:
		_, ok := impl.(LLM)
		return ok
	case CapImage:
		_, ok := impl.(ImageGen)
		return ok
	case CapTTS:
		_, ok := impl.(Voice)
		return ok
	case CapSegment:
		_, ok := impl.(Segmenter)
		return ok
	}
	return false
}

func sameImpl(a, b any) bool {
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	return ta == tb && ta.Comparable() && a == b
}

// Resolve returns the provider a task may use under policy, as T (LLM, ImageGen, Voice or Segmenter).
// It fails closed: an unknown task or policy, a policy that forbids the only available provider,
// and every cloud outcome is an error, never a fallback to something else.
//
// A policy that allows cloud still needs the cloud setting on, and then the budget check, which
// does not exist yet, so the request is refused with ErrBudgetNotReady.
func Resolve[T any](r *Registry, task Task, policy Policy) (T, error) {
	var zero T
	impl, err := r.resolve(task, policy)
	if err != nil {
		return zero, err
	}
	t, ok := impl.(T)
	if !ok {
		return zero, fmt.Errorf("task %q: provider has the wrong type %T", task, impl)
	}
	return t, nil
}

func (r *Registry) resolve(task Task, policy Policy) (any, error) {
	capab, ok := taskCapability[task]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTask, task)
	}
	if _, err := ParsePolicy(string(policy)); err != nil {
		return nil, err
	}
	if policy == Disabled {
		return nil, fmt.Errorf("%w: %s", ErrTaskDisabled, task)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	find := func(k Kind) any {
		for _, e := range r.entries {
			if e.Kind == k && e.Capability == capab {
				return e.Impl
			}
		}
		return nil
	}
	if policy == LocalOnly || policy == LocalFirstCloudFallback {
		if impl := find(KindLocal); impl != nil {
			return impl, nil
		}
		if policy == LocalOnly {
			return nil, fmt.Errorf("%w: %s needs a local %s provider", ErrNoProvider, task, capab)
		}
	}
	// Only CLOUD_PREFERRED, or LOCAL_FIRST_CLOUD_FALLBACK with no local provider, reaches here.
	if !r.cloudEnabled {
		return nil, fmt.Errorf("%w: %s", ErrCloudDisabled, task)
	}
	if find(KindCloud) == nil {
		return nil, fmt.Errorf("%w: %s has no cloud %s provider", ErrNoProvider, task, capab)
	}
	return nil, fmt.Errorf("%w: %s", ErrBudgetNotReady, task)
}

// TaskInfo pairs a task with the capability it needs.
type TaskInfo struct {
	Task       Task
	Capability Capability
}

// Tasks lists every task with the capability it needs, in pipeline order. It is read-only data for
// the settings screen; every task runs under DefaultPolicy (nothing makes it configurable yet).
func Tasks() []TaskInfo {
	return []TaskInfo{
		{TaskScript, taskCapability[TaskScript]},
		{TaskTTS, taskCapability[TaskTTS]},
		{TaskImages, taskCapability[TaskImages]},
		{TaskSubtitles, taskCapability[TaskSubtitles]},
	}
}
