package auth

import (
	"sync"
	"time"
)

// limiter is a minimal failure counter per key (the client IP): after max failures within window the
// key is refused until the oldest failure ages out. In-memory; a restart resets it. Full rate limiting
// is issue #17.
type limiter struct {
	max    int
	window time.Duration

	mu    sync.Mutex
	fails map[string][]time.Time
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, now)) < l.max
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	if len(l.fails) > 1024 { // bound memory under a spray of distinct keys
		for k := range l.fails {
			if len(l.prune(k, now)) == 0 {
				delete(l.fails, k)
			}
		}
	}
	l.fails[key] = append(l.prune(key, now), now)
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func (l *limiter) prune(key string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = keep
	}
	return keep
}
