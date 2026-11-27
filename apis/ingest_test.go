package apis_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joansgarcia/trace/issues"
	"github.com/joansgarcia/trace/projects"
)

const ingestPath = "/api/ingest/v1/events"

func TestMinimalEventCreatesAnOpenIssue(t *testing.T) {
	a := NewApp(t)
	project, key := createProject(t, a)

	status, body := postEvents(t, a, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError"))

	if status != http.StatusAccepted || strings.TrimSpace(body) != `{"accepted":1}` {
		t.Fatalf("response = %d %s, want 202 {\"accepted\":1}", status, body)
	}
	waitPending(t, a, 0)
	got := listIssues(t, a, project)
	if len(got) != 1 {
		t.Fatalf("issues = %+v, want 1", got)
	}
	want := issues.Issue{
		ID:         got[0].ID,
		ProjectID:  project.ID,
		Status:     issues.StatusOpen,
		Title:      "TypeError: x is undefined",
		Culprit:    "render @ src/app.js",
		EventCount: 1,
	}
	if got[0] != want {
		t.Errorf("issue = %+v, want %+v", got[0], want)
	}
	assertUUID(t, "project id", project.ID)
	assertUUID(t, "issue id", got[0].ID)
}

func TestEventsWithTheSameTypeAndInAppFramesShareAnIssue(t *testing.T) {
	a := NewApp(t)
	project, key := createProject(t, a)
	// The second Event moves the frames to other lines and changes the
	// message, which do not split an Issue.
	moved := strings.NewReplacer(`"lineno": 42`, `"lineno": 50`, "x is undefined", "y is undefined").
		Replace(minimalEvent("0190b7a4-7c1e-7000-8000-000000000002", "TypeError"))

	postAccepted(t, a, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError"))
	postAccepted(t, a, key, moved)
	postAccepted(t, a, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000003", "RangeError"))

	waitPending(t, a, 0)
	got := listIssues(t, a, project)
	if len(got) != 2 || got[0].EventCount != 2 || got[1].EventCount != 1 {
		t.Fatalf("issues = %+v, want a TypeError with 2 Events and a RangeError with 1", got)
	}
	if got[0].Title != "TypeError: x is undefined" {
		t.Errorf("title = %q, want the one of the first Event", got[0].Title)
	}
}

func TestResentEventIsAcceptedAndNotCountedAgain(t *testing.T) {
	a := NewApp(t)
	project, key := createProject(t, a)
	event := minimalEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError")

	postAccepted(t, a, key, event)
	waitPending(t, a, 0)
	postAccepted(t, a, key, event)
	waitPending(t, a, 0)

	got := listIssues(t, a, project)
	if len(got) != 1 || got[0].EventCount != 1 {
		t.Errorf("issues = %+v, want 1 with 1 Event", got)
	}
}

func TestIngestKeyIsRequiredAndValid(t *testing.T) {
	event := minimalEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError")
	cases := []struct {
		name string
		key  func(t *testing.T, a *App) string
		code string
	}{
		{
			name: "missing key",
			key:  func(*testing.T, *App) string { return "" },
			code: "ingest_key_missing",
		},
		{
			name: "unknown key",
			key: func(t *testing.T, a *App) string {
				createProject(t, a)
				return "not-a-key"
			},
			code: "ingest_key_invalid",
		},
		{
			name: "revoked key",
			key: func(t *testing.T, a *App) string {
				_, key := createProject(t, a)
				if err := a.Projects().Revoke(t.Context(), key); err != nil {
					t.Fatalf("revoke key: %v", err)
				}
				return key
			},
			code: "ingest_key_invalid",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := NewApp(t)

			status, body := postEvents(t, a, c.key(t, a), event)

			want := `{"error":{"code":"` + c.code + `",`
			if status != http.StatusUnauthorized || !strings.HasPrefix(body, want) {
				t.Errorf("response = %d %s, want 401 %s...", status, body, want)
			}
		})
	}
}

func TestRequestWithAnInvalidKeyIsRejectedWithoutReadingTheBody(t *testing.T) {
	a := NewApp(t)
	// The body never ends, so the response only arrives if the server
	// answers without reading it.
	body, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, a.URL+ingestPath, body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Trace-Ingest-Key", "not-a-key")

	res, err := a.Client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	_ = res.Body.Close()

	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

func TestIngestKeysAreResolvedFromMemory(t *testing.T) {
	a := NewApp(t)
	_, key := createProject(t, a)
	postAccepted(t, a, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError"))

	// Without data.db, a key resolved from it would fail.
	if _, err := a.Store().Data.Read.Exec("DELETE FROM ingest_keys"); err != nil {
		t.Fatalf("delete keys: %v", err)
	}

	postAccepted(t, a, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000002", "TypeError"))
}

func TestHubSubscriberIsNotifiedAfterTheIssueIsCommitted(t *testing.T) {
	a := NewApp(t)
	project, key := createProject(t, a)
	changes, unsubscribe := a.Hub().Subscribe()
	defer unsubscribe()

	postAccepted(t, a, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError"))

	select {
	case <-changes:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a notification")
	}
	if got := listIssues(t, a, project); len(got) != 1 {
		t.Errorf("issues = %+v at notification, want 1", got)
	}
}

func TestHubSubscriberIsNotNotifiedForAResentEvent(t *testing.T) {
	a := NewApp(t)
	_, key := createProject(t, a)
	changes, unsubscribe := a.Hub().Subscribe()
	defer unsubscribe()
	event := minimalEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError")
	postAccepted(t, a, key, event)
	select {
	case <-changes:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a notification")
	}

	postAccepted(t, a, key, event)
	// The queue deletes an Event after processing it, so any notification
	// for it is already sent.
	waitPending(t, a, 0)

	select {
	case <-changes:
		t.Error("notified for an Event that changed nothing")
	default:
	}
}

func TestInvalidEventRejectsTheWholeBatch(t *testing.T) {
	a := NewApp(t)
	project, key := createProject(t, a)
	missingSDK := `{"event_id":"0190b7a4-7c1e-7000-8000-000000000002","timestamp":"2026-11-27T10:00:00.000Z","platform":"javascript","exceptions":[{"type":"TypeError"}]}`

	status, body := postEvents(t, a, key, minimalEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError"), missingSDK)

	want := `{"error":{"code":"invalid_event","message":"the batch has invalid Events","details":[{"index":1,"message":"sdk.name and sdk.version are required"}]}}`
	if status != http.StatusBadRequest || strings.TrimSpace(body) != want {
		t.Errorf("response = %d %s, want 400 %s", status, body, want)
	}
	if got := a.Queue().Pending(); got != 0 {
		t.Errorf("pending = %d, want 0", got)
	}
	if got := listIssues(t, a, project); len(got) != 0 {
		t.Errorf("issues = %+v, want none", got)
	}
}

// postAccepted sends events with key and fails unless they are accepted.
func postAccepted(t *testing.T, a *App, key string, events ...string) {
	t.Helper()

	status, body := postEvents(t, a, key, events...)
	want := fmt.Sprintf(`{"accepted":%d}`, len(events))
	if status != http.StatusAccepted || strings.TrimSpace(body) != want {
		t.Fatalf("response = %d %s, want 202 %s", status, body, want)
	}
}

// assertUUID fails unless id, the id of what, is a UUID, so ids are not
// sequential.
func assertUUID(t *testing.T, what, id string) {
	t.Helper()

	if _, err := uuid.Parse(id); err != nil {
		t.Errorf("%s = %q, want a UUID", what, id)
	}
}

// createProject creates a Project in the app and returns it with its Ingest
// Key.
func createProject(t *testing.T, a *App) (projects.Project, string) {
	t.Helper()

	project, key, err := a.Projects().Create(t.Context(), "checkout")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project, key
}

// postEvents sends a batch of events, JSON objects, to the ingest endpoint
// with key, if not empty, and returns the status and body of the response.
func postEvents(t *testing.T, a *App, key string, events ...string) (int, string) {
	t.Helper()

	body := `{"events":[` + strings.Join(events, ",") + `]}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, a.URL+ingestPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-Trace-Ingest-Key", key)
	}

	res, err := a.Client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res.StatusCode, string(got)
}

// minimalEvent returns an Event with only the required fields, one
// exception of type errType and an In-app frame after a dependency frame.
func minimalEvent(id, errType string) string {
	return fmt.Sprintf(`{
		"event_id": %q,
		"timestamp": "2026-11-27T10:00:00.000Z",
		"platform": "javascript",
		"sdk": {"name": "trace-js", "version": "0.1.0"},
		"exceptions": [{
			"type": %q,
			"message": "x is undefined",
			"frames": [
				{"function": "dispatch", "filename": "node_modules/react/index.js", "lineno": 10, "colno": 2, "in_app": false},
				{"function": "render", "filename": "src/app.js", "lineno": 42, "colno": 7, "in_app": true}
			]
		}]
	}`, id, errType)
}

func listIssues(t *testing.T, a *App, project projects.Project) []issues.Issue {
	t.Helper()

	got, err := issues.List(t.Context(), a.Store().Data, project.ID)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	return got
}
