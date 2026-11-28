package grouping_test

import (
	"reflect"
	"testing"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/grouping"
)

func TestFilenamesAreNormalized(t *testing.T) {
	cases := []struct {
		filename, want string
	}{
		{"/srv/app/src/billing/charge.js", "billing/charge.js"},
		{"https://cdn.example.com/assets/app-3f2a1b9c.js?v=2#L1", "assets/app-<hash>.js"},
		{"https://cdn.example.com/static/js/main.a1b2c3d4e5.chunk.js", "js/main.<hash>.chunk.js"},
		{"file:///Users/ann/app/index.js", "app/index.js"},
		{`C:\app\src\index.js`, "src/index.js"},
		{"webpack://checkout/./src/cart.js", "src/cart.js"},
		{"app.js", "app.js"},
		// Words without a digit are not hashes, whatever their length.
		{"src/controllers/checkout.js", "controllers/checkout.js"},
		{"https://example.com", ""},
	}

	for _, c := range cases {
		t.Run(c.filename, func(t *testing.T) {
			if got := grouping.NormalizeFilename(c.filename); got != c.want {
				t.Errorf("NormalizeFilename(%q) = %q, want %q", c.filename, got, c.want)
			}
		})
	}
}

func TestMessagesWithoutFramesAreNormalized(t *testing.T) {
	cases := []struct {
		message, want string
	}{
		{"user 42 not found", "message: user <num> not found"},
		{"order 0190b7a4-7c1e-7000-8000-000000000001 failed", "message: order <uuid> failed"},
		{"fetch https://api.example.com/v1/users?id=3 failed", "message: fetch <url> failed"},
		{"mail to ann@example.com bounced", "message: mail to <email> bounced"},
		{"Cannot read properties of undefined (reading 'id')", "message: Cannot read properties of undefined (reading <str>)"},
		{`unknown key "plan"`, "message: unknown key <str>"},
		{"can't connect after 3.5 s", "message: can't connect after <num> s"},
		{"object 0x7ffd5e8c at deadbeef12", "message: object <hex> at <hex>"},
		{"Script error.", "message: Script error."},
	}

	for _, c := range cases {
		t.Run(c.message, func(t *testing.T) {
			e := event.Event{Exceptions: []event.Exception{{Type: "Error", Message: c.message}}}

			got := grouping.Compute(e).Components

			if want := []string{"type: Error", c.want}; !reflect.DeepEqual(got, want) {
				t.Errorf("components = %q, want %q", got, want)
			}
		})
	}
}

func TestEveryFrameCountsWithoutInAppFrames(t *testing.T) {
	inApp := false
	e := event.Event{Exceptions: []event.Exception{{
		Type: "TypeError",
		Frames: []event.Frame{
			{Function: "fetch", Filename: "vendor/http.js", InApp: &inApp},
			{Function: "native"},
			{Function: "send", Filename: "vendor/api.js"},
		},
	}}}

	got := grouping.Compute(e)

	want := []string{"type: TypeError", "frame: fetch @ vendor/http.js", "frame: send @ vendor/api.js"}
	if got.Version != 1 || !reflect.DeepEqual(got.Components, want) {
		t.Errorf("group = %d %q, want 1 %q", got.Version, got.Components, want)
	}
}

func TestGroupKeyDependsOnlyOnTheComponents(t *testing.T) {
	inApp := true
	first := event.Event{Exceptions: []event.Exception{{Type: "TypeError", Message: "x", Frames: []event.Frame{{Function: "a", Filename: "src/a.js", Lineno: 1, InApp: &inApp}}}}}
	second := event.Event{Exceptions: []event.Exception{{Type: "TypeError", Message: "y", Frames: []event.Frame{{Function: "a", Filename: "src/a.js", Lineno: 9, InApp: &inApp}}}}}
	custom := event.Event{GroupKey: []string{"type: TypeError", "frame: a @ src/a.js"}, Exceptions: first.Exceptions}

	if grouping.Compute(first).Key != grouping.Compute(second).Key {
		t.Error("Events with the same components have other Group Keys")
	}
	// The kind of each component counts, so a Custom Group Key never
	// collides with a computed one.
	if grouping.Compute(first).Key == grouping.Compute(custom).Key {
		t.Error("a Custom Group Key collides with a computed one")
	}
}
