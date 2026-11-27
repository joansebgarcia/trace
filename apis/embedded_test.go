package apis_test

import (
	"net/http"
	"testing"

	"github.com/joansebgarcia/amber/web"
)

// TestEmbeddedUI checks the web UI built into the binary it runs in: with
// the ui tag, GET / serves the index.html of the Vite build, and without it,
// a placeholder page.
func TestEmbeddedUI(t *testing.T) {
	if !web.Embedded {
		Case{
			Name:            "build without the UI serves the placeholder",
			Method:          http.MethodGet,
			Path:            "/",
			ExpectedStatus:  http.StatusOK,
			ExpectedHeaders: map[string]string{"Content-Type": "text/html; charset=utf-8"},
			ExpectedContent: []string{"The web UI is not included in this build"},
		}.Run(t)
		return
	}

	cases := []Case{
		{
			Name:            "build with the UI serves its index.html",
			Method:          http.MethodGet,
			Path:            "/",
			ExpectedStatus:  http.StatusOK,
			ExpectedHeaders: map[string]string{"Content-Type": "text/html; charset=utf-8"},
			ExpectedContent: []string{`<div id="app">`, `<script type="module"`},
		},
		{
			Name:            "UI route serves its index.html",
			Method:          http.MethodGet,
			Path:            "/issues/123",
			ExpectedStatus:  http.StatusOK,
			ExpectedContent: []string{`<div id="app">`},
		},
	}

	for _, c := range cases {
		c.Run(t)
	}
}
