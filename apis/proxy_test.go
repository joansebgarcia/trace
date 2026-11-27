package apis_test

import (
	"net/http"
	"testing"
)

func TestTrustedProxies(t *testing.T) {
	forwarded := http.Header{"X-Forwarded-For": {"203.0.113.7"}, "X-Forwarded-Proto": {"https"}}

	tests := []struct {
		name       string
		proxies    []string
		headers    http.Header
		wantIP     string
		wantScheme string
	}{
		{
			name:       "without trusted proxies the headers are ignored",
			headers:    forwarded,
			wantIP:     "127.0.0.1",
			wantScheme: "http",
		},
		{
			name:       "from a trusted proxy the headers resolve the client",
			proxies:    []string{"127.0.0.0/8"},
			headers:    forwarded,
			wantIP:     "203.0.113.7",
			wantScheme: "https",
		},
		{
			name:       "from outside the trusted proxies the headers are ignored",
			proxies:    []string{"10.0.0.0/8"},
			headers:    forwarded,
			wantIP:     "127.0.0.1",
			wantScheme: "http",
		},
		{
			name:       "hops a client prepends cannot spoof it",
			proxies:    []string{"127.0.0.0/8", "10.0.0.0/8"},
			headers:    http.Header{"X-Forwarded-For": {"198.51.100.1, 203.0.113.7, 10.0.0.2"}},
			wantIP:     "203.0.113.7",
			wantScheme: "http",
		},
		{
			name:       "a chain of trusted proxies resolves the first hop",
			proxies:    []string{"127.0.0.0/8", "10.0.0.0/8"},
			headers:    http.Header{"X-Forwarded-For": {"10.0.0.3, 10.0.0.2"}},
			wantIP:     "10.0.0.3",
			wantScheme: "http",
		},
		{
			name:       "several X-Forwarded-For lines read as one list",
			proxies:    []string{"127.0.0.0/8", "10.0.0.0/8"},
			headers:    http.Header{"X-Forwarded-For": {"203.0.113.7", "10.0.0.2"}},
			wantIP:     "203.0.113.7",
			wantScheme: "http",
		},
		{
			name:       "an IPv6 client resolves",
			proxies:    []string{"127.0.0.0/8"},
			headers:    http.Header{"X-Forwarded-For": {"2001:db8::7"}},
			wantIP:     "2001:db8::7",
			wantScheme: "http",
		},
		{
			name:       "a malformed hop stops the walk at the last trusted hop",
			proxies:    []string{"127.0.0.0/8", "10.0.0.0/8"},
			headers:    http.Header{"X-Forwarded-For": {"203.0.113.7, unknown, 10.0.0.2"}},
			wantIP:     "10.0.0.2",
			wantScheme: "http",
		},
		{
			name:       "the scheme comes from the nearest proxy",
			proxies:    []string{"127.0.0.0/8"},
			headers:    http.Header{"X-Forwarded-For": {"203.0.113.7"}, "X-Forwarded-Proto": {"http", "https"}},
			wantIP:     "203.0.113.7",
			wantScheme: "https",
		},
		{
			name:       "a scheme other than http or https is ignored",
			proxies:    []string{"127.0.0.0/8"},
			headers:    http.Header{"X-Forwarded-For": {"203.0.113.7"}, "X-Forwarded-Proto": {"ftp"}},
			wantIP:     "203.0.113.7",
			wantScheme: "http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &Logs{}
			a, err := StartApp(t, t.TempDir(), WithLogs(logs), WithTrustedProxies(t, tt.proxies...))
			if err != nil {
				t.Fatalf("start app: %v", err)
			}

			sendHealth(t, a, tt.headers)

			record := logs.Wait(t, "http request")
			if got := record["client_ip"]; got != tt.wantIP {
				t.Errorf("client_ip = %v, want %s", got, tt.wantIP)
			}
			if got := record["scheme"]; got != tt.wantScheme {
				t.Errorf("scheme = %v, want %s", got, tt.wantScheme)
			}
		})
	}
}

// sendHealth sends GET /api/health with headers and fails unless it answers
// 200.
func sendHealth(t *testing.T, a *App, headers http.Header) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, a.URL+"/api/health", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header = headers.Clone()

	res, err := a.Client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
}
