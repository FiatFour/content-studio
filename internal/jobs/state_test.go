package jobs

import "testing"

func TestTransitionEveryPair(t *testing.T) {
	allowed := map[[2]State]bool{
		{Queued, Running}: true, {Queued, Cancelled}: true,
		{Running, Completed}: true, {Running, Failed}: true, {Running, RetryScheduled}: true,
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
