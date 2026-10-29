package jobs

import (
	"testing"
	"time"
)

func TestRetryDelayDoublesCapsAndJitters(t *testing.T) {
	for class, b := range backoffs {
		if b.base == 0 {
			continue
		}
		prevHi := time.Duration(0)
		for n := 1; n <= 12; n++ {
			want := b.base << (n - 1)
			if want > b.max || want <= 0 {
				want = b.max
			}
			lo := RetryDelay(class, n, 0, 0)
			hi := RetryDelay(class, n, 0, 1)
			if lo != want/2 || hi != want {
				t.Errorf("%s attempt %d: delay range [%v, %v], want [%v, %v]", class, n, lo, hi, want/2, want)
			}
			mid := RetryDelay(class, n, 0, 0.5)
			if mid < lo || mid > hi {
				t.Errorf("%s attempt %d: jittered %v outside [%v, %v]", class, n, mid, lo, hi)
			}
			if hi < prevHi {
				t.Errorf("%s attempt %d: delay shrank", class, n)
			}
			prevHi = hi
		}
		if got := RetryDelay(class, 100, 0, 1); got != b.max {
			t.Errorf("%s: cap not applied, got %v want %v", class, got, b.max)
		}
	}
}

func TestProviderOutageBacksOffLongerThanTransientNetwork(t *testing.T) {
	for n := 1; n <= 5; n++ {
		if RetryDelay(ErrProviderOutage, n, 0, 0) <= RetryDelay(ErrTransientNetwork, n, 0, 0) {
			t.Errorf("attempt %d: provider_outage is not longer than transient_network", n)
		}
	}
}

func TestRateLimitedHonorsRetryAfter(t *testing.T) {
	if got := RetryDelay(ErrRateLimited, 1, 90*time.Second, 0); got != 90*time.Second {
		t.Errorf("retry-after 90s above the backoff: got %v", got)
	}
	if got := RetryDelay(ErrRateLimited, 1, time.Second, 0); got != 2500*time.Millisecond {
		t.Errorf("retry-after below the backoff must not shorten it: got %v", got)
	}
	if got := RetryDelay(ErrRateLimited, 1, 48*time.Hour, 0); got != MaxRetryAfter {
		t.Errorf("an absurd retry-after must be capped: got %v", got)
	}
	// Other classes ignore the hint.
	if got := RetryDelay(ErrTransientNetwork, 1, 90*time.Second, 0); got != time.Second {
		t.Errorf("transient_network used retry-after: %v", got)
	}
}

func TestRetryDelayBadJitterIsClamped(t *testing.T) {
	if got := RetryDelay(ErrTransientNetwork, 1, 0, -5); got != time.Second {
		t.Errorf("negative jitter: %v", got)
	}
	if got := RetryDelay(ErrTransientNetwork, 1, 0, 7); got != 2*time.Second {
		t.Errorf("jitter above 1: %v", got)
	}
}

func TestEveryRetryingClassHasASchedule(t *testing.T) {
	for _, c := range AllErrorClasses() {
		if c.Retries() {
			if _, ok := backoffs[c]; !ok {
				t.Errorf("%s retries but has no backoff schedule", c)
			}
		} else if _, ok := backoffs[c]; ok {
			t.Errorf("%s does not retry but has a backoff schedule", c)
		}
	}
}
