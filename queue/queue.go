package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joansebgarcia/trace/store"
)

const (
	// commitDelay is how long the writer waits for more Events before a
	// group commit.
	commitDelay = 5 * time.Millisecond
	// commitSize is the number of Events that triggers a group commit
	// without waiting for commitDelay.
	commitSize = 200
	// fetchSize is the number of Events the workers read from the queue at
	// a time.
	fetchSize = 100
	// defaultWorkers is the number of workers when Config.Workers is 0.
	defaultWorkers = 4
	// retryDelay is the wait before retrying a failed Event. It doubles
	// after each failure, up to maxRetryDelay.
	retryDelay    = 100 * time.Millisecond
	maxRetryDelay = 30 * time.Second
	// defaultCapacity is the capacity when Config.Capacity is 0: 25 group
	// commits of commitSize.
	defaultCapacity = 5000
)

var (
	// ErrOverloaded is returned by Enqueue when the writer cannot keep up:
	// accepting the Events would exceed Config.Capacity.
	ErrOverloaded = errors.New("queue is overloaded")
	// ErrStopped is returned by Enqueue once the queue is closing.
	ErrStopped = errors.New("queue is shutting down")

	errWriterStopped = errors.New("queue writer is not running")
)

// Config configures a queue.
type Config struct {
	// DB is ingest.db, migrated.
	DB *store.DB
	// Process commits an Event to data.db. It may receive an Event more than
	// once and must be idempotent. Nil starts no workers, so Events stay in
	// the queue.
	Process func(ctx context.Context, event []byte) error
	// Workers is the number of concurrent calls to Process. Zero uses a
	// default.
	Workers int
	// Capacity is the maximum number of Events accepted and waiting for
	// their commit; Enqueue fails with ErrOverloaded beyond it. Zero uses a
	// default.
	Capacity int
	// Logger receives the queue logs. Nil discards them.
	Logger *slog.Logger
}

// Queue is a durable queue of Events.
type Queue struct {
	config   Config
	logger   *slog.Logger
	requests chan *request
	jobs     chan job
	// wake tells the dispatcher that a commit added Events.
	wake chan struct{}
	// pending counts the Events committed and not yet deleted.
	pending atomic.Int64
	// waiting counts the Events accepted and not yet committed.
	waiting atomic.Int64
	alive   atomic.Bool

	// mu guards closed, so that no Enqueue sends on requests after Close
	// closes it.
	mu     sync.RWMutex
	closed bool
	// stop tells the dispatcher to hand out no more Events.
	stop chan struct{}
	// ctx is passed to Process; it ends when Close gives up waiting.
	ctx        context.Context
	cancel     context.CancelFunc
	closeOnce  sync.Once
	writerDone chan struct{}
	workers    sync.WaitGroup
}

// request is one call to Enqueue waiting for its commit.
type request struct {
	events [][]byte
	done   chan error
}

// job is one Event read from the queue for a worker.
type job struct {
	id      int64
	payload []byte
}

// New starts the writer and the workers on config.DB. Events left in the
// queue by an earlier run are processed again.
func New(config Config) (*Queue, error) {
	if config.Workers == 0 {
		config.Workers = defaultWorkers
	}
	if config.Capacity == 0 {
		config.Capacity = defaultCapacity
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		config:     config,
		logger:     logger,
		requests:   make(chan *request),
		jobs:       make(chan job),
		wake:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		writerDone: make(chan struct{}),
	}

	var pending int64
	if err := config.DB.Read.QueryRow("SELECT count(*) FROM queue").Scan(&pending); err != nil {
		cancel()
		return nil, fmt.Errorf("count queued Events: %w", err)
	}
	q.pending.Store(pending)

	q.alive.Store(true)
	go q.write()
	if config.Process == nil {
		close(q.jobs)
		return q, nil
	}
	go q.dispatch()
	for range config.Workers {
		q.workers.Add(1)
		go q.work()
	}

	return q, nil
}

