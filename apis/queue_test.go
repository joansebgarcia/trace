package apis_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joansgarcia/trace/migrations"
	"github.com/joansgarcia/trace/store"
)

func TestEnqueuedEventsReachDataAndLeaveTheQueue(t *testing.T) {
	a, err := StartApp(t, t.TempDir(), withEventsTable(t), WithProcess(saveEvent))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}

	if err := a.Queue().Enqueue(t.Context(), [][]byte{[]byte("checkout/e1"), []byte("checkout/e2")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	waitPending(t, a, 0)
	assertEventCount(t, a, "checkout", "e1", 1)
	assertEventCount(t, a, "checkout", "e2", 1)
}

func TestEventSavedBeforeACrashIsStoredOnceAfterRestart(t *testing.T) {
	dir := t.TempDir()
	saved := make(chan struct{}, 1)
	// crashing commits the Event to data.db, then fails on every attempt so
	// the queue never deletes it, until the app stops as a crash would.
	crashing := func(ctx context.Context, data *store.DB, event []byte) (bool, error) {
		if _, err := saveEvent(ctx, data, event); err != nil {
			return false, err
		}
		select {
		case saved <- struct{}{}:
		default:
		}
		return false, errors.New("crash")
	}
	first, err := StartApp(t, dir, withEventsTable(t), WithProcess(crashing))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	if err := first.Queue().Enqueue(t.Context(), [][]byte{[]byte("checkout/e1")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	<-saved
	first.Stop(t)

	second, err := StartApp(t, dir, withEventsTable(t), WithProcess(saveEvent))
	if err != nil {
		t.Fatalf("restart app: %v", err)
	}

	waitPending(t, second, 0)
	assertEventCount(t, second, "checkout", "e1", 1)
}

func TestHubSubscriberIsNotifiedAfterCommitToData(t *testing.T) {
	a, err := StartApp(t, t.TempDir(), withEventsTable(t), WithProcess(saveEvent))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	changes, unsubscribe := a.Hub().Subscribe()
	defer unsubscribe()

	if err := a.Queue().Enqueue(t.Context(), [][]byte{[]byte("checkout/e1")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case <-changes:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a notification")
	}
	assertEventCount(t, a, "checkout", "e1", 1)
}

// saveEvent is a processing function for Events of the form
// "<project>/<event_id>". It stores each one once in the table of
// withEventsTable, and reports whether it was new.
func saveEvent(ctx context.Context, data *store.DB, event []byte) (bool, error) {
	project, id, _ := strings.Cut(string(event), "/")
	result, err := data.Write.ExecContext(ctx, "INSERT OR IGNORE INTO test_events (project, event_id) VALUES (?, ?)", project, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// withEventsTable makes the app apply the built-in migrations plus one that
// adds to data.db the table saveEvent writes to.
func withEventsTable(t testing.TB) Option {
	t.Helper()

	schema, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	schema.Data = append(schema.Data, store.Migration{
		Version: len(schema.Data) + 1,
		Name:    "test_events",
		SQL:     "CREATE TABLE test_events (project TEXT NOT NULL, event_id TEXT NOT NULL, PRIMARY KEY (project, event_id))",
	})

	return WithMigrations(schema)
}

func waitPending(t *testing.T, a *App, want int) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for a.Queue().Pending() != want {
		if time.Now().After(deadline) {
			t.Fatalf("pending = %d, want %d", a.Queue().Pending(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertEventCount(t *testing.T, a *App, project, id string, want int) {
	t.Helper()

	var got int
	query := "SELECT count(*) FROM test_events WHERE project = ? AND event_id = ?"
	if err := a.Store().Data.Read.QueryRow(query, project, id).Scan(&got); err != nil {
		t.Fatalf("count Events: %v", err)
	}
	if got != want {
		t.Errorf("Event %s/%s stored %d times, want %d", project, id, got, want)
	}
}
