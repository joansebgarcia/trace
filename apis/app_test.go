package apis_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joansgarcia/trace/apis"
	"github.com/joansgarcia/trace/hub"
	"github.com/joansgarcia/trace/store"
)

const timeout = 5 * time.Second

// errHeld is the error of the processing function of WithoutProcess.
var errHeld = errors.New("held in the queue")

// App is an app started for a test. It stops when the test ends.
type App struct {
	*apis.Server
	// DataDir is the data directory of the app.
	DataDir string
	// URL is the base URL of the HTTP API, such as "http://127.0.0.1:54321".
	URL    string
	Client *http.Client
	// BrokenStore makes Stop ignore the shutdown error, for tests that break
	// the data directory on purpose.
	BrokenStore bool
	stopped     bool
}

// Option changes the config of an app before it starts.
type Option func(*apis.Config)

// Logs collects the logs of an app, at every level, as JSON records.
type Logs struct {
	mu  sync.Mutex
	out bytes.Buffer
}

func (l *Logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.Write(p)
}

// Wait returns the first record with message msg, waiting for it to be
// logged.
func (l *Logs) Wait(t testing.TB, msg string) map[string]any {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if record, ok := l.find(t, msg); ok {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %q log record", msg)
	return nil
}

func (l *Logs) find(t testing.TB, msg string) (map[string]any, bool) {
	t.Helper()

	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(l.out.String(), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("parse log line %q: %v", line, err)
		}
		if record["msg"] == msg {
			return record, true
		}
	}
	return nil, false
}

// NewApp starts an app on a free local port over a temporary data directory.
func NewApp(t testing.TB) *App {
	t.Helper()

	app, err := StartApp(t, t.TempDir())
	if err != nil {
		t.Fatalf("start app: %v", err)
	}

	return app
}

// StartApp starts an app on a free local port over dir, which may not exist
// yet. It returns the error of a failed start instead of failing the test.
func StartApp(t testing.TB, dir string, options ...Option) (*App, error) {
	t.Helper()

	config := apis.Config{
		Addr:            "127.0.0.1:0",
		DataDir:         dir,
		ShutdownTimeout: timeout,
	}
	for _, option := range options {
		option(&config)
	}
	server := apis.NewServer(config)
	if err := server.Start(); err != nil {
		return nil, err
	}

	app := &App{
		Server:  server,
		DataDir: dir,
		URL:     "http://" + server.Addr(),
		Client:  &http.Client{Timeout: timeout},
	}
	t.Cleanup(func() {
		if !app.stopped {
			app.Stop(t)
		}
	})

	return app, nil
}

// Stop shuts the app down before the test ends, such as to start another one
// over the same data directory.
func (a *App) Stop(t testing.TB) {
	t.Helper()

	a.stopped = true
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil && !a.BrokenStore {
		t.Errorf("shutdown app: %v", err)
	}
}

// WithProcess makes the app process queued Events with process.
func WithProcess(process func(ctx context.Context, data *store.DB, event []byte) ([]hub.Change, error)) Option {
	return func(c *apis.Config) { c.Process = process }
}

// WithoutProcess makes the app keep queued Events in the queue: its
// processing function fails on every attempt, so the queue retries each one
// until it stops.
func WithoutProcess() Option {
	return WithProcess(func(context.Context, *store.DB, []byte) ([]hub.Change, error) { return nil, errHeld })
}

// WithMigrations makes the app apply migrations instead of the ones built
// into the binary.
func WithMigrations(migrations *store.Migrations) Option {
	return func(c *apis.Config) { c.Migrations = migrations }
}

// WithLogs makes the app write its logs to logs.
func WithLogs(logs *Logs) Option {
	return func(c *apis.Config) {
		c.Logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
}

// WithTrustedProxies makes the app trust the forwarding headers of requests
// from the CIDRs in proxies.
func WithTrustedProxies(t testing.TB, proxies ...string) Option {
	t.Helper()

	prefixes := make([]netip.Prefix, 0, len(proxies))
	for _, proxy := range proxies {
		prefix, err := netip.ParsePrefix(proxy)
		if err != nil {
			t.Fatalf("parse proxy: %v", err)
		}
		prefixes = append(prefixes, prefix)
	}
	return func(c *apis.Config) { c.TrustedProxies = prefixes }
}

// WithUI makes the app serve ui as the web UI instead of the one built into
// the binary.
func WithUI(ui fs.FS) Option {
	return func(c *apis.Config) { c.UI = ui }
}
