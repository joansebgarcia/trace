package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The limits of the protocol. Lengths count characters (Unicode code
// points).
const (
	// MaxEvents is the largest number of Events in a batch.
	MaxEvents = 100
	// maxEventSize is the largest Event, in bytes as sent.
	maxEventSize = 128 << 10
	// maxExceptions is the largest number of exceptions of an Event.
	maxExceptions = 5
	// maxFrames is the largest number of frames of an exception.
	maxFrames = 100
	// maxMessage is the longest message of an exception.
	maxMessage = 8192
	// maxTags is the largest number of tags of an Event.
	maxTags = 50
	// maxTagValue is the longest value of a tag.
	maxTagValue = 200
	// maxUserID is the longest id of a User.
	maxUserID = 200
	// maxExtra is the largest extra, in bytes of compact JSON.
	maxExtra = 16 << 10
	// maxBreadcrumbs is the largest number of Breadcrumbs of an Event.
	maxBreadcrumbs = 100
	// maxBreadcrumbMessage is the longest message of a Breadcrumb.
	maxBreadcrumbMessage = 1024
	// maxGroupKey is the largest number of strings of a Custom Group Key.
	maxGroupKey = 10
	// maxGroupKeyPart is the longest string of a Custom Group Key.
	maxGroupKeyPart = 200
)

// DefaultEnvironment is the Environment of an Event that names none.
const DefaultEnvironment = "production"

// The messages of the errors of DecodeBatch. Those with verbs are formats.
const (
	msgInvalidBody        = "body must be a JSON object with an events array"
	msgBatchSize          = "events must hold between 1 and %d Events"
	msgInvalidEvents      = "the batch has invalid Events"
	msgLargeEvents        = "the batch has Events over %d bytes"
	msgLargeEvent         = "the Event holds more than %d bytes"
	msgNotObject          = "each Event must be a JSON object"
	msgWrongType          = "%s has the wrong type"
	msgInvalidID          = "event_id must be a UUID"
	msgMissingTime        = "timestamp is required"
	msgInvalidTime        = "timestamp must be an RFC 3339 date in UTC with milliseconds"
	msgMissingPlatform    = "platform is required"
	msgMissingSDK         = "sdk.name and sdk.version are required"
	msgExceptionCount     = "exceptions must hold between 1 and %d exceptions"
	msgMissingType        = "exceptions[%d].type is required"
	msgLongMessage        = "exceptions[%d].message holds more than %d characters"
	msgFrameCount         = "exceptions[%d].frames holds more than %d frames"
	msgNegativePosition   = "exceptions[%d].frames[%d] has a negative lineno or colno"
	msgInvalidLevel       = "level must be fatal, error or warning"
	msgInvalidMechanism   = "mechanism must be onerror, unhandledrejection, uncaughtException or manual"
	msgTagCount           = "tags holds more than %d tags"
	msgLongTag            = "tags.%s holds more than %d characters"
	msgUserID             = "user.id must hold between 1 and %d characters"
	msgExtraNotObject     = "extra must be a JSON object"
	msgLargeExtra         = "extra holds more than %d bytes"
	msgBreadcrumbCount    = "breadcrumbs holds more than %d Breadcrumbs"
	msgBreadcrumbTime     = "breadcrumbs[%d].timestamp must be an RFC 3339 date in UTC with milliseconds"
	msgBreadcrumbType     = "breadcrumbs[%d].type is required"
	msgBreadcrumbMessage  = "breadcrumbs[%d].message holds more than %d characters"
	msgBreadcrumbData     = "breadcrumbs[%d].data must be a JSON object"
	msgGroupKeyCount      = "group_key must hold between 1 and %d strings"
	msgGroupKeyPartLength = "group_key[%d] must hold between 1 and %d characters"
)

// The Levels of an Event, from the most to the least severe.
const (
	LevelFatal   Level = "fatal"
	LevelError   Level = "error"
	LevelWarning Level = "warning"
)

// The Mechanisms that capture an Event.
const (
	MechanismOnError            Mechanism = "onerror"
	MechanismUnhandledRejection Mechanism = "unhandledrejection"
	MechanismUncaughtException  Mechanism = "uncaughtException"
	MechanismManual             Mechanism = "manual"
)

var (
	levels     = []Level{LevelFatal, LevelError, LevelWarning}
	mechanisms = []Mechanism{MechanismOnError, MechanismUnhandledRejection, MechanismUncaughtException, MechanismManual}
)

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// timestampPattern is the form of a Timestamp; time.Parse then checks
	// that its date exists.
	timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3,9}(Z|\+00:00)$`)
)

// fieldNames caches, by struct type, the exact JSON names of its fields.
var fieldNames sync.Map

// Event is one Event as an SDK sends it.
type Event struct {
	EventID   string    `json:"event_id"`
	Timestamp Timestamp `json:"timestamp"`
	Platform  string    `json:"platform"`
	SDK       SDK       `json:"sdk"`
	// Level defaults to LevelError.
	Level Level `json:"level"`
	// Handled reports whether the host application caught the error. Nil
	// means the SDK did not tell.
	Handled    *bool       `json:"handled,omitempty"`
	Mechanism  Mechanism   `json:"mechanism,omitempty"`
	Exceptions []Exception `json:"exceptions"`
	// Environment defaults to DefaultEnvironment.
	Environment string            `json:"environment"`
	Release     string            `json:"release,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Contexts    *Contexts         `json:"contexts,omitempty"`
	Request     *Request          `json:"request,omitempty"`
	User        *User             `json:"user,omitempty"`
	// Extra is a free JSON object, compacted.
	Extra json.RawMessage `json:"extra,omitempty"`
	// Breadcrumbs go from the oldest to the newest.
	Breadcrumbs []Breadcrumb `json:"breadcrumbs,omitempty"`
	// GroupKey is the Custom Group Key, if the SDK sent one.
	GroupKey []string `json:"group_key,omitempty"`
}

