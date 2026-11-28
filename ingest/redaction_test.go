package ingest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/issues"
)

// redactionFixture is one Event of the Redaction contract with the Event it
// must be stored as. Terms are the extra key terms of its Project.
type redactionFixture struct {
	Name   string          `json:"-"`
	Terms  []string        `json:"terms"`
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
}

func TestRedactionFixturesAreStoredAsExpected(t *testing.T) {
	for _, f := range loadRedactionFixtures(t) {
		t.Run(f.Name, func(t *testing.T) {
			e := newEndpoint(t, withProcess())
			project, key := createProject(t, e)
			if f.Terms != nil {
				if err := e.Projects.AddRedactionTerms(t.Context(), project.ID, f.Terms...); err != nil {
					t.Fatalf("add terms: %v", err)
				}
			}

			if status, body := postEvents(t, e, key, string(f.Input)); status != 202 {
				t.Fatalf("response = %d %s, want 202", status, body)
			}

			waitPending(t, e.Queue, 0)
			stored := listEvents(t, e, project.ID)
			if len(stored) != 1 {
				t.Fatalf("stored Events = %d, want 1", len(stored))
			}
			got, want := generic(t, stored[0].Event), generic(t, decodeEvent(t, f.Output))
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				t.Errorf("stored Event =\n%s\nwant\n%s", gotJSON, wantJSON)
			}
		})
	}
}

func TestProjectTermsApplyOnlyToNewEvents(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	withRef := func(n int) string {
		return strings.Replace(minimalEvent(eventID(n)), `"platform"`, `"extra": {"order_ref": "A-1"}, "platform"`, 1)
	}
	postEvents(t, e, key, withRef(1))
	waitPending(t, e.Queue, 0)

	if err := e.Projects.AddRedactionTerms(t.Context(), project.ID, "orderref"); err != nil {
		t.Fatalf("add terms: %v", err)
	}
	postEvents(t, e, key, withRef(2))
	waitPending(t, e.Queue, 0)

	stored := listEvents(t, e, project.ID)
	if len(stored) != 2 {
		t.Fatalf("stored Events = %d, want 2", len(stored))
	}
	if got := string(stored[0].Event.Extra); got != `{"order_ref":"A-1"}` {
		t.Errorf("Event stored before the term: extra = %s, want it as sent", got)
	}
	if got := string(stored[1].Event.Extra); got != `{"order_ref":"[redacted]"}` {
		t.Errorf("Event stored after the term: extra = %s, want it redacted", got)
	}
}

func TestQueuedEventIsAlreadyRedacted(t *testing.T) {
	logs := &syncBuffer{}
	e := newEndpoint(t, withLogs(logs, 0))
	_, key := createProject(t, e)
	secrets := []string{"hunter2", "4111 1111 1111 1111", "pa55"}
	sensitive := strings.Replace(minimalEvent(eventID(1)), `"platform"`, `
		"extra": {"password": "hunter2", "note": "card 4111 1111 1111 1111"},
		"request": {"url": "https://ann:pa55@shop.example.com/cart"},
		"platform"`, 1)

	if status, body := postEvents(t, e, key, sensitive); status != 202 {
		t.Fatalf("response = %d %s, want 202", status, body)
	}

	var queued string
	if err := e.Store.Ingest.Read.QueryRow("SELECT CAST(payload AS TEXT) FROM queue").Scan(&queued); err != nil {
		t.Fatalf("read queue: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(queued, secret) {
			t.Errorf("queued Event holds %q: %s", secret, queued)
		}
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs hold %q", secret)
		}
	}
	// The client of the test connects from 127.0.0.1.
	if strings.Contains(queued, "127.0.0.1") {
		t.Errorf("queued Event holds the IP of the client: %s", queued)
	}
}

func listEvents(t *testing.T, e *endpoint, projectID string) []issues.Event {
	t.Helper()

	got, err := issues.ListEvents(t.Context(), e.Data, projectID)
	if err != nil {
		t.Fatalf("list Events: %v", err)
	}
	return got
}

// decodeEvent decodes raw as the server does, with its defaults.
func decodeEvent(t *testing.T, raw json.RawMessage) event.Event {
	t.Helper()

	events, err := event.DecodeBatch([]byte(`{"events":[` + string(raw) + `]}`))
	if err != nil {
		t.Fatalf("decode Event: %v", err)
	}
	return events[0]
}

// generic returns e as decoded JSON, so key order does not count.
func generic(t *testing.T, e event.Event) any {
	t.Helper()

	encoded, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("encode Event: %v", err)
	}
	var v any
	if err := json.Unmarshal(encoded, &v); err != nil {
		t.Fatalf("decode Event: %v", err)
	}
	return v
}

func loadRedactionFixtures(t *testing.T) []redactionFixture {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(fixturesDir, "redaction", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("find Redaction fixtures: %v", err)
	}
	fixtures := make([]redactionFixture, len(paths))
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
