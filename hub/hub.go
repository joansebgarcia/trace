// Package hub notifies in-process subscribers of the changes to data.db.
// Writers publish after their commit; the database takes no part in
// notifications.
package hub

import (
	"context"
	"sync"

	"github.com/joansgarcia/trace/store"
)

// subscriberBuffer is the number of changes a subscriber may leave unread
// before later ones are dropped for it.
const subscriberBuffer = 64

// The kinds of change of an Issue.
const (
	// IssueCreated is the first Event of an Issue.
	IssueCreated Kind = "created"
	// IssueUpdated is a later Event of an Issue.
	IssueUpdated Kind = "updated"
	// IssueRegressed is a Regression: an Event for a resolved Issue.
	IssueRegressed Kind = "regressed"
)

// Kind is what a Change did.
type Kind string

// Change is one committed change to data.db.
type Change struct {
	Kind      Kind
	ProjectID string
	IssueID   string
}

// Hub fans out changes to its subscribers.
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan Change]struct{}
	closed      bool
}

func New() *Hub {
	return &Hub{subscribers: map[chan Change]struct{}{}}
}

// Subscribe returns a channel that receives the changes committed to
// data.db, and a function that ends the subscription. A slow subscriber
// never blocks a writer: once it leaves subscriberBuffer changes unread, it
// misses the later ones until it catches up, so it must re-read what it
// shows after each change rather than apply changes one by one. The channel
// is closed when the hub closes.
func (h *Hub) Subscribe() (<-chan Change, func()) {
	c := make(chan Change, subscriberBuffer)

	h.mu.Lock()
	if h.closed {
		close(c)
	} else {
		h.subscribers[c] = struct{}{}
	}
	h.mu.Unlock()

	var once sync.Once
	return c, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers, c)
			h.mu.Unlock()
		})
	}
}

// Publish sends change to every subscriber. It never blocks.
func (h *Hub) Publish(change Change) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for c := range h.subscribers {
		select {
		case c <- change:
		default:
		}
	}
}

// Wrap returns process as a processing function of the queue that publishes
// the changes of each Event once process has committed them. An Event that
// fails publishes nothing.
func (h *Hub) Wrap(process func(ctx context.Context, data *store.DB, event []byte) ([]Change, error), data *store.DB) func(context.Context, []byte) error {
	return func(ctx context.Context, event []byte) error {
		changes, err := process(ctx, data, event)
		if err != nil {
			return err
		}
		for _, change := range changes {
			h.Publish(change)
		}
		return nil
	}
}

// Close ends every subscription by closing its channel. Later subscriptions
// receive a closed channel and later publishes do nothing.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}
	h.closed = true
	for c := range h.subscribers {
		close(c)
	}
	clear(h.subscribers)
}
