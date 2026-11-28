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
	codeInvalidEvent     code = "invalid_event"
	codeKeyMissing       code = "ingest_key_missing"
	codeKeyInvalid       code = "ingest_key_invalid"
	codeKeyConflict      code = "ingest_key_conflict"
	codeTooLarge         code = "payload_too_large"
	codeUnsupportedMedia code = "unsupported_media_type"
	codeMethod           code = "method_not_allowed"
	codeRateLimited      code = "rate_limited"
	codeOverloaded       code = "ingest_overloaded"
	codeShuttingDown     code = "shutting_down"
	codeInternal         code = "internal_error"
)

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

// code is the stable, snake_case code of an error response.
type code string

// message is one accepted Event as it waits in the queue.
type message struct {
	ProjectID  string      `json:"project_id"`
	ReceivedAt time.Time   `json:"received_at"`
	Event      event.Event `json:"event"`
}

// apiError is the body of every error response of the endpoint.
type apiError struct {
	Code    code           `json:"code"`
	Message string         `json:"message"`
	Details []event.Detail `json:"details"`
}

// attempt is one authenticated request for a batch, which counts its
// rejection, if any, to its Project.
type attempt struct {
	w          http.ResponseWriter
	projectID  string
	rejections *rejections
	// bodyRead is set once the whole body is read, so the connection can
	// serve another request.
	bodyRead bool
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
		case errors.Is(err, projects.ErrNotFound):
			logger.Warn("drop queued Event of a deleted Project", "project_id", m.ProjectID, "event_id", m.Event.EventID)
			return nil, nil
		case err != nil:
			return nil, err
		case change == hub.Change{}:
			return nil, nil
		}
		return []hub.Change{change}, nil
	}
}

// accept runs a batch through the steps of the endpoint: authenticate its
// Ingest Key, read its Events, and commit them to the queue. Nothing
// unredacted reaches the queue, and nothing of the client but its Events:
// not its IP.
func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	project, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	a := &attempt{w: w, projectID: project.ID, rejections: h.rejections}
	rate := h.chooseRate(project)
	events, ok := h.readEvents(a, r, project, rate)
	if !ok {
		return
	}
	h.enqueue(r.Context(), a, rate, events)
}

// authenticate resolves the Ingest Key of r to its Project, before reading
// the body, and answers the request itself if it cannot.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	key, conflict := readKey(r)
	switch {
	case conflict:
		reject(w, http.StatusBadRequest, apiError{Code: codeKeyConflict, Message: "the " + keyHeader + " header and the " + keyParam + " parameter carry different keys"})
		return projects.Project{}, false
	case key == "":
		reject(w, http.StatusUnauthorized, apiError{Code: codeKeyMissing, Message: "the " + keyHeader + " header or the " + keyParam + " parameter is required"})
		return projects.Project{}, false
	}
	project, ok, err := h.config.Projects.Resolve(r.Context(), key)
	if err != nil {
		h.logger.Error("resolve ingest key failed", "err", err)
		reject(w, http.StatusInternalServerError, apiError{Code: codeInternal, Message: "the server failed to resolve the ingest key"})
		return projects.Project{}, false
	}
	if !ok {
		reject(w, http.StatusUnauthorized, apiError{Code: codeKeyInvalid, Message: "the ingest key is unknown or revoked"})
		return projects.Project{}, false
	}
	return project, true
}

// readEvents reads the Events of r, validated and redacted. Before reading
// the body, it checks the media type, that the Project has a token left,
// and that the backlog has room: only the body tells how many Events the
// batch holds, so the rate limit of the whole batch comes in enqueue.
func (h *Handler) readEvents(a *attempt, r *http.Request, project projects.Project, rate float64) ([]event.Event, bool) {
	if !isSupported(r) {
		a.fail(http.StatusUnsupportedMediaType, apiError{Code: codeUnsupportedMedia, Message: "the body must be application/json or text/plain, optionally gzip"})
		return nil, false
	}
	if ok, wait := h.limiter.check(project.ID, rate, 1, time.Now()); !ok {
		a.rejectRateLimited(wait)
		return nil, false
	}
	if h.config.Queue.Pending() >= h.config.MaxPending {
		a.rejectOverloaded()
		return nil, false
	}

	body, err := readBody(a.w, r)
	switch {
	case errors.Is(err, errTooLarge):
		a.fail(http.StatusRequestEntityTooLarge, apiError{Code: codeTooLarge, Message: "the body exceeds " + strconv.Itoa(maxSent) + " bytes as sent or " + strconv.Itoa(maxBody) + " bytes decompressed"})
		return nil, false
	case err != nil:
		a.fail(http.StatusBadRequest, apiError{Code: codeInvalidEvent, Message: "the body could not be read"})
		return nil, false
	}
	a.bodyRead = true

	events, err := event.DecodeBatch(body)
	if err != nil {
		var batchErr *event.BatchError
		errors.As(err, &batchErr)
		status, c := http.StatusBadRequest, codeInvalidEvent
		if batchErr.TooLarge {
			status, c = http.StatusRequestEntityTooLarge, codeTooLarge
		}
		a.fail(status, apiError{Code: c, Message: batchErr.Message, Details: batchErr.Details})
		return nil, false
	}
	for i := range events {
		events[i] = Redact(events[i], project.RedactionTerms)
	}
	return events, true
}