// UnmarshalJSON decodes an Event, ignoring the keys that only match a field
// when case is ignored.
func (e *Event) UnmarshalJSON(data []byte) error {
	type plain Event
	return decodeExact(data, (*plain)(e))
}

// Timestamp is an RFC 3339 date in UTC, with a fraction of at least
// milliseconds, kept as the SDK sent it.
type Timestamp string

// Level is the severity of an Event.
type Level string

// UnmarshalJSON decodes a Level, rejecting an empty one.
func (l *Level) UnmarshalJSON(data []byte) error {
	return decodeNonEmpty(data, (*string)(l), msgInvalidLevel)
}

// Mechanism is how the SDK captured an Event.
type Mechanism string

// UnmarshalJSON decodes a Mechanism, rejecting an empty one.
func (m *Mechanism) UnmarshalJSON(data []byte) error {
	return decodeNonEmpty(data, (*string)(m), msgInvalidMechanism)
}

// SDK names the SDK that sent an Event.
type SDK struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// UnmarshalJSON decodes an SDK with exact field names.
func (s *SDK) UnmarshalJSON(data []byte) error {
	type plain SDK
	return decodeExact(data, (*plain)(s))
}

// Contexts describes where an Event happened. Each context maps names,
// such as "name" and "version", to values.
type Contexts struct {
	Runtime map[string]string `json:"runtime,omitempty"`
	OS      map[string]string `json:"os,omitempty"`
	Browser map[string]string `json:"browser,omitempty"`
}

// UnmarshalJSON decodes Contexts with exact field names.
func (c *Contexts) UnmarshalJSON(data []byte) error {
	type plain Contexts
	return decodeExact(data, (*plain)(c))
}

