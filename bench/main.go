//go:build unix

// Command bench verifies the throughput assumption of ADR 0001. It starts a
// real server built with the bench tag, sends Events to its benchmark
// endpoint at a fixed rate, and reports the accepted Events/s and the ingest
// latency. It exits with status 1 when the run misses the target.
//
// It runs on Unix only: it stops the server through its process group.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	benchPath       = "/api/bench/events"
	healthPath      = "/api/health"
	listeningMsg    = "http server listening"
	startTimeout    = 30 * time.Second
	requestTimeout  = 10 * time.Second
	shutdownTimeout = 30 * time.Second
	// logsDelay bounds the wait for the server logs to end once it exits.
	logsDelay = 5 * time.Second
)

// options are the command-line flags.
type options struct {
	server    string
	rate      float64
	duration  time.Duration
	warmup    time.Duration
	eventSize int
	p99       time.Duration
}

// logWatch reads the JSON logs of the server and sends the address of its
// listening record on found, once.
type logWatch struct {
	found chan<- string
	line  []byte
	sent  bool
}

// Write never fails, so the server never blocks or breaks writing logs.
func (l *logWatch) Write(p []byte) (int, error) {
	if l.sent {
		return len(p), nil
	}
	l.line = append(l.line, p...)
	for {
		end := bytes.IndexByte(l.line, '\n')
		if end < 0 {
			return len(p), nil
		}
		var record struct {
			Msg  string `json:"msg"`
			Addr string `json:"addr"`
		}
		if json.Unmarshal(l.line[:end], &record) == nil && record.Msg == listeningMsg {
			l.found <- record.Addr
			l.sent = true
			l.line = nil
			return len(p), nil
		}
		l.line = l.line[end+1:]
	}
}

// server is a running server process, the leader of its own process group so
// that stopping it also stops any process the -server command forks.
type server struct {
	cmd *exec.Cmd
	// exited is closed once the process has exited, with its error in err.
	exited chan struct{}
	err    error
}

// stop asks the server to shut down and waits for it to exit, killing it
// after shutdownTimeout. It returns nil if the server exited cleanly.
func (s *server) stop() error {
	select {
	case <-s.exited:
		return s.exitErr()
	default:
	}

	pgid := s.cmd.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop server: %w", err)
	}
	select {
	case <-s.exited:
		// A wrapper that does not catch SIGTERM dies of it while the server
		// shuts down; that is a clean stop too.
		if status, ok := s.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGTERM {
			return nil
		}
		return s.exitErr()
	case <-time.After(shutdownTimeout):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-s.exited
		return errors.New("timed out waiting for the server to shut down; killed it")
	}
}

// kill stops the server at once and waits for it to exit.
func (s *server) kill() {
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	<-s.exited
}

func (s *server) exitErr() error {
	if s.err != nil {
		return fmt.Errorf("server exited: %w", s.err)
	}
	return nil
}

// result is the outcome of one load run against the server.
type result struct {
	client  *http.Client
	url     string
	event   []byte
	elapsed time.Duration

	mu        sync.Mutex
	latencies []time.Duration
	failed    int
	// reasons counts the failed Events by the error or response they got.
	reasons map[string]int
}

