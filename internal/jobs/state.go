// Package jobs owns job and run state, the Postgres queue, and the worker loop.
package jobs

import "fmt"

type State string

const (
	Queued             State = "queued"
	Running            State = "running"
	WaitingForResource State = "waiting_for_resource"
	WaitingForHuman    State = "waiting_for_human"
	RetryScheduled     State = "retry_scheduled"
	Failed             State = "failed"
	Cancelled          State = "cancelled"
	Completed          State = "completed"
)

var AllStates = []State{Queued, Running, WaitingForResource, WaitingForHuman, RetryScheduled, Failed, Cancelled, Completed}

// legal is the transition table from docs/spec/jobs.md. Every job state change goes through Transition.
var legal = map[State][]State{
	Queued:             {Running, WaitingForResource, Cancelled},
	Running:            {Completed, Failed, RetryScheduled, Queued, WaitingForResource, WaitingForHuman, Cancelled},
	WaitingForResource: {Queued, Cancelled},
	WaitingForHuman:    {Queued, Completed, Cancelled},
	RetryScheduled:     {Queued, Cancelled},
	Failed:             {Queued},
}

func Transition(from, to State) error {
	for _, s := range legal[from] {
		if s == to {
			return nil
		}
	}
	return fmt.Errorf("illegal job transition %s -> %s", from, to)
}

// RunState is the episode-level state shown in the UI.
type RunState string

const (
	RunRunning        RunState = "running"
	RunReadyForReview RunState = "ready_for_review"
	RunFailed         RunState = "failed"
	RunCancelled      RunState = "cancelled"
)

// ErrorClass values from docs/spec/jobs.md, plus worker_restart for M1a crash recovery
// and qa_failed for a blocking QA rule.
type ErrorClass string

const (
	ErrInvalidInput      ErrorClass = "invalid_input"
	ErrTransientNetwork  ErrorClass = "transient_network"
	ErrValidationFailed  ErrorClass = "validation_failed"
	ErrUnsupported       ErrorClass = "unsupported"
	ErrInsufficientLocal ErrorClass = "insufficient_local_resources"
	ErrInternal          ErrorClass = "internal"
	ErrWorkerRestart     ErrorClass = "worker_restart"
	ErrQAFailed          ErrorClass = "qa_failed"
	ErrScriptDuration    ErrorClass = "script_duration"
	ErrCancelled         ErrorClass = "cancelled"
)

// Remedy is what the queue does when a stage fails with a given error class.
type Remedy int

const (
	FailRun Remedy = iota // fail the job and its run; a person decides (the default)
	RetryStage
	RewriteScript // fail the run, then start one new run with the message as scriptwriter feedback
	CancelRun
)

var remedies = map[ErrorClass]Remedy{
	ErrTransientNetwork:  RetryStage,
	ErrValidationFailed:  RetryStage,
	ErrInsufficientLocal: RetryStage,
	ErrWorkerRestart:     RetryStage,
	ErrScriptDuration:    RewriteScript,
	ErrCancelled:         CancelRun,
}

// Remedy returns FailRun for any class not in the table, so a new class is safe until it is classified.
func (c ErrorClass) Remedy() Remedy { return remedies[c] }

// StageError is what a stage returns to say how it failed. Message must already be safe to show.
type StageError struct {
	Class   ErrorClass
	Message string
	Err     error
}

func (e *StageError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Class, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Class, e.Message)
}

func (e *StageError) Unwrap() error { return e.Err }

func Errorf(class ErrorClass, err error, format string, args ...any) *StageError {
	return &StageError{Class: class, Message: fmt.Sprintf(format, args...), Err: err}
}
