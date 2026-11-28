// Package ingest serves the ingest endpoint of the protocol v1 and processes
// the Events it accepts.
package ingest

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/joansgarcia/trace/event"
	"github.com/joansgarcia/trace/hub"
	"github.com/joansgarcia/trace/issues"
	"github.com/joansgarcia/trace/projects"
	"github.com/joansgarcia/trace/queue"
	"github.com/joansgarcia/trace/redaction"
	"github.com/joansgarcia/trace/store"
)

const (
	// DefaultRateLimit is the rate limit of a Project, in Events/s, unless
	// configured otherwise.
	DefaultRateLimit = 200
	// DefaultMaxPending is the largest backlog of the queue, in Events,
	// unless configured otherwise.
	DefaultMaxPending = 100_000
	// Path is the path of the ingest endpoint.
	Path = "/api/ingest/v1/events"
	// keyHeader and keyParam carry the Ingest Key of a request.
	keyHeader = "X-Trace-Ingest-Key"
	keyParam  = "ingest_key"
	// maxSent is the largest body as sent, compressed or not.
	maxSent = 256 << 10
	// maxBody is the largest body after decompression.
	maxBody = 1 << 20
	// overloadedRetry is the Retry-After, in seconds, of an overloaded queue.
	overloadedRetry = 5
	// defaultReportInterval is how often the rejections are logged.
	defaultReportInterval = time.Minute
	// preflightMaxAge is how long, in seconds, a browser may cache a
	// preflight.
	preflightMaxAge = "86400"
)

// The error codes of the endpoint.
const (
	codeInvalidEvent     = "invalid_event"
	codeKeyMissing       = "ingest_key_missing"
	codeKeyInvalid       = "ingest_key_invalid"
	codeKeyConflict      = "ingest_key_conflict"
	codeTooLarge         = "payload_too_large"
	codeUnsupportedMedia = "unsupported_media_type"
	codeMethod           = "method_not_allowed"
	codeRateLimited      = "rate_limited"
	codeOverloaded       = "ingest_overloaded"
	codeShuttingDown     = "shutting_down"
	codeInternal         = "internal_error"
)

// changeKinds maps the changes of an Issue to those the hub publishes.
var changeKinds = map[issues.ChangeKind]hub.Kind{
	issues.ChangeCreated:   hub.IssueCreated,
	issues.ChangeUpdated:   hub.IssueUpdated,
	issues.ChangeRegressed: hub.IssueRegressed,
}

var (
	errTooLarge   = errors.New("body too large")
	errUnreadable = errors.New("body unreadable")
)

// Config configures the ingest endpoint.
type Config struct {
	Projects *projects.Projects
	Queue    *queue.Queue
	// Logger receives the endpoint logs. Nil discards them.
	Logger *slog.Logger
	// RateLimit is the default rate limit of a Project, in Events/s. Zero
	// uses DefaultRateLimit.
	RateLimit float64
	// MaxPending is the largest backlog of the queue, in Events. Zero uses
	// DefaultMaxPending.
	MaxPending int
	// ReportInterval is how often the rejections are logged. Zero uses
	// defaultReportInterval.
	ReportInterval time.Duration
}

// Handler serves the ingest endpoint. Close stops it once the server no
// longer serves it.
type Handler struct {
	config     Config
	logger     *slog.Logger
	limiter    *limiter
	rejections *rejections
	stop       chan struct{}
	reported   chan struct{}
	closeOnce  sync.Once
}

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

func NewHandler(config Config) *Handler {
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if config.RateLimit == 0 {
		config.RateLimit = DefaultRateLimit
	}
	if config.MaxPending == 0 {
		config.MaxPending = DefaultMaxPending
	}
	if config.ReportInterval == 0 {
		config.ReportInterval = defaultReportInterval
	}
	h := &Handler{
		config:     config,
		logger:     logger,
		limiter:    newLimiter(),
		rejections: &rejections{},
		stop:       make(chan struct{}),
		reported:   make(chan struct{}),
	}
	go h.report(config.ReportInterval)
	return h
}

// ServeHTTP answers the CORS preflight of the endpoint and accepts batches
// of Events. Every response allows any origin, without credentials.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Encoding, "+keyHeader)
		w.Header().Set("Access-Control-Max-Age", preflightMaxAge)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		h.accept(w, r)
	default:
		w.Header().Set("Allow", "POST, OPTIONS")
		reject(w, http.StatusMethodNotAllowed, apiError{Code: codeMethod, Message: "the endpoint accepts only POST"})
	}
}

