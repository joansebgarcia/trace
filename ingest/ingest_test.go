package ingest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joansgarcia/trace/ingest"
	"github.com/joansgarcia/trace/issues"
	"github.com/joansgarcia/trace/migrations"
	"github.com/joansgarcia/trace/projects"
	"github.com/joansgarcia/trace/queue"
	"github.com/joansgarcia/trace/store"
)

const (
	timeout = 5 * time.Second
	// fixturesDir holds the fixtures of the protocol contract.
	fixturesDir = "../docs/protocol/v1/fixtures"
)

// fixture is one request body of the protocol contract. An invalid one
// carries the error code it must get and, if one Event breaks the protocol,
// its index.
type fixture struct {
	Name  string          `json:"-"`
	Body  json.RawMessage `json:"body"`
	Code  string          `json:"code"`
	Index *int            `json:"index"`
}

// endpoint is the ingest endpoint over a migrated data directory.
type endpoint struct {
	URL      string
	Client   *http.Client
	Projects *projects.Projects
	Queue    *queue.Queue
	Store    *store.Store
	Data     *store.DB
}

// request is one request to the endpoint. Empty fields take a default: POST,
// application/json and no encoding.
type request struct {
	Method  string
	Query   string
	Headers map[string]string
	Body    io.Reader
}

func TestAcceptedEventIsProcessedIntoAnIssue(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)

	status, body := postEvents(t, e, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000001"))

	if status != http.StatusAccepted || strings.TrimSpace(body) != `{"accepted":1}` {
		t.Fatalf("response = %d %s, want 202 {\"accepted\":1}", status, body)
	}
	waitPending(t, e.Queue, 0)
	got, err := issues.List(t.Context(), e.Data, project.ID)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	if len(got) != 1 || got[0].EventCount != 1 {
		t.Errorf("issues = %+v, want 1 with 1 Event", got)
	}
}

func TestIngestKeyIsRequiredAndValid(t *testing.T) {
	event := minimalEvent("0190b7a4-7c1e-7000-8000-000000000001")
	cases := []struct {
		name string
		key  func(t *testing.T, e *endpoint) string
		code string
	}{
		{
			name: "missing key",
			key:  func(*testing.T, *endpoint) string { return "" },
			code: "ingest_key_missing",
		},
		{
			name: "unknown key",
			key: func(t *testing.T, e *endpoint) string {
				createProject(t, e)
				return "not-a-key"
			},
			code: "ingest_key_invalid",
		},
		{
			name: "revoked key",
			key: func(t *testing.T, e *endpoint) string {
				_, key := createProject(t, e)
				if err := e.Projects.Revoke(t.Context(), key); err != nil {
					t.Fatalf("revoke key: %v", err)
				}
				return key
			},
			code: "ingest_key_invalid",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEndpoint(t)

			status, body := postEvents(t, e, c.key(t, e), event)

			want := `{"error":{"code":"` + c.code + `",`
			if status != http.StatusUnauthorized || !strings.HasPrefix(body, want) {
				t.Errorf("response = %d %s, want 401 %s...", status, body, want)
			}
		})
	}
}

