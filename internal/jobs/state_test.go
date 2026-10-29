package jobs

import "testing"

func TestTransitionEveryPair(t *testing.T) {
	allowed := map[[2]State]bool{
		{Queued, Running}: true, {Queued, Cancelled}: true, {Queued, WaitingForResource}: true,
		{Running, Completed}: true, {Running, Failed}: true, {Running, RetryScheduled}: true, {Running, Queued}: true,
		{Running, WaitingForResource}: true, {Running, WaitingForHuman}: true, {Running, Cancelled}: true,
		{WaitingForResource, Queued}: true, {WaitingForResource, Cancelled}: true,
		{WaitingForHuman, Queued}: true, {WaitingForHuman, Completed}: true, {WaitingForHuman, Cancelled}: true,
		{RetryScheduled, Queued}: true, {RetryScheduled, Cancelled}: true,
		{Failed, Queued}: true,
	}
	for _, from := range AllStates {
		for _, to := range AllStates {
			err := Transition(from, to)
			if want := allowed[[2]State{from, to}]; want != (err == nil) {
				t.Errorf("%s -> %s: allowed=%v, got err=%v", from, to, want, err)
			}
		}
	}
}

func TestEveryErrorClassHasTheIntendedRemedy(t *testing.T) {
	want := map[ErrorClass]Remedy{
		ErrTransientNetwork: RetryStage, ErrValidationFailed: RetryStage,
		ErrRateLimited: RetryStage, ErrProviderOutage: RetryStage,
		ErrInsufficientLocal: WaitResource, ErrWorkerRestart: RetryStage,
		ErrScriptDuration: RewriteScript, ErrCancelled: CancelRun,
		ErrInvalidInput: FailRun, ErrAuthOrPermission: FailRun, ErrUnsupported: FailRun, ErrInternal: FailRun,
		ErrUncertainOutcome: FailRun,
		ErrQAFailed:         FailRun, ErrorClass("a_class_nobody_classified"): FailRun,
	}
	if got := len(AllErrorClasses()); got != len(want)-1 {
		t.Errorf("table has %d classes, test expects %d", got, len(want)-1)
	}
	for c, r := range want {
		if got := c.Remedy(); got != r {
			t.Errorf("%s: remedy %d, want %d", c, got, r)
		}
	}
}