// Enqueue stores events and returns once they are committed to ingest.db. It
// fails with ErrOverloaded if the writer cannot keep up and with ErrStopped
// once the queue is closing. If ctx ends first, the Events may still be
// committed.
func (q *Queue) Enqueue(ctx context.Context, events [][]byte) error {
	if len(events) == 0 {
		return nil
	}

	size := int64(len(events))
	if q.waiting.Add(size) > int64(q.config.Capacity) {
		q.waiting.Add(-size)
		return ErrOverloaded
	}

	r := &request{events: events, done: make(chan error, 1)}
	if err := q.send(ctx, r); err != nil {
		q.waiting.Add(-size)
		return err
	}

	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Pending returns the number of committed Events not yet processed.
func (q *Queue) Pending() int {
	return int(q.pending.Load())
}

// Ping reports an error unless the writer is running.
func (q *Queue) Ping() error {
	if !q.alive.Load() {
		return errWriterStopped
	}
	return nil
}

// Close stops accepting Events, commits the ones already accepted, and waits
// for the workers to finish the Events in progress, until ctx ends. Events
// still in the queue are processed on the next start.
func (q *Queue) Close(ctx context.Context) error {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		close(q.requests)
		q.mu.Unlock()
		close(q.stop)
	})

	select {
	case <-q.writerDone:
	case <-ctx.Done():
		q.cancel()
		return fmt.Errorf("commit accepted Events: %w", ctx.Err())
	}

	done := make(chan struct{})
	go func() {
		q.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		q.cancel()
		return nil
	case <-ctx.Done():
		q.cancel()
		return fmt.Errorf("finish Events in progress: %w", ctx.Err())
	}
}

func (q *Queue) send(ctx context.Context, r *request) error {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if q.closed {
		return ErrStopped
	}
	select {
	case q.requests <- r:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// write group-commits the requests until q.requests is closed.
func (q *Queue) write() {
	defer close(q.writerDone)
	defer q.alive.Store(false)

	for first := range q.requests {
		batch := []*request{first}
		size := len(first.events)
		timer := time.NewTimer(commitDelay)
	collect:
		for size < commitSize {
			select {
			case r, ok := <-q.requests:
				if !ok {
					break collect
				}
				batch = append(batch, r)
				size += len(r.events)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()

		err := q.commit(batch, size)
		q.waiting.Add(-int64(size))
		if err == nil {
			q.notify()
		} else {
			q.logger.Error("group commit failed", "events", size, "err", err)
		}
		for _, r := range batch {
			r.done <- err
		}
	}
}

// commit inserts the Events of batch, size in total, in one transaction.
func (q *Queue) commit(batch []*request, size int) error {
	tx, err := q.config.DB.Write.Begin()
	if err != nil {
		return fmt.Errorf("begin group commit: %w", err)
	}
	insert, err := tx.Prepare("INSERT INTO queue (payload) VALUES (?)")
	if err != nil {
		return errors.Join(fmt.Errorf("prepare group commit: %w", err), tx.Rollback())
	}
	defer func() { _ = insert.Close() }()

	for _, r := range batch {
		for _, event := range r.events {
			if _, err := insert.Exec(event); err != nil {
				return errors.Join(fmt.Errorf("insert Event: %w", err), tx.Rollback())
			}
		}
	}
	// Count the Events before the commit makes them visible to the
	// workers, so Pending never goes below the true count.
	q.pending.Add(int64(size))
	if err := tx.Commit(); err != nil {
		q.pending.Add(-int64(size))
		return fmt.Errorf("commit Events: %w", err)
	}

	return nil
}

func (q *Queue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// dispatch reads the queue in order and hands each Event to a worker. Within
// a run, an Event is handed out once; one left in the queue is handed out
// again on the next start.
func (q *Queue) dispatch() {
	defer close(q.jobs)

	var last int64
	for {
		jobs, err := q.fetch(last)
		if err != nil {
			q.logger.Error("read queue failed", "err", err)
		}
		for _, j := range jobs {
			select {
			case q.jobs <- j:
				last = j.id
			case <-q.stop:
				return
			}
		}
		if len(jobs) == fetchSize {
			continue
		}
		select {
		case <-q.wake:
		case <-q.stop:
			return
		}
	}
}

func (q *Queue) fetch(after int64) ([]job, error) {
	rows, err := q.config.DB.Read.Query("SELECT id, payload FROM queue WHERE id > ? ORDER BY id LIMIT ?", after, fetchSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.payload); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}

	return jobs, rows.Err()
}

func (q *Queue) work() {
	defer q.workers.Done()

	for j := range q.jobs {
		if !q.process(j) {
			return
		}
		if _, err := q.config.DB.Write.Exec("DELETE FROM queue WHERE id = ?", j.id); err != nil {
			q.logger.Error("delete processed Event failed; it will be processed again on the next start", "queue_id", j.id, "err", err)
			continue
		}
		q.pending.Add(-1)
	}
}

// process calls Process on j until it succeeds, waiting longer after each
// failure. It returns false if the queue closes first; j then stays in the
// queue for the next start.
func (q *Queue) process(j job) bool {
	delay := retryDelay
	for {
		err := q.config.Process(q.ctx, j.payload)
		if err == nil {
			return true
		}
		q.logger.Warn("process Event failed; retrying", "queue_id", j.id, "retry_in", delay.String(), "err", err)

		select {
		case <-time.After(delay):
		case <-q.stop:
			return false
		}
		delay = min(delay*2, maxRetryDelay)
	}
}