func TestRequestWithAnInvalidKeyIsRejectedWithoutReadingTheBody(t *testing.T) {
	e := newEndpoint(t)
	// The body never ends, so the response only arrives if the server
	// answers without reading it.
	body, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.URL, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Trace-Ingest-Key", "not-a-key")

	res, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	_ = res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

func TestInvalidEventRejectsTheWholeBatch(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)
	missingSDK := `{"event_id":"0190b7a4-7c1e-7000-8000-000000000002","timestamp":"2026-11-27T10:00:00.000Z","platform":"javascript","exceptions":[{"type":"TypeError"}]}`

	status, body := postEvents(t, e, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000001"), missingSDK)

	want := `{"error":{"code":"invalid_event","message":"the batch has invalid Events","details":[{"index":1,"message":"sdk.name and sdk.version are required"}]}}`
	if status != http.StatusBadRequest || strings.TrimSpace(body) != want {
		t.Errorf("response = %d %s, want 400 %s", status, body, want)
	}
	if got := e.Queue.Pending(); got != 0 {
		t.Errorf("pending = %d, want 0", got)
	}
}

func TestValidFixturesAreAccepted(t *testing.T) {
	for _, f := range loadFixtures(t, "valid") {
		t.Run(f.Name, func(t *testing.T) {
			e := newEndpoint(t)
			_, key := createProject(t, e)

			status, body := postBody(t, e, key, string(f.Body))

			if status != http.StatusAccepted {
				t.Errorf("response = %d %s, want 202", status, body)
			}
		})
	}
}

func TestInvalidFixturesAreRejectedWithTheirCode(t *testing.T) {
	for _, f := range loadFixtures(t, "invalid") {
		t.Run(f.Name, func(t *testing.T) {
			e := newEndpoint(t)
			_, key := createProject(t, e)

			status, body := postBody(t, e, key, string(f.Body))

			var got struct {
				Error struct {
					Code    string `json:"code"`
					Details []struct {
						Index int `json:"index"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("response = %d %s, want a JSON error", status, body)
			}
			if status != http.StatusBadRequest || got.Error.Code != f.Code {
				t.Errorf("response = %d %s, want 400 %s", status, body, f.Code)
			}
			var indexes []int
			for _, d := range got.Error.Details {
				indexes = append(indexes, d.Index)
			}
			var want []int
			if f.Index != nil {
				want = []int{*f.Index}
			}
			if !reflect.DeepEqual(indexes, want) {
				t.Errorf("details indexes = %v, want %v; body: %s", indexes, want, body)
			}
			if got := e.Queue.Pending(); got != 0 {
				t.Errorf("pending = %d, want 0", got)
			}
		})
	}
}

// option changes how newEndpoint sets up the endpoint.
type option func(*setup)

// setup is what newEndpoint builds the endpoint from.
type setup struct {
	process bool
	config  ingest.Config
	logs    io.Writer
}

// withProcess makes the queue process Events into Issues; without it, they
// stay queued.
func withProcess() option {
	return func(s *setup) { s.process = true }
}

// withRateLimit sets the default rate limit, in Events/s, of every Project.
func withRateLimit(rate float64) option {
	return func(s *setup) { s.config.RateLimit = rate }
}

// withMaxPending sets the largest backlog of the queue.
func withMaxPending(n int) option {
	return func(s *setup) { s.config.MaxPending = n }
}

// withLogs makes the endpoint write its logs to w as JSON records, and report
// its rejections every interval.
func withLogs(w io.Writer, interval time.Duration) option {
	return func(s *setup) {
		s.logs = w
		s.config.ReportInterval = interval
	}
}

// newEndpoint serves the ingest endpoint until the test ends.
func newEndpoint(t *testing.T, options ...option) *endpoint {
	t.Helper()

	var o setup
	for _, option := range options {
		option(&o)
	}
	logger := slog.New(slog.DiscardHandler)
	if o.logs != nil {
		logger = slog.New(slog.NewJSONHandler(o.logs, nil))
	}

	s := openStore(t)
	config := queue.Config{DB: s.Ingest, Logger: logger}
	if o.process {
		p := ingest.NewProcess(logger)
		config.Process = func(ctx context.Context, event []byte) error {
			_, err := p(ctx, s.Data, event)
			return err
		}
	}
	q, err := queue.New(config)
	if err != nil {
		t.Fatalf("start queue: %v", err)
	}
	t.Cleanup(func() { _ = q.Close(context.Background()) })

	p := projects.New(s.Data)
	o.config.Projects = p
	o.config.Queue = q
	o.config.Logger = logger
	handler := ingest.NewHandler(o.config)
	t.Cleanup(handler.Close)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &endpoint{
		URL:      server.URL + ingest.Path,
		Client:   &http.Client{Timeout: timeout},
		Projects: p,
		Queue:    q,
		Store:    s,
		Data:     s.Data,
	}
}

// createProject creates a Project and returns it with its Ingest Key.
func createProject(t *testing.T, e *endpoint) (projects.Project, string) {
	t.Helper()

	project, key, err := e.Projects.Create(t.Context(), "checkout")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project, key
}

// postEvents sends a batch of events, JSON objects, with key, if not empty,
// and returns the status and body of the response.
func postEvents(t *testing.T, e *endpoint, key string, events ...string) (int, string) {
	t.Helper()

	return postBody(t, e, key, `{"events":[`+strings.Join(events, ",")+`]}`)
}

// postBody sends body with key, if not empty, and returns the status and
// body of the response.
func postBody(t *testing.T, e *endpoint, key, body string) (int, string) {
	t.Helper()

	headers := map[string]string{}
	if key != "" {
		headers["X-Trace-Ingest-Key"] = key
	}
	res, got := send(t, e, request{Headers: headers, Body: strings.NewReader(body)})
	return res.StatusCode, got
}

// send sends r to the endpoint and returns the response with its body read.
func send(t *testing.T, e *endpoint, r request) (*http.Response, string) {
	t.Helper()

	method := r.Method
	if method == "" {
		method = http.MethodPost
	}
	url := e.URL
	if r.Query != "" {
		url += "?" + r.Query
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, r.Body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range r.Headers {
		if value == "" {
			req.Header.Del(name)
			continue
		}
		req.Header.Set(name, value)
	}

	res, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, string(got)
}

func waitPending(t *testing.T, q *queue.Queue, want int) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for q.Pending() != want {
		if time.Now().After(deadline) {
			t.Fatalf("pending = %d, want %d", q.Pending(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// minimalEvent returns an Event with only the required fields and one
// exception.
func minimalEvent(id string) string {
	return fmt.Sprintf(`{
		"event_id": %q,
		"timestamp": "2026-11-27T10:00:00.000Z",
		"platform": "javascript",
		"sdk": {"name": "trace-js", "version": "0.1.0"},
		"exceptions": [{"type": "TypeError", "message": "x is undefined"}]
	}`, id)
}

// loadFixtures loads the fixtures of the protocol contract in kind, "valid"
// or "invalid".
func loadFixtures(t *testing.T, kind string) []fixture {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(fixturesDir, kind, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("find %s fixtures: %v", kind, err)
	}
	fixtures := make([]fixture, len(paths))
	for i, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		if err := json.Unmarshal(content, &fixtures[i]); err != nil {
			t.Fatalf("decode fixture %s: %v", path, err)
		}
		fixtures[i].Name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	return fixtures
}

// openStore opens a temporary data directory with the migrations built into
// the binary applied. It closes when the test ends.
func openStore(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if err := s.Migrate(t.Context(), schema); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// eventID returns the n-th event_id of a test.
func eventID(n int) string {
	return fmt.Sprintf("0190b7a4-7c1e-7000-8000-%012d", n)
}

// assertError fails unless res is an error response with status and code.
func assertError(t *testing.T, res *http.Response, body string, status int, code string) {
	t.Helper()

	var got struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || res.StatusCode != status || got.Error.Code != code {
		t.Errorf("response = %d %s, want %d %s", res.StatusCode, body, status, code)
	}
}

func listIssues(t *testing.T, e *endpoint, projectID string) []issues.Issue {
	t.Helper()

	got, err := issues.List(t.Context(), e.Data, projectID)
	if err != nil {
		t.Fatalf("list Issues: %v", err)
	}
	return got
}
