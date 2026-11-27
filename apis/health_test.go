package apis_test

import (
	"net/http"
	"testing"
)

func TestHealth(t *testing.T) {
	cases := []Case{
		{
			Name:            "healthy instance returns ok",
			Method:          http.MethodGet,
			Path:            "/api/health",
			ExpectedStatus:  http.StatusOK,
			ExpectedHeaders: map[string]string{"Content-Type": "application/json"},
			ExpectedBody:    `{"status":"ok"}`,
		},
		{
			Name:   "unreachable data.db returns unavailable",
			Method: http.MethodGet,
			Path:   "/api/health",
			NewApp: func(t testing.TB) *App {
				app := NewApp(t)
				app.BrokenStore = true
				_ = app.Store().Data.Write.Close()
				return app
			},
			ExpectedStatus: http.StatusServiceUnavailable,
			ExpectedBody:   `{"status":"unavailable"}`,
		},
		{
			Name:   "unreachable ingest.db returns unavailable",
			Method: http.MethodGet,
			Path:   "/api/health",
			NewApp: func(t testing.TB) *App {
				app := NewApp(t)
				_ = app.Store().Ingest.Read.Close()
				return app
			},
			ExpectedStatus: http.StatusServiceUnavailable,
			ExpectedBody:   `{"status":"unavailable"}`,
		},
		{
			Name:   "stopped queue writer returns unavailable",
			Method: http.MethodGet,
			Path:   "/api/health",
			NewApp: func(t testing.TB) *App {
				app := NewApp(t)
				if err := app.Queue().Close(t.Context()); err != nil {
					t.Fatalf("stop queue: %v", err)
				}
				return app
			},
			ExpectedStatus: http.StatusServiceUnavailable,
			ExpectedBody:   `{"status":"unavailable"}`,
		},
		{
			Name:           "other methods are not allowed",
			Method:         http.MethodPost,
			Path:           "/api/health",
			ExpectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, c := range cases {
		c.Run(t)
	}
}
