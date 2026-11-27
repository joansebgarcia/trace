package apis

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// NewRouter serves the HTTP API. ping reports whether the instance is
// healthy; once stopping is set, every request gets 503 shutting_down.
func NewRouter(logger *slog.Logger, ping func(context.Context) error, stopping *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", serveHealth(logger, ping))

	return logRequests(logger, rejectWhileStopping(stopping, mux))
}

func rejectWhileStopping(stopping *atomic.Bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stopping.Load() {
			w.Header().Set("Connection", "close")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "shutting_down"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveHealth(logger *slog.Logger, ping func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := ping(r.Context()); err != nil {
			logger.Warn("health check failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		logger.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", recorder.status)
	})
}
