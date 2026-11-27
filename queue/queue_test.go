package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joansgarcia/trace/migrations"
	"github.com/joansgarcia/trace/queue"
	"github.com/joansgarcia/trace/store"
)

const timeout = 5 * time.Second

// recorder is a processing function that reports each Event it receives.
type recorder struct {
	got chan string
}

func newRecorder() *recorder {
	return &recorder{got: make(chan string, 1024)}
}

func (r *recorder) Process(_ context.Context, event []byte) error {
	r.got <- string(event)
	return nil
}

func TestEnqueuedEventsAreProcessedAndLeaveTheQueue(t *testing.T) {
	db := openStore(t, t.TempDir()).Ingest
	r := newRecorder()
	q := startQueue(t, queue.Config{DB: db, Process: r.Process})

	if err := q.Enqueue(t.Context(), [][]byte{[]byte("a"), []byte("b")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	got := map[string]bool{}
	for range 2 {
		got[receive(t, r.got)] = true
	}
	if !got["a"] || !got["b"] {
		t.Errorf("processed %v, want a and b", got)
	}
	waitPending(t, q, 0)
}

func TestEventsEnqueuedAfterTheQueueDrainsAreProcessed(t *testing.T) {
	r := newRecorder()
	q := startQueue(t, queue.Config{DB: openStore(t, t.TempDir()).Ingest, Process: r.Process})

	for _, event := range []string{"a", "b"} {
		if err := q.Enqueue(t.Context(), [][]byte{[]byte(event)}); err != nil {
			t.Fatalf("enqueue %s: %v", event, err)
		}
		if got := receive(t, r.got); got != event {
			t.Fatalf("processed %q, want %q", got, event)
		}
		waitPending(t, q, 0)
	}
}

func TestFailedEventIsRetried(t *testing.T) {
	r := newRecorder()
	failed := false
	flaky := func(ctx context.Context, event []byte) error {
		if !failed {
			failed = true
			return errors.New("database is busy")
		}
		return r.Process(ctx, event)
	}
	q := startQueue(t, queue.Config{DB: openStore(t, t.TempDir()).Ingest, Process: flaky, Workers: 1})

	if err := q.Enqueue(t.Context(), [][]byte{[]byte("a")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if got := receive(t, r.got); got != "a" {
		t.Errorf("processed %q, want a", got)
	}
	waitPending(t, q, 0)
}

func TestAcceptedEventsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	failing := func(context.Context, []byte) error { return errors.New("not now") }
	s := openStore(t, dir)
	first := startQueue(t, queue.Config{DB: s.Ingest, Process: failing})

	if err := first.Enqueue(t.Context(), [][]byte{[]byte("a"), []byte("b")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if got := first.Pending(); got != 2 {
		t.Errorf("pending right after enqueue = %d, want 2", got)
	}
	closeQueue(t, first)
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	r := newRecorder()
	second := startQueue(t, queue.Config{DB: openStore(t, dir).Ingest, Process: r.Process})
	got := map[string]bool{}
	for range 2 {
		got[receive(t, r.got)] = true
	}
	if !got["a"] || !got["b"] {
		t.Errorf("processed %v after restart, want a and b", got)
	}
	waitPending(t, second, 0)
}

func TestProcessedEventsAreNotProcessedAgainAfterARestart(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	r := newRecorder()
	first := startQueue(t, queue.Config{DB: s.Ingest, Process: r.Process})
	if err := first.Enqueue(t.Context(), [][]byte{[]byte("a")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	receive(t, r.got)
	waitPending(t, first, 0)
	closeQueue(t, first)
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	again := newRecorder()
	second := startQueue(t, queue.Config{DB: openStore(t, dir).Ingest, Process: again.Process})
	if got := second.Pending(); got != 0 {
		t.Errorf("pending after restart = %d, want 0", got)
	}
	if err := second.Enqueue(t.Context(), [][]byte{[]byte("b")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if got := receive(t, again.got); got != "b" {
		t.Errorf("processed %q after restart, want only b", got)
	}
}

func TestEnqueueAfterCloseFails(t *testing.T) {
	q := startQueue(t, queue.Config{DB: openStore(t, t.TempDir()).Ingest, Process: newRecorder().Process})
	closeQueue(t, q)

	err := q.Enqueue(t.Context(), [][]byte{[]byte("a")})

	if !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("enqueue after close = %v, want ErrStopped", err)
	}
}

func TestCloseWaitsForTheEventInProgress(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	blocking := func(context.Context, []byte) error {
		close(started)
		<-release
		return nil
	}
	q := startQueue(t, queue.Config{DB: openStore(t, t.TempDir()).Ingest, Process: blocking, Workers: 1})
	if err := q.Enqueue(t.Context(), [][]byte{[]byte("a")}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	<-started

	closed := make(chan error, 1)
	go func() { closed <- q.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("close returned %v before the Event in progress finished", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := q.Pending(); got != 0 {
		t.Errorf("pending after close = %d, want 0", got)
	}
}

func TestEnqueueFailsWhenOverloaded(t *testing.T) {
	db := openStore(t, t.TempDir()).Ingest
	q := startQueue(t, queue.Config{DB: db, Process: newRecorder().Process, Capacity: 2})

	// Holding the only write connection stalls the writer, so accepted
	// Events pile up waiting for their commit.
	stall, err := db.Write.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("stall writer: %v", err)
	}

	results := make(chan error, 2)
	for _, batch := range [][][]byte{{[]byte("a"), []byte("b")}, {[]byte("c")}} {
		go func() { results <- q.Enqueue(t.Context(), batch) }()
	}

	// Three Events do not fit a capacity of two: one batch is rejected at
	// once, while the other waits for the writer.
	if err := receiveError(t, results); !errors.Is(err, queue.ErrOverloaded) {
		t.Fatalf("first result = %v, want ErrOverloaded", err)
	}
	if err := stall.Rollback(); err != nil {
		t.Fatalf("release writer: %v", err)
	}
	if err := receiveError(t, results); err != nil {
		t.Fatalf("second result = %v, want success", err)
	}
}

func startQueue(t *testing.T, config queue.Config) *queue.Queue {
	t.Helper()

	q, err := queue.New(config)
	if err != nil {
		t.Fatalf("new queue: %v", err)
	}
	t.Cleanup(func() { closeQueue(t, q) })

	return q
}

// closeQueue closes q. Closing it again is harmless.
func closeQueue(t *testing.T, q *queue.Queue) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := q.Close(ctx); err != nil {
		t.Errorf("close queue: %v", err)
	}
}

// openStore opens the data directory dir with its files migrated. Closing it
// again when the test ends is harmless.
func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if err := s.Migrate(t.Context(), schema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return s
}

func receive(t *testing.T, c <-chan string) string {
	t.Helper()

	select {
	case v := <-c:
		return v
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a processed Event")
		return ""
	}
}

func receiveError(t *testing.T, c <-chan error) error {
	t.Helper()

	select {
	case err := <-c:
		return err
	case <-time.After(timeout):
		t.Fatal("timed out waiting for Enqueue")
		return nil
	}
}

func waitPending(t *testing.T, q *queue.Queue, want int) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for q.Pending() != want {
		if time.Now().After(deadline) {
			t.Fatalf("pending = %d, want %d", q.Pending(), want)
		}
		time.Sleep(time.Millisecond)
	}
}
