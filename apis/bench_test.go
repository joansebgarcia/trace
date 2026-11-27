package apis_test

import (
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

// TestBenchEndpoint checks the benchmark endpoint in the build it runs in:
// with the bench tag it enqueues, and without it, as in release builds, it
// does not exist.
func TestBenchEndpoint(t *testing.T) {
	if !hasBuildTag(t, "bench") {
		Case{
			Name:           "benchmark endpoint is not found without the bench tag",
			Method:         http.MethodPost,
			Path:           "/api/bench/events",
			Body:           `{"event_id":"e1"}`,
			ExpectedStatus: http.StatusNotFound,
		}.Run(t)
		return
	}

	cases := []Case{
		{
			Name:           "accepted Event is committed to the queue",
			Method:         http.MethodPost,
			Path:           "/api/bench/events",
			Body:           `{"event_id":"e1"}`,
			NewApp:         newBenchApp(1),
			ExpectedStatus: http.StatusAccepted,
		},
		{
			Name:   "Event sent while the queue is closing returns shutting down",
			Method: http.MethodPost,
			Path:   "/api/bench/events",
			Body:   `{"event_id":"e1"}`,
			NewApp: func(t testing.TB) *App {
				app := NewApp(t)
				if err := app.Queue().Close(t.Context()); err != nil {
					t.Fatalf("stop queue: %v", err)
				}
				return app
			},
			ExpectedStatus: http.StatusServiceUnavailable,
			ExpectedBody:   `{"error":"shutting_down"}`,
		},
		{
			Name:           "other methods are not allowed",
			Method:         http.MethodGet,
			Path:           "/api/bench/events",
			ExpectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, c := range cases {
		c.Run(t)
	}
}

// newBenchApp returns an app whose queue, checked when the test ends, must
// hold pending Events: the ones the case sends, as none is processed.
func newBenchApp(pending int) func(t testing.TB) *App {
	return func(t testing.TB) *App {
		app, err := StartApp(t, t.TempDir(), WithoutProcess())
		if err != nil {
			t.Fatalf("start app: %v", err)
		}
		t.Cleanup(func() {
			if got := app.Queue().Pending(); got != pending {
				t.Errorf("pending = %d, want %d", got, pending)
			}
		})
		return app
	}
}

// hasBuildTag reports whether the test binary was built with tag.
func hasBuildTag(t testing.TB, tag string) bool {
	t.Helper()

	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("read build info: not available")
	}
	for _, setting := range info.Settings {
		if setting.Key == "-tags" {
			return slices.Contains(strings.Split(setting.Value, ","), tag)
		}
	}
	return false
}
