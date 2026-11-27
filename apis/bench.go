//go:build bench

package apis

import (
	"errors"
	"io"
	"net/http"

	"github.com/joansebgarcia/amber/queue"
)

// maxBenchEvent is the largest Event body the benchmark endpoint accepts.
const maxBenchEvent = 1 << 20

// The benchmark endpoint enqueues each request body as one Event and answers
// 202 once it is committed. It exists only in builds with the bench tag, to
// measure the queue before the ingest endpoint does.
func init() {
	routeBench = func(mux *http.ServeMux, q *queue.Queue) {
		mux.HandleFunc("POST /api/bench/events", serveBenchEvent(q))
	}
}

func serveBenchEvent(q *queue.Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		event, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBenchEvent))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_body"})
			return
		}

		switch err := q.Enqueue(r.Context(), [][]byte{event}); {
		case err == nil:
			w.WriteHeader(http.StatusAccepted)
		case errors.Is(err, queue.ErrOverloaded):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "overloaded"})
		case errors.Is(err, queue.ErrStopped):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "shutting_down"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enqueue_failed"})
		}
	}
}
