package ingest_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The frames of the grouping tests: f builds one from its function,
// filename, line and whether it is an In-app frame.
func f(function, filename string, lineno int, inApp bool) string {
	return fmt.Sprintf(`{"function": %q, "filename": %q, "lineno": %d, "colno": 7, "in_app": %t}`, function, filename, lineno, inApp)
}

// noInApp builds a frame the SDK did not mark.
func noInApp(function, filename string, lineno int) string {
	return fmt.Sprintf(`{"function": %q, "filename": %q, "lineno": %d}`, function, filename, lineno)
}

// exc builds an exception of type errType with message and frames.
func exc(errType, message string, frames ...string) string {
	return fmt.Sprintf(`{"type": %q, "message": %q, "frames": [%s]}`, errType, message, strings.Join(frames, ","))
}

func TestEquivalentEventsShareAnIssue(t *testing.T) {
	dependency := f("dispatch", "https://cdn.example.com/node_modules/react/index.js", 10, false)
	cases := []struct {
		name          string
		first, second string
	}{
		{
			name:   "another line, column and message",
			first:  eventWith(1, exc("TypeError", "x is undefined", dependency, f("render", "src/app.js", 42, true))),
			second: eventWith(2, exc("TypeError", "y is undefined", dependency, f("render", "src/app.js", 50, true))),
		},
		{
			name:   "another bundler hash, host and query",
			first:  eventWith(1, exc("TypeError", "x", f("render", "https://a.example.com/assets/app-3f2a1b9c.js?v=1", 1, true))),
			second: eventWith(2, exc("TypeError", "x", f("render", "https://b.example.com/assets/app-9d8e7f6a.js#x", 1, true))),
		},
		{
			name:   "another directory above the last two segments",
			first:  eventWith(1, exc("TypeError", "x", f("charge", "/srv/app/src/billing/charge.js", 1, true))),
			second: eventWith(2, exc("TypeError", "x", f("charge", "file:///home/ci/build/billing/charge.js", 1, true))),
		},
		{
			name: "recursion of another depth",
			first: eventWith(1, exc("RangeError", "too deep",
				f("walk", "src/tree.js", 3, true), f("walk", "src/tree.js", 3, true), f("walk", "src/tree.js", 3, true), f("main", "src/index.js", 1, true))),
			second: eventWith(2, exc("RangeError", "too deep", f("walk", "src/tree.js", 3, true), f("main", "src/index.js", 1, true))),
		},
		{
			name:   "other dependency frames and frames without filename",
			first:  eventWith(1, exc("TypeError", "x", dependency, `{"function": "Promise.all"}`, f("render", "src/app.js", 1, true))),
			second: eventWith(2, exc("TypeError", "x", f("render", "src/app.js", 1, true))),
		},
		{
			name:   "anonymous function under another name for it",
			first:  eventWith(1, exc("TypeError", "x", f("", "src/app.js", 1, true))),
			second: eventWith(2, exc("TypeError", "x", f("<anonymous>", "src/app.js", 9, true))),
		},
		{
			name:   "no In-app frames: all frames, on other lines",
			first:  eventWith(1, exc("TypeError", "x", noInApp("fetch", "vendor/http.js", 3), noInApp("send", "vendor/api.js", 9))),
			second: eventWith(2, exc("TypeError", "y", noInApp("fetch", "vendor/http.js", 4), noInApp("send", "vendor/api.js", 12))),
		},
		{
			name:   "no frames: messages that differ in numbers and ids",
			first:  eventWith(1, exc("Error", "user 123 not found in 0190b7a4-7c1e-7000-8000-00000000000a")),
			second: eventWith(2, exc("Error", "user 456 not found in 0190b7a4-7c1e-7000-8000-00000000000b")),
		},
		{
			name:   "no frames: messages that differ only in a JWT",
			first:  eventWith(1, exc("Error", "bad token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnMQ")),
			second: eventWith(2, exc("Error", "bad token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIyIn0.c2lnMg")),
		},
		{
			name:   "the same Custom Group Key over other stacks",
			first:  eventWith(1, exc("TypeError", "x", f("render", "src/app.js", 1, true)), `"group_key": ["checkout", "timeout"]`),
			second: eventWith(2, exc("RangeError", "y", f("pay", "src/pay.js", 1, true)), `"group_key": ["checkout", "timeout"]`),
		},
		{
			name: "the same chain of causes",
			first: eventWith(1, exc("Error", "save failed", f("save", "src/db.js", 1, true))+","+
				exc("TypeError", "x", f("encode", "src/codec.js", 4, true))),
			second: eventWith(2, exc("Error", "save failed again", f("save", "src/db.js", 7, true))+","+
				exc("TypeError", "y", f("encode", "src/codec.js", 9, true))),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := countIssues(t, c.first, c.second); got != 1 {
				t.Errorf("Issues = %d, want 1", got)
			}
		})
	}
}

