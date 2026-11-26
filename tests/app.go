// Package tests provides the test seam shared by every server block: the app
// running inside the test process over a temporary data directory, driven by
// HTTP.
package tests

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/joansebgarcia/amber/apis"
)

const timeout = 5 * time.Second

// App is an app started for a test. It stops when the test ends.
type App struct {
	*apis.Server
	// DataDir is the temporary data directory of the app.
	DataDir string
	// URL is the base URL of the HTTP API, such as "http://127.0.0.1:54321".
	URL    string
	Client *http.Client
}

// NewApp starts an app on a free local port over a temporary data directory.
func NewApp(t testing.TB) *App {
	t.Helper()

	server := apis.NewServer(apis.Config{
		Addr:            "127.0.0.1:0",
		ShutdownTimeout: timeout,
	})
	if err := server.Start(); err != nil {
		t.Fatalf("start app: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown app: %v", err)
		}
	})

	return &App{
		Server:  server,
		DataDir: t.TempDir(),
		URL:     "http://" + server.Addr(),
		Client:  &http.Client{Timeout: timeout},
	}
}
