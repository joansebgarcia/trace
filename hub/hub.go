// Package hub notifies in-process subscribers that data.db changed. Writers
// publish after their commit; the database takes no part in notifications.
package hub

import (
	"context"
	"sync"

	"github.com/joansgarcia/trace/store"
)

// Hub fans out change notifications to its subscribers.
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
	closed      bool
}

func New() *Hub {
	return &Hub{subscribers: map[chan struct{}]struct{}{}}
}

// Subscribe returns a channel that receives a value after commits to data.db,
// and a function that ends the subscription. Notifications that arrive while
// one is still unread merge into it, so a slow subscriber never blocks a
// writer and must re-read what it shows after each value. The channel is
// closed when the hub closes.
func (h *Hub) Subscribe() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)

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

// Publish notifies every subscriber that data.db changed. It never blocks.
func (h *Hub) Publish() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for c := range h.subscribers {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// Wrap returns process as a processing function of the queue that publishes
// after each Event that changes data, once process has committed it. An
// Event that fails or changes nothing publishes nothing.
func (h *Hub) Wrap(process func(ctx context.Context, data *store.DB, event []byte) (bool, error), data *store.DB) func(context.Context, []byte) error {
	return func(ctx context.Context, event []byte) error {
		changed, err := process(ctx, data, event)
		if err != nil {
			return err
		}
		if changed {
			h.Publish()
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
