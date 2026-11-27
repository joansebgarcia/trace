package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// MaxEvents is the largest number of Events in a batch.
const MaxEvents = 100

// The messages of the errors of DecodeBatch. Those with verbs are formats.
const (
	msgInvalidBody     = "body must be a JSON object with an events array"
	msgBatchSize       = "events must hold between 1 and %d Events"
	msgInvalidEvents   = "the batch has invalid Events"
	msgNotObject       = "each Event must be a JSON object"
	msgWrongType       = "%s has the wrong type"
	msgInvalidTime     = "timestamp must be an RFC 3339 date"
	msgInvalidID       = "event_id must be a UUID"
	msgMissingTime     = "timestamp is required"
	msgNotUTC          = "timestamp must be in UTC"
	msgMissingPlatform = "platform is required"
	msgMissingSDK      = "sdk.name and sdk.version are required"
	msgNoExceptions    = "exceptions must hold at least one exception"
	msgMissingType     = "exceptions[%d].type is required"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Event is one Event as an SDK sends it.
type Event struct {
	EventID    string      `json:"event_id"`
	Timestamp  time.Time   `json:"timestamp"`
	Platform   string      `json:"platform"`
	SDK        SDK         `json:"sdk"`
	Exceptions []Exception `json:"exceptions"`
}

// SDK names the SDK that sent an Event.
type SDK struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Exception is one error of an Event, from the outermost to its cause.
type Exception struct {
	Type    string  `json:"type"`
	Message string  `json:"message"`
	Frames  []Frame `json:"frames"`
}

// Frame is one stack frame of an Exception, as the SDK parsed it.
type Frame struct {
	Function string `json:"function"`
	Filename string `json:"filename"`
	Lineno   int    `json:"lineno,omitempty"`
	Colno    int    `json:"colno,omitempty"`
	// InApp marks an In-app frame. Nil means the SDK did not tell.
	InApp *bool `json:"in_app,omitempty"`
}

// IsInApp reports whether the SDK marked f as an In-app frame.
func (f Frame) IsInApp() bool {
	return f.InApp != nil && *f.InApp
}

// BatchError is a batch that breaks the protocol. Details lists the invalid
// Events, if the batch itself is well formed.
type BatchError struct {
	Message string
	Details []Detail
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

// DecodeBatch decodes a body of the form {"events": [...]}. The batch is all
// or nothing: if any Event is invalid, it returns a *BatchError listing
// every invalid one. Unknown fields are ignored.
func DecodeBatch(body []byte) ([]Event, error) {
	var batch struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, &BatchError{Message: msgInvalidBody}
	}
	if len(batch.Events) == 0 || len(batch.Events) > MaxEvents {
		return nil, &BatchError{Message: fmt.Sprintf(msgBatchSize, MaxEvents)}
	}

	events := make([]Event, len(batch.Events))
	var details []Detail
	for i, raw := range batch.Events {
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
		// The Event is valid JSON, so the only other failure is a
		// timestamp that is not RFC 3339.
		return Event{}, errors.New(msgInvalidTime)
	}
	event.EventID = strings.ToLower(event.EventID)

	switch {
	case !uuidPattern.MatchString(event.EventID):
		return Event{}, errors.New(msgInvalidID)
	case event.Timestamp.IsZero():
		return Event{}, errors.New(msgMissingTime)
	case !isUTC(event.Timestamp):
		return Event{}, errors.New(msgNotUTC)
	case event.Platform == "":
		return Event{}, errors.New(msgMissingPlatform)
	case event.SDK.Name == "" || event.SDK.Version == "":
		return Event{}, errors.New(msgMissingSDK)
	case len(event.Exceptions) == 0:
		return Event{}, errors.New(msgNoExceptions)
	}
	for i, exception := range event.Exceptions {
		if exception.Type == "" {
			return Event{}, fmt.Errorf(msgMissingType, i)
		}
	}
	return event, nil
}

func isUTC(t time.Time) bool {
	_, offset := t.Zone()
	return offset == 0
}
