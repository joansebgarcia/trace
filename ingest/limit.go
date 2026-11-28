package ingest

import (
	"math"
	"sync"
	"time"

	"github.com/joansgarcia/trace/event"
)

// burstSeconds is the burst of a bucket, in seconds of its rate. It is
// never below one full batch, so every valid batch fits eventually.
const burstSeconds = 5

// limiter holds a token bucket per Project, in memory. Each token is one
// Event.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// bucket is the token bucket of one Project.
type bucket struct {
	rate   float64
	tokens float64
	at     time.Time
}

func newLimiter() *limiter {
	return &limiter{buckets: map[string]*bucket{}}
}

// take takes n tokens of the bucket of projectID, which refills at rate
// Events/s, and reports whether they fit. A batch that does not fit takes
// none, and gets the wait until it would.
func (l *limiter) take(projectID string, rate float64, n int, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.refill(projectID, rate, now)
	if wait := b.waitFor(n); wait > 0 {
		return false, wait
	}
	b.tokens -= float64(n)
	return true, 0
}

// check reports, without taking any, whether n tokens of the bucket of
// projectID are there, and the wait until they would be.
func (l *limiter) check(projectID string, rate float64, n int, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	wait := l.refill(projectID, rate, now).waitFor(n)
	return wait == 0, wait
}

// give returns n tokens taken from the bucket of projectID, for a batch the
// queue then refused.
func (l *limiter) give(projectID string, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if b, ok := l.buckets[projectID]; ok {
		b.tokens = min(b.tokens+float64(n), computeBurst(b.rate))
	}
}

// refill returns the bucket of projectID, created full, with the tokens
// earned since its last use at rate.
func (l *limiter) refill(projectID string, rate float64, now time.Time) *bucket {
	b, ok := l.buckets[projectID]
	if !ok {
		b = &bucket{rate: rate, tokens: computeBurst(rate), at: now}
		l.buckets[projectID] = b
	}
	elapsed := max(now.Sub(b.at).Seconds(), 0)
	b.rate = rate
	b.tokens = min(b.tokens+elapsed*rate, computeBurst(rate))
	b.at = now
	return b
}

// waitFor returns how long b takes to hold n tokens; zero if it holds them.
func (b *bucket) waitFor(n int) time.Duration {
	need := float64(n) - b.tokens
	if need <= 0 {
		return 0
	}
	return time.Duration(need / b.rate * float64(time.Second))
}

// computeBurst returns the capacity of a bucket that refills at rate.
func computeBurst(rate float64) float64 {
	return max(rate*burstSeconds, event.MaxEvents)
}

// roundUpSeconds returns wait as a Retry-After: whole seconds, at least 1.
func roundUpSeconds(wait time.Duration) int {
	return max(int(math.Ceil(wait.Seconds())), 1)
}
