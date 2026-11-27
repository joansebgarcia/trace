package apis_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/joansebgarcia/amber/apis"
	"github.com/joansebgarcia/amber/store"
)

const timeout = 5 * time.Second

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
func WithProcess(process func(ctx context.Context, data *store.DB, event []byte) error) Option {
	return func(c *apis.Config) { c.Process = process }
}

// WithMigrations makes the app apply migrations instead of the ones built
// into the binary.
func WithMigrations(migrations *store.Migrations) Option {
	return func(c *apis.Config) { c.Migrations = migrations }
}
