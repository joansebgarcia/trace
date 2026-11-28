package ingest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/ingest"
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

func TestKeyNamesAreMatchedIgnoringCaseDashesAndUnderscores(t *testing.T) {
	cases := []struct {
		key       string
		sensitive bool
	}{
		{"X-Api-Key", true},
		{"user_password", true},
		{"PASSWD", true},
		{"set-cookie", true},
		{"x_csrf_token", true},
		{"Private-Key", true},
		{"author", true}, // a false positive the rules accept
		{"plan", false},
		{"email", false},
		{"api", false},
	}

	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got := ingest.Redact(event.Event{Tags: map[string]string{c.key: "value"}}, nil)

			if redacted := got.Tags[c.key] == ingest.RedactionMarker; redacted != c.sensitive {
				t.Errorf("tag %s = %q, want redacted %t", c.key, got.Tags[c.key], c.sensitive)
			}
		})
	}
}

func TestFragmentsAreRedactedInMessages(t *testing.T) {
	cases := []struct {
		name, message, want string
	}{
		{"card passing Luhn", "card 4111111111111111 declined", "card [redacted] declined"},
		{"card grouped by spaces", "card 4111 1111 1111 1111 declined", "card [redacted] declined"},
		{"card grouped by dashes", "card 5555-5555-5555-4444", "card [redacted]"},
		{"number failing Luhn", "order 4111111111111112", "order 4111111111111112"},
		{"number too short", "order 411111111111", "order 411111111111"},
		{"Bearer token", "header Bearer abc.DEF-123=", "header Bearer [redacted]"},
		{"JWT", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln here", "token [redacted] here"},
		{"userinfo", "GET https://ann:pa55@api.example.com/x", "GET https://[redacted]@api.example.com/x"},
		{"user without password", "GET https://ann@api.example.com/x", "GET https://ann@api.example.com/x"},
		{"email and IP", "ann@example.com from 10.0.0.1", "ann@example.com from 10.0.0.1"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := event.Event{Exceptions: []event.Exception{{Type: "Error", Message: c.message}}}

			got := ingest.Redact(e, nil)

			if got.Exceptions[0].Message != c.want {
				t.Errorf("message = %q, want %q", got.Exceptions[0].Message, c.want)
			}
		})
	}
}

func TestSensitiveQueryParametersAreRedacted(t *testing.T) {
	e := event.Event{Request: &event.Request{URL: "https://shop.example.com/a?Access_Token=x&page=2&pwd=&q=%20#token=y"}}

	got := ingest.Redact(e, []string{"q"})

	want := "https://shop.example.com/a?Access_Token=[redacted]&page=2&pwd=[redacted]&q=[redacted]#token=y"
	if got.Request.URL != want {
		t.Errorf("url = %q, want %q", got.Request.URL, want)
	}
}

func TestProtectedFieldsAreNeverRedacted(t *testing.T) {
	inApp := true
	e := event.Event{
		EventID:     "0190b7a4-7c1e-7000-8000-000000000001",
		Platform:    "token",
		SDK:         event.SDK{Name: "secret", Version: "4111111111111111"},
		Level:       event.LevelError,
		Environment: "password",
		Release:     "Bearer abc",
		GroupKey:    []string{"4111111111111111", "Bearer abc"},
		Exceptions: []event.Exception{{
			Type:   "Bearer abc",
			Frames: []event.Frame{{Function: "Bearer abc", Filename: "https://ann:pw@x.com/a.js?token=1", InApp: &inApp}},
		}},
	}

	got := ingest.Redact(e, nil)

	if got.Platform != e.Platform || got.SDK != e.SDK || got.Environment != e.Environment || got.Release != e.Release {
		t.Errorf("Event = %+v, want platform, sdk, environment and release as sent", got)
	}
	if got.GroupKey[0] != e.GroupKey[0] || got.GroupKey[1] != e.GroupKey[1] {
		t.Errorf("group_key = %v, want %v", got.GroupKey, e.GroupKey)
	}
	if got.Exceptions[0].Type != "Bearer abc" || got.Exceptions[0].Frames[0] != e.Exceptions[0].Frames[0] {
		t.Errorf("exception = %+v, want type and frames as sent", got.Exceptions[0])
	}
}

func TestRedactLeavesItsInputAsItIs(t *testing.T) {
	e := event.Event{
		Tags:        map[string]string{"token": "t"},
		Exceptions:  []event.Exception{{Type: "Error", Message: "Bearer abc"}},
		Breadcrumbs: []event.Breadcrumb{{Message: "Bearer abc"}},
		Request:     &event.Request{URL: "https://a.com/?token=t"},
	}

	ingest.Redact(e, nil)

	if e.Tags["token"] != "t" || e.Exceptions[0].Message != "Bearer abc" || e.Breadcrumbs[0].Message != "Bearer abc" || e.Request.URL != "https://a.com/?token=t" {
		t.Errorf("input changed: %+v", e)
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