func TestDifferentEventsGetDifferentIssues(t *testing.T) {
	cases := []struct {
		name          string
		first, second string
	}{
		{
			name:   "another type",
			first:  eventWith(1, exc("TypeError", "x", f("render", "src/app.js", 1, true))),
			second: eventWith(2, exc("RangeError", "x", f("render", "src/app.js", 1, true))),
		},
		{
			name:   "another In-app function",
			first:  eventWith(1, exc("TypeError", "x", f("render", "src/app.js", 1, true))),
			second: eventWith(2, exc("TypeError", "x", f("mount", "src/app.js", 1, true))),
		},
		{
			name:   "another In-app file",
			first:  eventWith(1, exc("TypeError", "x", f("render", "src/app.js", 1, true))),
			second: eventWith(2, exc("TypeError", "x", f("render", "src/cart.js", 1, true))),
		},
		{
			name:   "another cause",
			first:  eventWith(1, exc("Error", "x", f("save", "src/db.js", 1, true))+","+exc("TypeError", "x")),
			second: eventWith(2, exc("Error", "x", f("save", "src/db.js", 1, true))+","+exc("RangeError", "x")),
		},
		{
			name:   "no frames: other words in the message",
			first:  eventWith(1, exc("Error", "Failed to fetch")),
			second: eventWith(2, exc("Error", "Network down")),
		},
		{
			name:   "another Custom Group Key over the same stack",
			first:  eventWith(1, exc("TypeError", "x", f("render", "src/app.js", 1, true)), `"group_key": ["a"]`),
			second: eventWith(2, exc("TypeError", "x", f("render", "src/app.js", 1, true)), `"group_key": ["b"]`),
		},
		{
			name:   "a Custom Group Key against none",
			first:  eventWith(1, exc("TypeError", "x", f("render", "src/app.js", 1, true)), `"group_key": ["TypeError"]`),
			second: eventWith(2, exc("TypeError", "x", f("render", "src/app.js", 1, true))),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := countIssues(t, c.first, c.second); got != 2 {
				t.Errorf("Issues = %d, want 2", got)
			}
		})
	}
}

func TestStoredEventExplainsItsGroupKey(t *testing.T) {
	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	sent := []string{
		eventWith(1, exc("TypeError", "x",
			f("dispatch", "node_modules/react/index.js", 1, false),
			f("render", "https://app.example.com/assets/js/app-3f2a1b9c.js", 2, true),
			f("", "/srv/src/util.js", 3, true))),
		eventWith(2, exc("Error", "user 42 not found")),
		eventWith(3, exc("Error", "x"), `"group_key": ["checkout", "timeout"]`),
	}
	postEvents(t, e, key, sent...)
	waitPending(t, e.Queue, 0)

	want := map[string][]string{
		eventID(1): {"type: TypeError", "frame: render @ js/app-<hash>.js", "frame: src/util.js"},
		eventID(2): {"type: Error", "message: user <num> not found"},
		eventID(3): {"custom: checkout", "custom: timeout"},
	}
	stored := listEvents(t, e, project.ID)
	if len(stored) != len(want) {
		t.Fatalf("stored Events = %d, want %d", len(stored), len(want))
	}
	for _, got := range stored {
		id := got.Event.EventID
		if got.GroupingVersion != 1 || !reflect.DeepEqual(got.GroupComponents, want[id]) {
			t.Errorf("Event %s: grouping = %d %q, want 1 %q", id, got.GroupingVersion, got.GroupComponents, want[id])
		}
	}
}

// countIssues sends events to a new Project, one batch each, and returns
// the number of Issues they make.
func countIssues(t *testing.T, events ...string) int {
	t.Helper()

	e := newEndpoint(t, withProcess())
	project, key := createProject(t, e)
	for _, event := range events {
		if status, body := postEvents(t, e, key, event); status != 202 {
			t.Fatalf("response = %d %s, want 202", status, body)
		}
	}
	waitPending(t, e.Queue, 0)
	return len(listIssues(t, e, project.ID))
}

// eventWith returns the n-th Event of a test with exceptions, the contents
// of its exceptions array, and fields, more JSON members.
func eventWith(n int, exceptions string, fields ...string) string {
	members := append([]string{
		fmt.Sprintf(`"event_id": %q`, eventID(n)),
		`"timestamp": "2026-11-27T10:00:00.000Z"`,
		`"platform": "javascript"`,
		`"sdk": {"name": "trace-js", "version": "0.1.0"}`,
		`"exceptions": [` + exceptions + `]`,
	}, fields...)
	return "{" + strings.Join(members, ",") + "}"
}
