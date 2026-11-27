package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/issues"
	"github.com/joansgarcia/trace/projects"
	"github.com/joansgarcia/trace/queue"
	"github.com/joansgarcia/trace/store"
)

const (
	// Path is the path of the ingest endpoint.
	Path = "/api/ingest/v1/events"
	// keyHeader carries the Ingest Key of a request.
	keyHeader = "X-Trace-Ingest-Key"
	// maxBody is the largest body the endpoint reads.
	maxBody = 1 << 20
	// overloadedRetry is the Retry-After, in seconds, of an overloaded queue.
	overloadedRetry = 5
)

// message is one accepted Event as it waits in the queue.
type message struct {
	ProjectID  string      `json:"project_id"`
	ReceivedAt time.Time   `json:"received_at"`
	Event      event.Event `json:"event"`
}

// apiError is the body of every error response of the endpoint.
type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details []event.Detail `json:"details"`
}

func NewHandler(p *projects.Projects, q *queue.Queue, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(keyHeader)
		if key == "" {
			reject(w, http.StatusUnauthorized, apiError{Code: "ingest_key_missing", Message: "the " + keyHeader + " header is required"})
			return
		}
		project, ok, err := p.Resolve(r.Context(), key)
		if err != nil {
			logger.Error("resolve ingest key failed", "err", err)
			reject(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "the server failed to resolve the ingest key"})
			return
		}
		if !ok {
			reject(w, http.StatusUnauthorized, apiError{Code: "ingest_key_invalid", Message: "the ingest key is unknown or revoked"})
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				reject(w, http.StatusRequestEntityTooLarge, apiError{Code: "payload_too_large", Message: "the body exceeds " + strconv.Itoa(maxBody) + " bytes"})
				return
			}
			reject(w, http.StatusBadRequest, apiError{Code: "invalid_event", Message: "the body could not be read"})
			return
		}

		events, err := event.DecodeBatch(body)
		if err != nil {
			var batchErr *event.BatchError
			errors.As(err, &batchErr)
			writeError(w, http.StatusBadRequest, apiError{Code: "invalid_event", Message: batchErr.Message, Details: batchErr.Details})
			return
		}

		payloads, err := encode(project.ID, time.Now().UTC(), events)
		if err != nil {
			logger.Error("encode Events failed", "project_id", project.ID, "err", err)
			writeError(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "the server failed to accept the Events"})
			return
		}
		switch err := q.Enqueue(r.Context(), payloads); {
		case err == nil:
			writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(events)})
		case errors.Is(err, queue.ErrOverloaded):
			w.Header().Set("Retry-After", strconv.Itoa(overloadedRetry))
			writeError(w, http.StatusServiceUnavailable, apiError{Code: "ingest_overloaded", Message: "the server is overloaded; retry later"})
		case errors.Is(err, queue.ErrStopped):
			writeError(w, http.StatusServiceUnavailable, apiError{Code: "shutting_down", Message: "the server is shutting down"})
		default:
			logger.Error("enqueue Events failed", "project_id", project.ID, "err", err)
			writeError(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "the server failed to accept the Events"})
		}
	}
}

// NewProcess returns the function that processes a queued Event into its
// Issue and reports whether it changed data.db. A queued Event it cannot
// decode is logged and dropped, since retrying it would never succeed.
func NewProcess(logger *slog.Logger) func(ctx context.Context, data *store.DB, payload []byte) (bool, error) {
	return func(ctx context.Context, data *store.DB, payload []byte) (bool, error) {
		var m message
		if err := json.Unmarshal(payload, &m); err != nil {
			logger.Error("drop undecodable queued Event", "err", err)
			return false, nil
		}
		return issues.Record(ctx, data, m.ProjectID, m.ReceivedAt, m.Event)
	}
}

func encode(projectID string, receivedAt time.Time, events []event.Event) ([][]byte, error) {
	payloads := make([][]byte, len(events))
	for i, event := range events {
		payload, err := json.Marshal(message{ProjectID: projectID, ReceivedAt: receivedAt, Event: event})
		if err != nil {
			return nil, err
		}
		payloads[i] = payload
	}
	return payloads, nil
}

// reject writes an error response without reading the body, and closes the
// connection so the server does not read the body to reuse it.
func reject(w http.ResponseWriter, status int, e apiError) {
	w.Header().Set("Connection", "close")
	writeError(w, status, e)
}

func writeError(w http.ResponseWriter, status int, e apiError) {
	if e.Details == nil {
		e.Details = []event.Detail{}
	}
	writeJSON(w, status, map[string]apiError{"error": e})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
