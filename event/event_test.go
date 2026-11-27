package event_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/joansgarcia/trace/event"
)

const valid = `{"event_id":"0190b7a4-7c1e-7000-8000-000000000001","timestamp":"2026-11-27T10:00:00.000Z","platform":"javascript","sdk":{"name":"trace-js","version":"0.1.0"},"exceptions":[{"type":"TypeError"}]}`

func TestInvalidEventRejectsTheWholeBatch(t *testing.T) {
	missingSDK := `{"event_id":"0190b7a4-7c1e-7000-8000-000000000002","timestamp":"2026-11-27T10:00:00.000Z","platform":"javascript","exceptions":[{"type":"TypeError"}]}`

	events, err := event.DecodeBatch([]byte(`{"events":[` + valid + `,` + missingSDK + `]}`))

	var batchErr *event.BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("err = %v, want a *BatchError", err)
	}
	if events != nil {
		t.Errorf("events = %+v, want none", events)
	}
	want := event.BatchError{
		Message: "the batch has invalid Events",
		Details: []event.Detail{{Index: 1, Message: "sdk.name and sdk.version are required"}},
	}
	if !reflect.DeepEqual(*batchErr, want) {
		t.Errorf("err = %+v, want %+v", *batchErr, want)
	}
}
