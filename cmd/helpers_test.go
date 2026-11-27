package cmd_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/joansebgarcia/trace/cmd"
)

const timeout = 5 * time.Second

// execute runs a command that returns on its own, such as --help, and
// returns its output.
func execute(args ...string) (string, error) {
	var out bytes.Buffer
	root := cmd.NewRoot("1.2.3")
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	err := root.Execute()

	return out.String(), err
}

// serveHealthy runs serve with args on a free local port, waits until it is
// healthy and returns a function that stops it and returns its output.
func serveHealthy(t *testing.T, args ...string) (stop func() string) {
	t.Helper()

	addr := findFreeAddr(t)
	stop = serve(t, append([]string{"serve", "--http=" + addr}, args...)...)
	waitHealthy(t, addr)

	return stop
}

// serve runs the command with args until the test ends and returns a function
// that stops it and returns its output. It runs inside a temporary working
// directory, so the default data directory is temporary too.
func serve(t *testing.T, args ...string) (stop func() string) {
	t.Helper()
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	root := cmd.NewRoot("test")
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	stopped := false
	stop = func() string {
		t.Helper()
		if !stopped {
			stopped = true
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("serve returned error: %v\noutput:\n%s", err, out.String())
				}
			case <-time.After(timeout):
				t.Fatal("serve did not stop")
			}
		}
		return out.String()
	}
	t.Cleanup(func() { stop() })

	return stop
}

// waitHealthy polls GET /api/health on addr until it answers 200.
func waitHealthy(t *testing.T, addr string) {
	t.Helper()

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := client.Get("http://" + addr + "/api/health")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no healthy server on %s", addr)
}

// findFreeAddr returns a local TCP address with a port that is free right now.
func findFreeAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer func() { _ = listener.Close() }()

	return listener.Addr().String()
}

func splitLogLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
