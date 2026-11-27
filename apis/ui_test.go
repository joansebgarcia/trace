package apis_test

import (
	"net/http"
	"testing"
	"testing/fstest"
)

func TestUI(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":         {Data: []byte(`<div id="app">index</div>`)},
		"assets/app-1a2b.js": {Data: []byte(`console.log("app")`)},
	}
	newApp := func(t testing.TB) *App {
		app, err := StartApp(t, t.TempDir(), WithUI(ui))
		if err != nil {
			t.Fatalf("start app: %v", err)
		}
		return app
	}

	cases := []Case{
		{
			Name:            "root serves index.html",
			Method:          http.MethodGet,
			Path:            "/",
			NewApp:          newApp,
			ExpectedStatus:  http.StatusOK,
			ExpectedHeaders: map[string]string{"Content-Type": "text/html; charset=utf-8"},
			ExpectedBody:    `<div id="app">index</div>`,
		},
		{
			Name:            "UI route serves index.html",
			Method:          http.MethodGet,
			Path:            "/issues/123",
			NewApp:          newApp,
			ExpectedStatus:  http.StatusOK,
			ExpectedHeaders: map[string]string{"Content-Type": "text/html; charset=utf-8"},
			ExpectedBody:    `<div id="app">index</div>`,
		},
		{
			Name:            "static asset is served as itself",
			Method:          http.MethodGet,
			Path:            "/assets/app-1a2b.js",
			NewApp:          newApp,
			ExpectedStatus:  http.StatusOK,
			ExpectedHeaders: map[string]string{"Content-Type": "text/javascript; charset=utf-8"},
			ExpectedBody:    `console.log("app")`,
		},
		{
			Name:           "missing asset is not found",
			Method:         http.MethodGet,
			Path:           "/assets/app-old.js",
			NewApp:         newApp,
			ExpectedStatus: http.StatusNotFound,
		},
		{
			Name:           "assets directory is not found",
			Method:         http.MethodGet,
			Path:           "/assets/",
			NewApp:         newApp,
			ExpectedStatus: http.StatusNotFound,
		},
		{
			Name:            "unknown API path returns the JSON error",
			Method:          http.MethodGet,
			Path:            "/api/missing",
			NewApp:          newApp,
			ExpectedStatus:  http.StatusNotFound,
			ExpectedHeaders: map[string]string{"Content-Type": "application/json"},
			ExpectedBody:    `{"error":"not_found"}`,
		},
		{
			Name:           "other methods are not allowed",
			Method:         http.MethodPost,
			Path:           "/issues/123",
			NewApp:         newApp,
			ExpectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, c := range cases {
		c.Run(t)
	}
}
