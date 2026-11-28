package apis_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/joansgarcia/trace/apis"
	"github.com/joansgarcia/trace/hub"
	"github.com/joansgarcia/trace/migrations"
	"github.com/joansgarcia/trace/store"
)

func TestRequestsDuringShutdownReturnShuttingDown(t *testing.T) {
	started, release, process := newBlockingProcess()
	a, err := StartApp(t, t.TempDir(), withEventsTable(t), WithProcess(process))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	if err := a.Queue().Enqueue(t.Context(), [][]byte{[]byte("checkout/e1")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	<-started

	stopped := stopInBackground(t, a)
	status, body := waitUnhealthy(t, a)
	close(release)
	<-stopped

	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
	if want := `{"error":"shutting_down"}`; body != want {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func TestShutdownClosesHubStreams(t *testing.T) {
	a := NewApp(t)
	changes, unsubscribe := a.Hub().Subscribe()
	defer unsubscribe()

	a.Stop(t)

	select {
	case _, open := <-changes:
		if open {
			t.Fatal("got a notification, want the stream closed")
		}
	case <-time.After(timeout):
		t.Fatal("timed out waiting for the stream to close")
	}
}

func TestEventsAcceptedBeforeShutdownAreProcessedBeforeExit(t *testing.T) {
	dir := t.TempDir()
	started, release, process := newBlockingProcess()
	first, err := StartApp(t, dir, withEventsTable(t), WithProcess(process))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	if err := first.Queue().Enqueue(t.Context(), [][]byte{[]byte("checkout/e1")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	<-started

	stopped := stopInBackground(t, first)
	waitUnhealthy(t, first)
	close(release)
	<-stopped

	// Without processing, the second app only shows what the first one
	// left.
	second, err := StartApp(t, dir, withEventsTable(t), WithoutProcess())
	if err != nil {
		t.Fatalf("restart app: %v", err)
	}
	if got := second.Queue().Pending(); got != 0 {
		t.Errorf("pending after restart = %d, want 0", got)
	}
	assertEventCount(t, second, "checkout", "e1", 1)
}

func TestShutdownGivesUpAfterTimeoutAndKeepsEventsForNextStart(t *testing.T) {
	dir := t.TempDir()
	queued, err := StartApp(t, dir, withEventsTable(t), WithoutProcess())
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	if err := queued.Queue().Enqueue(t.Context(), [][]byte{[]byte("checkout/e1")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	queued.Stop(t)

	// The processing function is never released, so the shutdown can only
	// end by giving up.
	started, _, process := newBlockingProcess()
	config := apis.Config{
		Addr:            "127.0.0.1:0",
		DataDir:         dir,
		ShutdownTimeout: 200 * time.Millisecond,
	}
	withEventsTable(t)(&config)
	WithProcess(process)(&config)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- apis.NewServer(config).Run(ctx) }()
	<-started

	cancel()
	begin := time.Now()
	select {
	case err := <-done:
		if err == nil {
			t.Error("run returned nil, want the error of the incomplete shutdown")
		}
		if elapsed := time.Since(begin); elapsed < config.ShutdownTimeout {
			t.Errorf("run returned after %v, before the shutdown timeout of %v", elapsed, config.ShutdownTimeout)
		}
	case <-time.After(timeout):
		t.Fatal("run did not return after the shutdown timeout")
	}

	restarted, err := StartApp(t, dir, withEventsTable(t), WithProcess(saveEvent))
	if err != nil {
		t.Fatalf("restart app: %v", err)
	}
	waitPending(t, restarted, 0)
	assertEventCount(t, restarted, "checkout", "e1", 1)
}

func TestShutdownCheckpointsTheWALAndReleasesTheDataDirectory(t *testing.T) {
	a, err := StartApp(t, t.TempDir(), withEventsTable(t), WithProcess(saveEvent))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	if err := a.Queue().Enqueue(t.Context(), [][]byte{[]byte("checkout/e1")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitPending(t, a, 0)
	// An Operator reading the files with another tool keeps SQLite from
	// checkpointing on its own when the app closes its connections.
	for _, file := range []string{"ingest.db", "data.db"} {
		holdOpen(t, filepath.Join(a.DataDir, file))
	}

	a.Stop(t)

	for _, file := range []string{"ingest.db-wal", "data.db-wal"} {
		info, err := os.Stat(filepath.Join(a.DataDir, file))
		if err == nil && info.Size() > 0 {
			t.Errorf("%s has %d bytes after shutdown, want it checkpointed", file, info.Size())
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat %s: %v", file, err)
		}
	}
	st, err := store.Open(a.DataDir)
	if err != nil {
		t.Fatalf("open data directory after shutdown: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("close data directory: %v", err)
	}
}

// holdOpen opens a connection to the SQLite file at path, reads from it, and
// keeps it open until the test ends.
func holdOpen(t *testing.T, path string) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
}

func TestShutdownGivesUpAfterTimeoutWhenProcessIgnoresItsContext(t *testing.T) {
	dir := t.TempDir()
	queued, err := StartApp(t, dir, withEventsTable(t), WithoutProcess())
	if err != nil {
		t.Fatalf("start app: %v", err)
	}
	if err := queued.Queue().Enqueue(t.Context(), [][]byte{[]byte("checkout/e1")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	queued.Stop(t)

	// stuck holds the only write connection of data.db and ignores its
	// context, as a Process blocked in the middle of a commit would.
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stuck := func(ctx context.Context, data *store.DB, event []byte) ([]hub.Change, error) {
		tx, err := data.Write.Begin()
		if err != nil {
			return nil, err
		}
		started <- struct{}{}
		<-release
		return nil, tx.Rollback()
	}
	config := apis.Config{
		Addr:            "127.0.0.1:0",
		DataDir:         dir,
		ShutdownTimeout: 200 * time.Millisecond,
	}
	withEventsTable(t)(&config)
	WithProcess(stuck)(&config)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- apis.NewServer(config).Run(ctx) }()
	<-started

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("run returned nil, want the error of the incomplete shutdown")
		}
	case <-time.After(timeout):
		t.Fatal("run did not return after the shutdown timeout")
	}
}

// newBlockingProcess returns a processing function that signals started when it
// receives an Event, then waits for release or for its context to end before
// saving it like saveEvent.
func newBlockingProcess() (started <-chan struct{}, release chan struct{}, process func(context.Context, *store.DB, []byte) ([]hub.Change, error)) {
	s := make(chan struct{}, 1)
	release = make(chan struct{})
	process = func(ctx context.Context, data *store.DB, event []byte) ([]hub.Change, error) {
		select {
		case s <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return saveEvent(ctx, data, event)
	}
	return s, release, process
}

// stopInBackground stops a and returns a channel closed once it has stopped.
func stopInBackground(t *testing.T, a *App) <-chan struct{} {
	t.Helper()

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		a.Stop(t)
	}()
	return stopped
}

// waitUnhealthy polls the health endpoint of a until it answers other than
// 200, and returns that answer.
func waitUnhealthy(t *testing.T, a *App) (int, string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := a.Client.Get(a.URL + "/api/health")
		if err != nil {
			t.Fatalf("get health: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read health: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode, strings.TrimSpace(string(body))
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the app to stop answering ok")
	return 0, ""
}

// saveEvent is a processing function for Events of the form
// "<project>/<event_id>". It stores each one once in the table of
// withEventsTable, and returns a change if it was new.
func saveEvent(ctx context.Context, data *store.DB, event []byte) ([]hub.Change, error) {
	project, id, _ := strings.Cut(string(event), "/")
	result, err := data.Write.ExecContext(ctx, "INSERT OR IGNORE INTO test_events (project, event_id) VALUES (?, ?)", project, id)
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return nil, err
	}
	return []hub.Change{{Kind: hub.IssueUpdated, ProjectID: project}}, nil
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
