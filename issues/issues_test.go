package issues_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/hub"
	"github.com/joansgarcia/trace/issues"
	"github.com/joansgarcia/trace/migrations"
	"github.com/joansgarcia/trace/projects"
	"github.com/joansgarcia/trace/store"
)

func TestFirstEventCreatesAnOpenIssue(t *testing.T) {
	data := openStore(t).Data
	projectID := createProject(t, data)

	record(t, data, projectID, newEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError", "x is undefined"))

	got := listIssues(t, data, projectID)
	if len(got) != 1 {
		t.Fatalf("issues = %+v, want 1", got)
	}
	want := issues.Issue{
		ID:              got[0].ID,
		ProjectID:       projectID,
		Status:          issues.StatusOpen,
		Title:           "TypeError: x is undefined",
		Culprit:         "render @ src/app.js",
		Level:           event.LevelError,
		EventCount:      1,
		FirstSeen:       receivedAt,
		LastSeen:        receivedAt,
		StatusChangedAt: receivedAt,
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("issue = %+v, want %+v", got[0], want)
	}
	if _, err := uuid.Parse(got[0].ID); err != nil {
		t.Errorf("issue id = %q, want a UUID", got[0].ID)
	}
}

func TestEventsWithTheSameGroupKeyShareAnIssue(t *testing.T) {
	data := openStore(t).Data
	projectID := createProject(t, data)

	record(t, data, projectID, newEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError", "x is undefined"))
	record(t, data, projectID, newEvent("0190b7a4-7c1e-7000-8000-000000000002", "TypeError", "y is undefined"))
	record(t, data, projectID, newEvent("0190b7a4-7c1e-7000-8000-000000000003", "RangeError", "x is undefined"))

	got := listIssues(t, data, projectID)
	if len(got) != 2 || got[0].EventCount != 2 || got[1].EventCount != 1 {
		t.Fatalf("issues = %+v, want a TypeError with 2 Events and a RangeError with 1", got)
	}
	if got[0].Title != "TypeError: x is undefined" {
		t.Errorf("title = %q, want the one of the first Event", got[0].Title)
	}
}

func TestResentEventIsNotCountedAgain(t *testing.T) {
	data := openStore(t).Data
	projectID := createProject(t, data)
	e := newEvent("0190b7a4-7c1e-7000-8000-000000000001", "TypeError", "x is undefined")
	record(t, data, projectID, e)

	change, err := issues.Record(t.Context(), data, projectID, receivedAt, e)
	if err != nil {
		t.Fatalf("record again: %v", err)
	}

	if change != (hub.Change{}) {
		t.Errorf("change = %+v, want none for a resent Event", change)
	}
	if got := listIssues(t, data, projectID); len(got) != 1 || got[0].EventCount != 1 {
		t.Errorf("issues = %+v, want 1 with 1 Event", got)
	}
}

// receivedAt is when the tests receive their Events.
var receivedAt = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

func createProject(t *testing.T, data *store.DB) string {
	t.Helper()

	project, _, err := projects.New(data).Create(t.Context(), "checkout")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project.ID
}

// record stores e and fails unless it is stored.
func record(t *testing.T, data *store.DB, projectID string, e event.Event) {
	t.Helper()

	change, err := issues.Record(t.Context(), data, projectID, receivedAt, e)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if change.Kind == "" {
		t.Fatalf("change = none, want the Event stored")
	}
}

func listIssues(t *testing.T, data *store.DB, projectID string) []issues.Issue {
	t.Helper()

	got, err := issues.List(t.Context(), data, projectID)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}
	return got
}

// newEvent returns an Event with one exception of type errType and an In-app
// frame after a dependency frame.
func newEvent(id, errType, message string) event.Event {
	inApp, dependency := true, false
	return event.Event{
		EventID:   id,
		Timestamp: "2026-11-27T10:00:00.000Z",
		Platform:  "javascript",
		Level:     event.LevelError,
		SDK:       event.SDK{Name: "trace-js", Version: "0.1.0"},
		Exceptions: []event.Exception{{
			Type:    errType,
			Message: message,
			Frames: []event.Frame{
				{Function: "dispatch", Filename: "node_modules/react/index.js", Lineno: 10, InApp: &dependency},
				{Function: "render", Filename: "src/app.js", Lineno: 42, InApp: &inApp},
			},
		}},
	}
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
