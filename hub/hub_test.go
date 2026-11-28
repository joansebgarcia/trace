package hub_test

import (
	"context"
	"errors"
	"testing"

	"github.com/joansgarcia/trace/hub"
	"github.com/joansgarcia/trace/store"
)

func TestWrapPublishesAfterTheEventIsCommitted(t *testing.T) {
	h := hub.New()
	changes, unsubscribe := h.Subscribe()
	defer unsubscribe()
	created := hub.Change{Kind: hub.IssueCreated, ProjectID: "p1", IssueID: "i1"}
	process := h.Wrap(func(context.Context, *store.DB, []byte) ([]hub.Change, error) {
		if notified(changes) {
			t.Error("notified before the commit")
		}
		return []hub.Change{created}, nil
	}, nil)

	if err := process(t.Context(), []byte("a")); err != nil {
		t.Fatalf("process: %v", err)
	}

	select {
	case got := <-changes:
		if got != created {
			t.Errorf("change = %+v, want %+v", got, created)
		}
	default:
		t.Error("not notified after the commit")
	}
}

func TestWrapDoesNotPublishForAnEventThatChangesNothing(t *testing.T) {
	h := hub.New()
	changes, unsubscribe := h.Subscribe()
	defer unsubscribe()
	process := h.Wrap(func(context.Context, *store.DB, []byte) ([]hub.Change, error) { return nil, nil }, nil)

	if err := process(t.Context(), []byte("a")); err != nil {
		t.Fatalf("process: %v", err)
	}

	if notified(changes) {
		t.Error("notified for an Event that changed nothing")
	}
}

func TestWrapDoesNotPublishForAFailedEvent(t *testing.T) {
	h := hub.New()
	changes, unsubscribe := h.Subscribe()
	defer unsubscribe()
	errBusy := errors.New("database is busy")
	process := h.Wrap(func(context.Context, *store.DB, []byte) ([]hub.Change, error) {
		return []hub.Change{{Kind: hub.IssueUpdated}}, errBusy
	}, nil)

	if err := process(t.Context(), []byte("a")); !errors.Is(err, errBusy) {
		t.Errorf("err = %v, want %v", err, errBusy)
	}

	if notified(changes) {
		t.Error("notified for a failed Event")
	}
}

// notified reports whether changes holds a notification, without waiting.
func notified(changes <-chan hub.Change) bool {
	select {
	case <-changes:
		return true
	default:
		return false
	}
}

func TestSlowSubscriberNeverBlocksAPublish(t *testing.T) {
	h := hub.New()
	changes, unsubscribe := h.Subscribe()
	defer unsubscribe()

	// Far more changes than a subscriber buffers, none read.
	for range 1000 {
		h.Publish(hub.Change{Kind: hub.IssueUpdated})
	}

	if !notified(changes) {
		t.Error("not notified, want the subscriber to have changes to read")
	}
}