// enqueue takes the tokens of events, at rate, and commits them to the
// queue if the backlog has room for them, answering 202 once committed. A
// batch the queue refuses gives its tokens back.
func (h *Handler) enqueue(ctx context.Context, a *attempt, rate float64, events []event.Event) {
	// Duplicates count too: the rate limit counts the Events sent.
	if ok, wait := h.limiter.take(a.projectID, rate, len(events), time.Now()); !ok {
		a.rejectRateLimited(wait)
		return
	}
	if h.config.Queue.Pending()+len(events) > h.config.MaxPending {
		h.limiter.give(a.projectID, len(events))
		a.rejectOverloaded()
		return
	}

	err := h.commit(ctx, a.projectID, events)
	if err != nil {
		h.limiter.give(a.projectID, len(events))
	}
	switch {
	case err == nil:
		writeJSON(a.w, http.StatusAccepted, map[string]int{"accepted": len(events)})
	case errors.Is(err, queue.ErrOverloaded), errors.Is(err, queue.ErrFull):
		if errors.Is(err, queue.ErrFull) {
			h.logger.Error("ingest.db is full", "project_id", a.projectID, "err", err)
		}
		a.rejectOverloaded()
	case errors.Is(err, queue.ErrStopped):
		a.w.Header().Set("Retry-After", strconv.Itoa(overloadedRetry))
		a.fail(http.StatusServiceUnavailable, apiError{Code: codeShuttingDown, Message: "the server is shutting down"})
	default:
		h.logger.Error("enqueue Events failed", "project_id", a.projectID, "err", err)
		a.fail(http.StatusInternalServerError, apiError{Code: codeInternal, Message: "the server failed to accept the Events"})
	}
}

// commit encodes events for the Project projectID, received now, and
// commits them to the queue.
func (h *Handler) commit(ctx context.Context, projectID string, events []event.Event) error {
	receivedAt := time.Now().UTC()
	payloads := make([][]byte, len(events))
	for i, e := range events {
		payload, err := json.Marshal(message{ProjectID: projectID, ReceivedAt: receivedAt, Event: e})
		if err != nil {
			return err
		}
		payloads[i] = payload
	}
	return h.config.Queue.Enqueue(ctx, payloads)
}

// chooseRate returns the rate limit of project: its override, or else the
// default.
func (h *Handler) chooseRate(project projects.Project) float64 {
	if project.RateLimit > 0 {
		return project.RateLimit
	}
	return h.config.RateLimit
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

// fail counts the rejection of a and writes it. Before the whole body is
// read, the connection closes, so the server does not read the rest.
func (a *attempt) fail(status int, e apiError) {
	a.rejections.add(a.projectID, e.Code)
	if !a.bodyRead {
		reject(a.w, status, e)
		return
	}
	writeError(a.w, status, e)
}

// rejectRateLimited answers 429 with the wait, in whole seconds, until the
// batch fits.
func (a *attempt) rejectRateLimited(wait time.Duration) {
	a.w.Header().Set("Retry-After", strconv.Itoa(roundUpSeconds(wait)))
	a.fail(http.StatusTooManyRequests, apiError{Code: codeRateLimited, Message: "the Project sent more Events than its rate limit allows"})
}

// rejectOverloaded answers 503 ingest_overloaded.
func (a *attempt) rejectOverloaded() {
	a.w.Header().Set("Retry-After", strconv.Itoa(overloadedRetry))
	a.fail(http.StatusServiceUnavailable, apiError{Code: codeOverloaded, Message: "the server is overloaded; retry later"})
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