// Close stops logging the rejections, after logging those not yet logged.
// Call it once the server no longer serves the endpoint.
func (h *Handler) Close() {
	h.closeOnce.Do(func() {
		close(h.stop)
		<-h.reported
	})
}

// NewProcess returns the function that processes a queued Event into its
// Issue and returns the change it made, if any. A queued Event it cannot
// decode, or whose Project was deleted, is logged and dropped, since
// retrying it would never succeed.
func NewProcess(logger *slog.Logger) func(ctx context.Context, data *store.DB, payload []byte) ([]hub.Change, error) {
	return func(ctx context.Context, data *store.DB, payload []byte) ([]hub.Change, error) {
		var m message
		if err := json.Unmarshal(payload, &m); err != nil {
			logger.Error("drop undecodable queued Event", "err", err)
			return nil, nil
		}
		change, err := issues.Record(ctx, data, m.ProjectID, m.ReceivedAt, m.Event)
		switch {
		case errors.Is(err, issues.ErrProjectNotFound):
			logger.Warn("drop queued Event of a deleted Project", "project_id", m.ProjectID, "event_id", m.Event.EventID)
			return nil, nil
		case err != nil:
			return nil, err
		case change.Kind == "":
			return nil, nil
		}
		return []hub.Change{{Kind: changeKinds[change.Kind], ProjectID: m.ProjectID, IssueID: change.IssueID}}, nil
	}
}

// accept runs a batch through the steps of the endpoint, in order: the
// Ingest Key, the media type, a first check of the rate limit and the
// backlog, the body limits, the validation, the Redaction, the rate limit
// of the batch, the backlog again, and the commit to the queue. Nothing
// unredacted reaches the queue, and nothing of the client but its Events:
// not its IP.
func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	key, conflict := readKey(r)
	switch {
	case conflict:
		reject(w, http.StatusBadRequest, apiError{Code: codeKeyConflict, Message: "the " + keyHeader + " header and the " + keyParam + " parameter carry different keys"})
		return
	case key == "":
		reject(w, http.StatusUnauthorized, apiError{Code: codeKeyMissing, Message: "the " + keyHeader + " header or the " + keyParam + " parameter is required"})
		return
	}
	project, ok, err := h.config.Projects.Resolve(r.Context(), key)
	if err != nil {
		h.logger.Error("resolve ingest key failed", "err", err)
		reject(w, http.StatusInternalServerError, apiError{Code: codeInternal, Message: "the server failed to resolve the ingest key"})
		return
	}
	if !ok {
		reject(w, http.StatusUnauthorized, apiError{Code: codeKeyInvalid, Message: "the ingest key is unknown or revoked"})
		return
	}

	if !isSupported(r) {
		h.fail(w, project.ID, http.StatusUnsupportedMediaType, apiError{Code: codeUnsupportedMedia, Message: "the body must be application/json or text/plain, optionally gzip"}, true)
		return
	}

	rate := h.config.RateLimit
	if project.RateLimit > 0 {
		rate = project.RateLimit
	}
	// A Project without a token left, or a full backlog, is refused before
	// the body is read.
	if ok, wait := h.limiter.check(project.ID, rate, 1, time.Now()); !ok {
		h.rateLimited(w, project.ID, wait, true)
		return
	}
	if h.config.Queue.Pending() >= h.config.MaxPending {
		h.overloaded(w, project.ID, true)
		return
	}

	body, err := readBody(w, r)
	switch {
	case errors.Is(err, errTooLarge):
		h.fail(w, project.ID, http.StatusRequestEntityTooLarge, apiError{Code: codeTooLarge, Message: "the body exceeds " + strconv.Itoa(maxSent) + " bytes as sent or " + strconv.Itoa(maxBody) + " bytes decompressed"}, true)
		return
	case err != nil:
		h.fail(w, project.ID, http.StatusBadRequest, apiError{Code: codeInvalidEvent, Message: "the body could not be read"}, true)
		return
	}

	events, err := event.DecodeBatch(body)
	if err != nil {
		var batchErr *event.BatchError
		errors.As(err, &batchErr)
		status, code := http.StatusBadRequest, codeInvalidEvent
		if batchErr.TooLarge {
			status, code = http.StatusRequestEntityTooLarge, codeTooLarge
		}
		h.fail(w, project.ID, status, apiError{Code: code, Message: batchErr.Message, Details: batchErr.Details}, false)
		return
	}
	for i := range events {
		events[i] = redaction.Apply(events[i], project.RedactionTerms)
	}

	// Duplicates count too: the rate limit counts the Events sent.
	if ok, wait := h.limiter.take(project.ID, rate, len(events), time.Now()); !ok {
		h.rateLimited(w, project.ID, wait, false)
		return
	}
	if h.config.Queue.Pending()+len(events) > h.config.MaxPending {
		h.limiter.give(project.ID, len(events))
		h.overloaded(w, project.ID, false)
		return
	}

	payloads, err := encode(project.ID, time.Now().UTC(), events)
	if err != nil {
		h.limiter.give(project.ID, len(events))
		h.logger.Error("encode Events failed", "project_id", project.ID, "err", err)
		h.fail(w, project.ID, http.StatusInternalServerError, apiError{Code: codeInternal, Message: "the server failed to accept the Events"}, false)
		return
	}
	err = h.config.Queue.Enqueue(r.Context(), payloads)
	if err != nil {
		h.limiter.give(project.ID, len(events))
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(events)})
	case errors.Is(err, queue.ErrOverloaded), errors.Is(err, queue.ErrFull):
		if errors.Is(err, queue.ErrFull) {
			h.logger.Error("ingest.db is full", "project_id", project.ID, "err", err)
		}
		h.overloaded(w, project.ID, false)
	case errors.Is(err, queue.ErrStopped):
		w.Header().Set("Retry-After", strconv.Itoa(overloadedRetry))
		h.fail(w, project.ID, http.StatusServiceUnavailable, apiError{Code: codeShuttingDown, Message: "the server is shutting down"}, false)
	default:
		h.logger.Error("enqueue Events failed", "project_id", project.ID, "err", err)
		h.fail(w, project.ID, http.StatusInternalServerError, apiError{Code: codeInternal, Message: "the server failed to accept the Events"}, false)
	}
}

