//go:build unix

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joansgarcia/trace/apis"
	"github.com/joansgarcia/trace/hub"
	"github.com/joansgarcia/trace/store"
)

const timeout = 5 * time.Second

// TestBenchEndpoint checks the benchmark endpoint in the build it runs in:
// with the bench tag it enqueues, and without it, as in release builds, it
// does not exist.
func TestBenchEndpoint(t *testing.T) {
	if !hasBuildTag(t, "bench") {
		s := startApp(t)

		if status, _ := post(t, s, http.MethodPost, `{"event_id":"e1"}`); status != http.StatusNotFound {
			t.Errorf("status = %d, want 404 without the bench tag", status)
		}
		return
	}

	t.Run("accepted Event is committed to the queue", func(t *testing.T) {
		s := startApp(t)

		if status, body := post(t, s, http.MethodPost, `{"event_id":"e1"}`); status != http.StatusAccepted {
			t.Errorf("response = %d %s, want 202", status, body)
		}
		if got := s.Queue().Pending(); got != 1 {
			t.Errorf("pending = %d, want 1", got)
		}
	})

	t.Run("Event sent while the queue is closing returns shutting down", func(t *testing.T) {
		s := startApp(t)
		if err := s.Queue().Close(t.Context()); err != nil {
			t.Fatalf("stop queue: %v", err)
		}

		status, body := post(t, s, http.MethodPost, `{"event_id":"e1"}`)

		if status != http.StatusServiceUnavailable || strings.TrimSpace(body) != `{"error":"shutting_down"}` {
			t.Errorf("response = %d %s, want 503 shutting_down", status, body)
		}
	})

	t.Run("other methods are not allowed", func(t *testing.T) {
		s := startApp(t)

		if status, _ := post(t, s, http.MethodGet, ""); status != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", status)
		}
	})
}

// startApp starts a server whose queue keeps every Event, as none is
// processed. It stops when the test ends.
func startApp(t *testing.T) *apis.Server {
	t.Helper()

	held := errors.New("held in the queue")
	s := apis.NewServer(apis.Config{
		Addr:            "127.0.0.1:0",
		DataDir:         t.TempDir(),
		ShutdownTimeout: timeout,
		Process:         func(context.Context, *store.DB, []byte) ([]hub.Change, error) { return nil, held },
	})
	if err := s.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}

// post sends body to the benchmark endpoint of s with method and returns the
// status and body of the response.
func post(t *testing.T, s *apis.Server, method, body string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, "http://"+s.Addr()+benchPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res.StatusCode, string(got)
}

// hasBuildTag reports whether the test binary was built with tag.
func hasBuildTag(t *testing.T, tag string) bool {
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