// Request is the HTTP request the host application was serving.
type Request struct {
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`
}

// UnmarshalJSON decodes a Request with exact field names.
func (r *Request) UnmarshalJSON(data []byte) error {
	type plain Request
	return decodeExact(data, (*plain)(r))
}

// User is the User the host application was serving.
type User struct {
	ID string `json:"id"`
}

// UnmarshalJSON decodes a User with exact field names.
func (u *User) UnmarshalJSON(data []byte) error {
	type plain User
	return decodeExact(data, (*plain)(u))
}

// Exception is one error of an Event, from the outermost to its cause.
type Exception struct {
	Type    string  `json:"type"`
	Message string  `json:"message"`
	Frames  []Frame `json:"frames"`
	// RawStack is the stack trace as the runtime printed it.
	RawStack string `json:"raw_stack,omitempty"`
}

// UnmarshalJSON decodes an Exception with exact field names.
func (e *Exception) UnmarshalJSON(data []byte) error {
	type plain Exception
	return decodeExact(data, (*plain)(e))
}

// Frame is one stack frame of an Exception, as the SDK parsed it.
type Frame struct {
	Function string `json:"function"`
	Filename string `json:"filename"`
	// Lineno and Colno count from 1; 0 means unknown.
	Lineno int `json:"lineno,omitempty"`
	Colno  int `json:"colno,omitempty"`
	// InApp marks an In-app frame. Nil means the SDK did not tell.
	InApp *bool `json:"in_app,omitempty"`
}

// UnmarshalJSON decodes a Frame with exact field names.
func (f *Frame) UnmarshalJSON(data []byte) error {
	type plain Frame
	return decodeExact(data, (*plain)(f))
}

// IsInApp reports whether the SDK marked f as an In-app frame.
func (f Frame) IsInApp() bool {
	return f.InApp != nil && *f.InApp
}

// Breadcrumb is one record of what happened before an Event.
type Breadcrumb struct {
	Timestamp Timestamp `json:"timestamp"`
	// Type is free, such as "navigation", "http", "console", "ui" or
	// "custom".
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	// Data is a free JSON object, compacted.
	Data json.RawMessage `json:"data,omitempty"`
}

// UnmarshalJSON decodes a Breadcrumb with exact field names.
func (b *Breadcrumb) UnmarshalJSON(data []byte) error {
	type plain Breadcrumb
	return decodeExact(data, (*plain)(b))
}

// BatchError is a batch that breaks the protocol. Details lists the invalid
// Events, if the batch itself is well formed. TooLarge means those Events
// break a size limit rather than a rule of their content.
type BatchError struct {
	Message  string
	Details  []Detail
	TooLarge bool
}

func (e *BatchError) Error() string {
	return e.Message
}

// Detail is the reason one Event of a batch is invalid.
type Detail struct {
	// Index is the position of the Event in the batch, from 0.
	Index   int    `json:"index"`
	Message string `json:"message"`
}

// batch is the body of a request.
type batch struct {
	Events []json.RawMessage `json:"events"`
}

// DecodeBatch decodes a body of the form {"events": [...]}. The batch is all
// or nothing: if any Event is invalid, it returns a *BatchError listing
// every invalid one, or only those over the size limit if there are any.
// Field names are case-sensitive, and unknown fields, case variants
// included, are ignored.
func DecodeBatch(body []byte) ([]Event, error) {
	var b batch
	if err := decodeExact(body, &b); err != nil {
		return nil, &BatchError{Message: msgInvalidBody}
	}
	if len(b.Events) == 0 || len(b.Events) > MaxEvents {
		return nil, &BatchError{Message: fmt.Sprintf(msgBatchSize, MaxEvents)}
	}

	var large []Detail
	for i, raw := range b.Events {
		if len(raw) > maxEventSize {
			large = append(large, Detail{Index: i, Message: fmt.Sprintf(msgLargeEvent, maxEventSize)})
		}
	}
	if large != nil {
		return nil, &BatchError{Message: fmt.Sprintf(msgLargeEvents, maxEventSize), Details: large, TooLarge: true}
	}

	events := make([]Event, len(b.Events))
	var details []Detail
	for i, raw := range b.Events {
		event, err := decodeEvent(raw)
		if err != nil {
			details = append(details, Detail{Index: i, Message: err.Error()})
			continue
		}
		events[i] = event
	}
	if details != nil {
		return nil, &BatchError{Message: msgInvalidEvents, Details: details}
	}
	return events, nil
}

func decodeEvent(raw json.RawMessage) (Event, error) {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return Event{}, errors.New(msgNotObject)
	}
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return Event{}, fmt.Errorf(msgWrongType, typeErr.Field)
		}
		// The Event is valid JSON, so any other failure is a rule of an
		// UnmarshalJSON method, such as an empty level.
		return Event{}, err
	}
	event.EventID = strings.ToLower(event.EventID)
	if event.Level == "" {
		event.Level = LevelError
	}
	if event.Environment == "" {
		event.Environment = DefaultEnvironment
	}

	switch {
	case !uuidPattern.MatchString(event.EventID):
		return Event{}, errors.New(msgInvalidID)
	case event.Timestamp == "":
		return Event{}, errors.New(msgMissingTime)
	case !isTimestamp(event.Timestamp):
		return Event{}, errors.New(msgInvalidTime)
	case event.Platform == "":
		return Event{}, errors.New(msgMissingPlatform)
	case event.SDK.Name == "" || event.SDK.Version == "":
		return Event{}, errors.New(msgMissingSDK)
	case len(event.Exceptions) == 0 || len(event.Exceptions) > maxExceptions:
		return Event{}, fmt.Errorf(msgExceptionCount, maxExceptions)
	case !slices.Contains(levels, event.Level):
		return Event{}, errors.New(msgInvalidLevel)
	case event.Mechanism != "" && !slices.Contains(mechanisms, event.Mechanism):
		return Event{}, errors.New(msgInvalidMechanism)
	}
	for i, exception := range event.Exceptions {
		if err := checkException(i, exception); err != nil {
			return Event{}, err
		}
	}
	if err := checkTags(event.Tags); err != nil {
		return Event{}, err
	}
	if err := checkUser(event.User); err != nil {
		return Event{}, err
	}
	extra, err := checkExtra(event.Extra)
	if err != nil {
		return Event{}, err
	}
	event.Extra = extra
	if err := checkBreadcrumbs(event.Breadcrumbs); err != nil {
		return Event{}, err
	}
	if err := checkGroupKey(event.GroupKey); err != nil {
		return Event{}, err
	}
	return event, nil
}

// checkException checks exceptions[i] of an Event.
func checkException(i int, exception Exception) error {
	switch {
	case exception.Type == "":
		return fmt.Errorf(msgMissingType, i)
	case utf8.RuneCountInString(exception.Message) > maxMessage:
		return fmt.Errorf(msgLongMessage, i, maxMessage)
	case len(exception.Frames) > maxFrames:
		return fmt.Errorf(msgFrameCount, i, maxFrames)
	}
	for j, frame := range exception.Frames {
		if frame.Lineno < 0 || frame.Colno < 0 {
			return fmt.Errorf(msgNegativePosition, i, j)
		}
	}
	return nil
}

// checkTags checks the number of tags and the length of their values.
func checkTags(tags map[string]string) error {
	if len(tags) > maxTags {
		return fmt.Errorf(msgTagCount, maxTags)
	}
	for name, value := range tags {
		if utf8.RuneCountInString(value) > maxTagValue {
			return fmt.Errorf(msgLongTag, name, maxTagValue)
		}
	}
	return nil
}

// checkUser checks the id of user, if the Event has one.
func checkUser(user *User) error {
	if user == nil {
		return nil
	}
	if n := utf8.RuneCountInString(user.ID); n == 0 || n > maxUserID {
		return fmt.Errorf(msgUserID, maxUserID)
	}
	return nil
}

// checkExtra checks extra and returns it compacted.
func checkExtra(extra json.RawMessage) (json.RawMessage, error) {
	compact, err := compactObject(extra)
	switch {
	case err != nil:
		return nil, errors.New(msgExtraNotObject)
	case len(compact) > maxExtra:
		return nil, fmt.Errorf(msgLargeExtra, maxExtra)
	}
	return compact, nil
}

// checkBreadcrumbs checks breadcrumbs and compacts their data.
func checkBreadcrumbs(breadcrumbs []Breadcrumb) error {
	if len(breadcrumbs) > maxBreadcrumbs {
		return fmt.Errorf(msgBreadcrumbCount, maxBreadcrumbs)
	}
	for i := range breadcrumbs {
		breadcrumb := &breadcrumbs[i]
		switch {
		case !isTimestamp(breadcrumb.Timestamp):
			return fmt.Errorf(msgBreadcrumbTime, i)
		case breadcrumb.Type == "":
			return fmt.Errorf(msgBreadcrumbType, i)
		case utf8.RuneCountInString(breadcrumb.Message) > maxBreadcrumbMessage:
			return fmt.Errorf(msgBreadcrumbMessage, i, maxBreadcrumbMessage)
		}
		data, err := compactObject(breadcrumb.Data)
		if err != nil {
			return fmt.Errorf(msgBreadcrumbData, i)
		}
		breadcrumb.Data = data
	}
	return nil
}

// checkGroupKey checks a Custom Group Key. Nil means the SDK sent none.
func checkGroupKey(key []string) error {
	if key == nil {
		return nil
	}
	if len(key) == 0 || len(key) > maxGroupKey {
		return fmt.Errorf(msgGroupKeyCount, maxGroupKey)
	}
	for i, part := range key {
		if n := utf8.RuneCountInString(part); n == 0 || n > maxGroupKeyPart {
			return fmt.Errorf(msgGroupKeyPartLength, i, maxGroupKeyPart)
		}
	}
	return nil
}

// isTimestamp reports whether t is an RFC 3339 date in UTC with at least
// milliseconds.
func isTimestamp(t Timestamp) bool {
	if !timestampPattern.MatchString(string(t)) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, string(t))
	return err == nil
}

// compactObject compacts raw, a JSON object or null. It returns nil for
// null or an absent value.
func compactObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if !bytes.HasPrefix(raw, []byte("{")) {
		return nil, errors.New("not a JSON object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

// decodeExact decodes the JSON object data into v, a pointer to a struct
// without an UnmarshalJSON method. It first drops the keys that are not the
// exact JSON name of a field, which encoding/json would match ignoring case.
func decodeExact(data []byte, v any) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	names := exactNames(reflect.TypeOf(v).Elem())
	maps.DeleteFunc(object, func(key string, _ json.RawMessage) bool { return !names[key] })
	exact, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return json.Unmarshal(exact, v)
}

// exactNames returns the set of JSON names of the fields of the struct t.
func exactNames(t reflect.Type) map[string]bool {
	if names, ok := fieldNames.Load(t); ok {
		return names.(map[string]bool)
	}
	names := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	fieldNames.Store(t, names)
	return names
}

// decodeNonEmpty decodes the JSON string data into s, leaving s as it is
// for null. An empty string fails with message.
func decodeNonEmpty(data []byte, s *string, message string) error {
	if string(data) == "null" {
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == "" {
		return errors.New(message)
	}
	*s = value
	return nil
}