// rateLimited answers 429 with the wait, in whole seconds, until the batch
// fits.
func (h *Handler) rateLimited(w http.ResponseWriter, projectID string, wait time.Duration, unread bool) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
	h.fail(w, projectID, http.StatusTooManyRequests, apiError{Code: codeRateLimited, Message: "the Project sent more Events than its rate limit allows"}, unread)
}

// overloaded answers 503 ingest_overloaded.
func (h *Handler) overloaded(w http.ResponseWriter, projectID string, unread bool) {
	w.Header().Set("Retry-After", strconv.Itoa(overloadedRetry))
	h.fail(w, projectID, http.StatusServiceUnavailable, apiError{Code: codeOverloaded, Message: "the server is overloaded; retry later"}, unread)
}

// fail counts a rejection of the Project projectID and writes it; unread
// means the body was not read whole, so the connection closes.
func (h *Handler) fail(w http.ResponseWriter, projectID string, status int, e apiError, unread bool) {
	h.rejections.add(projectID, e.Code)
	if unread {
		reject(w, status, e)
		return
	}
	writeError(w, status, e)
}

// report logs the rejections every interval, and once more on Close.
func (h *Handler) report(interval time.Duration) {
	defer close(h.reported)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.logRejections()
		case <-h.stop:
			h.logRejections()
			return
		}
	}
}

// logRejections logs the rejections counted since the last call, if any.
func (h *Handler) logRejections() {
	if counts := h.rejections.take(); len(counts) > 0 {
		h.logger.Warn("ingest rejections", "rejections", counts)
	}
}

// readKey returns the Ingest Key of r, from the header or else the query,
// and whether both carry different keys.
func readKey(r *http.Request) (string, bool) {
	header := r.Header.Get(keyHeader)
	query := r.URL.Query().Get(keyParam)
	if header != "" && query != "" && header != query {
		return "", true
	}
	if header != "" {
		return header, false
	}
	return query, false
}

// isSupported reports whether the endpoint accepts the Content-Type and
// Content-Encoding of r.
func isSupported(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && mediaType != "text/plain") {
		return false
	}
	switch r.Header.Get("Content-Encoding") {
	case "", "identity", "gzip":
		return true
	}
	return false
}

// readBody reads the body of r, decompressed, and fails with errTooLarge as
// soon as it exceeds a limit, without reading further.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var body io.Reader = http.MaxBytesReader(w, r.Body, maxSent)
	if r.Header.Get("Content-Encoding") == "gzip" {
		decompressed, err := gzip.NewReader(body)
		if err != nil {
			return nil, classify(err)
		}
		body = decompressed
	}

	content, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if err != nil {
		return nil, classify(err)
	}
	if len(content) > maxBody {
		return nil, errTooLarge
	}
	return content, nil
}

// classify returns errTooLarge for an error of a body over maxSent, and
// errUnreadable for any other.
func classify(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return errTooLarge
	}
	return errors.Join(errUnreadable, err)
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

// reject writes an error response without reading the rest of the body, and
// closes the connection so the server does not read the body to reuse it.
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
