package jobs

import "time"

// backoff is the retry schedule of one error class: base * 2^(n-1) for the n-th failure, capped at max.
type backoff struct{ base, max time.Duration }

var backoffs = map[ErrorClass]backoff{
	ErrTransientNetwork:  {2 * time.Second, 2 * time.Minute},
	ErrValidationFailed:  {time.Second, 30 * time.Second}, // a re-prompt, so quick
	ErrRateLimited:       {5 * time.Second, 10 * time.Minute},
	ErrProviderOutage:    {30 * time.Second, 15 * time.Minute}, // longer: outages last
	ErrInsufficientLocal: {15 * time.Second, 5 * time.Minute},
	ErrWorkerRestart:     {0, 0},
}

// MaxRetryAfter caps a provider's retry-after hint. It comes from the network, so it is untrusted:
// a huge value must not park a job for days.
const MaxRetryAfter = time.Hour

// RetryDelay is how long to wait before the next attempt after the attemptsUsed-th failure of a
// retryable class (attemptsUsed counts from 1). The delay doubles each failure up to the class cap
// and then gets equal jitter: it lands in [d/2, d], where jitter in [0, 1) picks the point, so
// workers that failed together do not retry together. For rate_limited the provider's retry-after
// is a floor (capped at MaxRetryAfter). Classes without a schedule wait zero.
func RetryDelay(class ErrorClass, attemptsUsed int, retryAfter time.Duration, jitter float64) time.Duration {
	b := backoffs[class]
	d := b.base
	for i := 1; i < attemptsUsed && d < b.max; i++ {
		d *= 2
	}
	if d > b.max {
		d = b.max
	}
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	d = d/2 + time.Duration(float64(d/2)*jitter)
	if class == ErrRateLimited && retryAfter > 0 {
		if retryAfter > MaxRetryAfter {
			retryAfter = MaxRetryAfter
		}
		if retryAfter > d {
			d = retryAfter
		}
	}
	return d
}
