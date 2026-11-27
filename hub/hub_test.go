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
	process := h.Wrap(func(context.Context, *store.DB, []byte) (bool, error) {
		if notified(changes) {
			t.Error("notified before the commit")
		}
		return true, nil
	}, nil)

	if err := process(t.Context(), []byte("a")); err != nil {
		t.Fatalf("process: %v", err)
	}

	if !notified(changes) {
		t.Error("not notified after the commit")
	}
}

func TestWrapDoesNotPublishForAnEventThatChangesNothing(t *testing.T) {
	h := hub.New()
	changes, unsubscribe := h.Subscribe()
	defer unsubscribe()
	process := h.Wrap(func(context.Context, *store.DB, []byte) (bool, error) { return false, nil }, nil)

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
	process := h.Wrap(func(context.Context, *store.DB, []byte) (bool, error) { return true, errBusy }, nil)

	if err := process(t.Context(), []byte("a")); !errors.Is(err, errBusy) {
		t.Errorf("err = %v, want %v", err, errBusy)
	}

	if notified(changes) {
		t.Error("notified for a failed Event")
	}
}

// notified reports whether changes holds a notification, without waiting.
func notified(changes <-chan struct{}) bool {
	select {
	case <-changes:
		return true
	default:
		return false
	}
}