// send posts the Event and records its latency from due, or its failure.
func (r *result) send(due time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(r.event))
	if err != nil {
		r.fail(err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := r.client.Do(req)
	if err != nil {
		r.fail(err.Error())
		return
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	latency := time.Since(due)
	if res.StatusCode != http.StatusAccepted {
		r.fail(fmt.Sprintf("%d %s", res.StatusCode, strings.TrimSpace(string(body))))
		return
	}

	r.mu.Lock()
	r.latencies = append(r.latencies, latency)
	r.mu.Unlock()
}

func (r *result) fail(reason string) {
	r.mu.Lock()
	r.failed++
	r.reasons[reason]++
	r.mu.Unlock()
}

func main() {
	var o options
	flag.StringVar(&o.server, "server", "bin/amber-bench", "command that runs the server binary built with the bench tag; serve and its flags are appended")
	flag.Float64Var(&o.rate, "rate", 500, "Events/s to send, and the target to accept")
	flag.DurationVar(&o.duration, "duration", 30*time.Second, "length of the measured run")
	flag.DurationVar(&o.warmup, "warmup", 5*time.Second, "length of the unmeasured run before it")
	flag.IntVar(&o.eventSize, "event-size", 2048, "size in bytes of each Event")
	flag.DurationVar(&o.p99, "p99", 50*time.Millisecond, "target bound on the p99 ingest latency")
	flag.Parse()

	pass, err := benchmark(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if !pass {
		os.Exit(1)
	}
}

// benchmark runs the server, loads it, and prints the report. It reports
// whether the run met the target.
func benchmark(o options) (bool, error) {
	dir, err := os.MkdirTemp("", "amber-bench-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	srv, addr, err := startServer(o.server, dir)
	if err != nil {
		return false, err
	}
	// Stop the server on an early return; the last line stops it otherwise.
	defer func() { _ = srv.stop() }()

	client := &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 4096,
			IdleConnTimeout:     time.Minute,
		},
	}
	base := "http://" + addr
	if err := waitHealthy(client, base); err != nil {
		return false, err
	}

	event := buildEvent(o.eventSize)
	if o.warmup > 0 {
		load(client, base, event, o.rate, o.warmup)
	}
	r := load(client, base, event, o.rate, o.duration)

	target := Target{Rate: o.rate, P99: o.p99}
	summary := Summarize(r.latencies, r.failed, r.elapsed, target)
	if err := printReport(os.Stdout, o, target, summary, r.reasons); err != nil {
		return false, err
	}

	return summary.Pass, srv.stop()
}

// startServer runs command serve over dir on a free local port, in a process
// group of its own, and returns it with the address it listens on.
func startServer(command, dir string) (*server, string, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, "", errors.New("empty -server command")
	}
	args := append(fields[1:], "serve",
		"--http", "127.0.0.1:0",
		"--dir", dir,
		"--log-format", "json",
		"--log-level", "info",
	)
	found := make(chan string, 1)
	cmd := exec.Command(fields[0], args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = &logWatch{found: found}
	cmd.Stderr = os.Stderr
	cmd.WaitDelay = logsDelay
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start server: %w", err)
	}

	srv := &server{cmd: cmd, exited: make(chan struct{})}
	go func() {
		srv.err = cmd.Wait()
		close(srv.exited)
	}()

	select {
	case addr := <-found:
		return srv, addr, nil
	case <-srv.exited:
		return nil, "", fmt.Errorf("server exited before listening: %v", srv.err)
	case <-time.After(startTimeout):
		srv.kill()
		return nil, "", errors.New("timed out waiting for the server to listen")
	}
}

// waitHealthy polls the health endpoint until it answers 200.
func waitHealthy(client *http.Client, base string) error {
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		res, err := client.Get(base + healthPath)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("timed out waiting for the server to be healthy")
}

// load sends event at rate for duration without waiting for responses, so a
// slow server cannot slow the load. Each latency counts from the moment its
// request was due, not from when it was sent.
func load(client *http.Client, base string, event []byte, rate float64, duration time.Duration) *result {
	r := &result{client: client, url: base + benchPath, event: event, reasons: map[string]int{}}
	interval := time.Duration(float64(time.Second) / rate)
	total := int(duration / interval)

	var wg sync.WaitGroup
	start := time.Now()
	for i := range total {
		due := start.Add(time.Duration(i) * interval)
		time.Sleep(time.Until(due))
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.send(due)
		}()
	}
	wg.Wait()
	r.elapsed = time.Since(start)

	return r
}

// buildEvent returns a JSON Event of about size bytes, shaped like one an SDK
// sends.
func buildEvent(size int) []byte {
	event := map[string]any{
		"event_id":    "00000000000000000000000000000000",
		"timestamp":   "2026-01-01T00:00:00Z",
		"level":       "error",
		"release":     "checkout@1.0.0",
		"environment": "production",
		"exception": map[string]any{
			"type":  "TypeError",
			"value": "Cannot read properties of undefined (reading 'id')",
		},
		"stacktrace": "",
	}
	base, _ := json.Marshal(event)
	if pad := size - len(base); pad > 0 {
		frame := "at handler (src/checkout.js:42:7); "
		event["stacktrace"] = strings.Repeat(frame, pad/len(frame)+1)[:pad]
	}
	payload, _ := json.Marshal(event)
	return payload
}

// printReport writes the report of a run to w as Markdown.
func printReport(w io.Writer, o options, target Target, s Summary, reasons map[string]int) error {
	var b strings.Builder
	verdict := "PASS"
	if !s.Pass {
		verdict = "FAIL: the assumption of ADR 0001 does not hold; reopen the ADR instead of tuning silently"
	}

	fmt.Fprintf(&b, "## Benchmark: queue acceptance path\n\n")
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Target | %.0f Events/s, p99 < %s |\n", target.Rate, target.P99)
	fmt.Fprintf(&b, "| Run | %s at %.0f Events/s after %s warmup, %d-byte Events |\n", o.duration, o.rate, o.warmup, len(buildEvent(o.eventSize)))
	fmt.Fprintf(&b, "| Accepted | %d (%.1f Events/s) |\n", s.Accepted, s.Rate)
	fmt.Fprintf(&b, "| Failed | %d |\n", s.Failed)
	fmt.Fprintf(&b, "| p50 | %s |\n", s.P50.Round(10*time.Microsecond))
	fmt.Fprintf(&b, "| p99 | %s |\n", s.P99.Round(10*time.Microsecond))
	fmt.Fprintf(&b, "| Max | %s |\n", s.Max.Round(10*time.Microsecond))
	fmt.Fprintf(&b, "| Verdict | %s |\n", verdict)
	for reason, n := range reasons {
		fmt.Fprintf(&b, "\n- %d × %s", n, reason)
	}
	if len(reasons) > 0 {
		b.WriteString("\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
}
