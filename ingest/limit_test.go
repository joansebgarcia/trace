package ingest_test

import (
	"net/http"
	"testing"
)

func TestProjectOverItsRateLimitIsRateLimited(t *testing.T) {
	// 1 Event/s allows a burst of 100 Events, one full batch.
	e := newEndpoint(t, withRateLimit(1))
	_, key := createProject(t, e)
	post(t, e, key, 100, http.StatusAccepted)

	res, body := send(t, e, request{Headers: map[string]string{"X-Trace-Ingest-Key": key}, Body: batchOf(1)})

	assertError(t, res, body, http.StatusTooManyRequests, "rate_limited")
	if got := res.Header.Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want 1", got)
	}
}

func TestRateLimitedBatchConsumesNoTokens(t *testing.T) {
	e := newEndpoint(t, withRateLimit(1))
	_, key := createProject(t, e)
	post(t, e, key, 60, http.StatusAccepted)

	res, body := send(t, e, request{Headers: map[string]string{"X-Trace-Ingest-Key": key}, Body: batchOf(50)})

	assertError(t, res, body, http.StatusTooManyRequests, "rate_limited")
	// 40 tokens are left, and 1 Event/s adds the 10 missing in 10 s.
	if got := res.Header.Get("Retry-After"); got != "10" {
		t.Errorf("Retry-After = %q, want 10", got)
	}
	post(t, e, key, 40, http.StatusAccepted)
}

func TestRateLimitOfAProjectDoesNotAffectAnother(t *testing.T) {
	e := newEndpoint(t, withRateLimit(1))
	_, noisy := createProject(t, e)
	_, quiet := createProject(t, e)
	post(t, e, noisy, 100, http.StatusAccepted)
	post(t, e, noisy, 1, http.StatusTooManyRequests)

	post(t, e, quiet, 100, http.StatusAccepted)
}

func TestProjectRateLimitOverridesTheDefault(t *testing.T) {
	e := newEndpoint(t)
	limited, limitedKey := createProject(t, e)
	_, defaultKey := createProject(t, e)
	if err := e.Projects.SetRateLimit(t.Context(), limited.ID, 1); err != nil {
		t.Fatalf("set rate limit: %v", err)
	}

	post(t, e, limitedKey, 100, http.StatusAccepted)
	post(t, e, limitedKey, 1, http.StatusTooManyRequests)
	// The default of 200 Events/s allows a burst of 1000.
	for range 10 {
		post(t, e, defaultKey, 100, http.StatusAccepted)
	}
}

// post sends a batch of n minimal Events with key and fails unless it gets
// status.
func post(t *testing.T, e *endpoint, key string, n, status int) {
	t.Helper()

	res, body := send(t, e, request{Headers: map[string]string{"X-Trace-Ingest-Key": key}, Body: batchOf(n)})
	if res.StatusCode != status {
		t.Fatalf("batch of %d: response = %d %s, want %d", n, res.StatusCode, body, status)
	}
}
