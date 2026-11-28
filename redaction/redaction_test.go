package redaction_test

import (
	"testing"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/redaction"
)

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
			got := redaction.Apply(event.Event{Tags: map[string]string{c.key: "value"}}, nil)

			if redacted := got.Tags[c.key] == redaction.Marker; redacted != c.sensitive {
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

			got := redaction.Apply(e, nil)

			if got.Exceptions[0].Message != c.want {
				t.Errorf("message = %q, want %q", got.Exceptions[0].Message, c.want)
			}
		})
	}
}

func TestSensitiveQueryParametersAreRedacted(t *testing.T) {
	e := event.Event{Request: &event.Request{URL: "https://shop.example.com/a?Access_Token=x&page=2&pwd=&q=%20#token=y"}}

	got := redaction.Apply(e, []string{"q"})

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

	got := redaction.Apply(e, nil)

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

func TestApplyLeavesItsInputAsItIs(t *testing.T) {
	e := event.Event{
		Tags:        map[string]string{"token": "t"},
		Exceptions:  []event.Exception{{Type: "Error", Message: "Bearer abc"}},
		Breadcrumbs: []event.Breadcrumb{{Message: "Bearer abc"}},
		Request:     &event.Request{URL: "https://a.com/?token=t"},
	}

	redaction.Apply(e, nil)

	if e.Tags["token"] != "t" || e.Exceptions[0].Message != "Bearer abc" || e.Breadcrumbs[0].Message != "Bearer abc" || e.Request.URL != "https://a.com/?token=t" {
		t.Errorf("input changed: %+v", e)
	}
}
