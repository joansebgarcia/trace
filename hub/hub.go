// Package hub notifies in-process subscribers that data.db changed. Writers
// publish after their commit; the database takes no part in notifications.
package hub

import "sync"

// Hub fans out change notifications to its subscribers.
type Hub struct {
	mu          sync.Mutex
	subscribers map[chan struct{}]struct{}
}

func New() *Hub {
	return &Hub{subscribers: map[chan struct{}]struct{}{}}
}

// Subscribe returns a channel that receives a value after commits to data.db,
// and a function that ends the subscription. Notifications that arrive while
// one is still unread merge into it, so a slow subscriber never blocks a
// writer and must re-read what it shows after each value.
func (h *Hub) Subscribe() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)

	h.mu.Lock()
	h.subscribers[c] = struct{}{}
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
