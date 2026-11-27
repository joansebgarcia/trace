package grouping_test

import (
	"testing"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/grouping"
)

func TestEventsWithTheSameTypeAndInAppFramesShareAGroupKey(t *testing.T) {
	first := newEvent("TypeError", "x is undefined", 42)
	// Other lines and another message do not split an Issue.
	moved := newEvent("TypeError", "y is undefined", 50)
	other := newEvent("RangeError", "x is undefined", 42)

	if grouping.Key(first) != grouping.Key(moved) {
		t.Error("moved Event has another Group Key, want the same")
	}
	if grouping.Key(first) == grouping.Key(other) {
		t.Error("Event of another type has the same Group Key, want another")
	}
}

// newEvent returns an Event with one exception of type errType and an In-app
// frame at lineno after a dependency frame.
func newEvent(errType, message string, lineno int) event.Event {
	inApp, dependency := true, false
	return event.Event{
		Exceptions: []event.Exception{{
			Type:    errType,
			Message: message,
			Frames: []event.Frame{
				{Function: "dispatch", Filename: "node_modules/react/index.js", Lineno: 10, InApp: &dependency},
				{Function: "render", Filename: "src/app.js", Lineno: lineno, InApp: &inApp},
			},
		}},
	}
}
