package apis_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Case is one HTTP request against an app and its expected response.
type Case struct {
	Name   string
	Method string
	// Path is the request path, such as "/api/health".
	Path    string
	Body    string
	Headers map[string]string

	// NewApp builds the app for the case. Nil uses NewApp.
	NewApp func(t testing.TB) *App

	ExpectedStatus  int
	ExpectedHeaders map[string]string
	// ExpectedBody is the exact response body, ignoring surrounding
	// whitespace. Empty skips the check.
	ExpectedBody string
	// ExpectedContent lists substrings the response body must contain.
	ExpectedContent []string
}

// Run runs the case as a subtest of t.
func (c Case) Run(t *testing.T) {
	t.Helper()

	t.Run(c.Name, func(t *testing.T) {
		newApp := c.NewApp
		if newApp == nil {
			newApp = NewApp
		}
		a := newApp(t)

		res, body := c.send(t, a)

		if res.StatusCode != c.ExpectedStatus {
			t.Errorf("status = %d, want %d; body: %s", res.StatusCode, c.ExpectedStatus, body)
		}
		for name, want := range c.ExpectedHeaders {
			if got := res.Header.Get(name); got != want {
				t.Errorf("header %s = %q, want %q", name, got, want)
			}
		}
		if c.ExpectedBody != "" && strings.TrimSpace(body) != strings.TrimSpace(c.ExpectedBody) {
			t.Errorf("body = %s, want %s", body, c.ExpectedBody)
		}
		for _, want := range c.ExpectedContent {
			if !strings.Contains(body, want) {
				t.Errorf("body does not contain %q; body: %s", want, body)
			}
		}
	})
}

func (c Case) send(t *testing.T, a *App) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), c.Method, a.URL+c.Path, strings.NewReader(c.Body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for name, value := range c.Headers {
		req.Header.Set(name, value)
	}

	res, err := a.Client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	return res, string(body)
}
