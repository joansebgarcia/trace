// Package grouping computes the Group Key that decides which Issue an Event
// belongs to.
package grouping

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/joansgarcia/trace/event"
)

// Version is the version of the algorithm Compute applies. A new version
// applies only to Events grouped after it, and may open new Issues.
const Version = 1

// minHash is the shortest token of a filename taken for a bundler hash.
const minHash = 8

// The patterns of the variable parts of a message, replaced in order.
var placeholders = []struct {
	pattern     *regexp.Regexp
	placeholder string
}{
	{regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`), "<url>"},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "<email>"},
	{regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "<uuid>"},
	{regexp.MustCompile(`(?i)\b0x[0-9a-f]+\b`), "<hex>"},
}

var (
	// quotedPattern finds a quoted literal, after the character before it
	// so that the apostrophe of a word, as in "can't", does not open one.
	quotedPattern = regexp.MustCompile("(^|[^A-Za-z0-9])(\"[^\"]*\"|'[^']*'|`[^`]*`)")
	// longHexPattern finds runs of hex digits long enough to be ids.
	longHexPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b`)
	numberPattern  = regexp.MustCompile(`\b\d+(?:\.\d+)?\b`)
	hashPattern    = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// Group is the Group Key of an Event with what it was computed from.
type Group struct {
	// Version is the version of the algorithm.
	Version int
	// Key is the hex SHA-256 of the components. It is unique within a
	// Project, not across Projects.
	Key string
	// Components are the readable parts of the Event behind Key, in order,
	// such as "type: TypeError" or "frame: render @ src/app.js".
	Components []string
}

// Compute returns the Group of e. A Custom Group Key replaces the
// computation. Otherwise the components are, for each exception from the
// outermost to its last cause, its type and its In-app frames as function
// and normalized filename, without line, column or message. Without In-app
// frames, every frame counts; without frames, the type and the message with
// its variable parts replaced.
func Compute(e event.Event) Group {
	var components []string
	switch {
	case len(e.GroupKey) > 0:
		for _, part := range e.GroupKey {
			components = append(components, "custom: "+part)
		}
	case hasFrames(e, true):
		components = frameComponents(e, true)
	case hasFrames(e, false):
		components = frameComponents(e, false)
	default:
		for _, exception := range e.Exceptions {
			components = append(components, "type: "+exception.Type, "message: "+normalizeMessage(exception.Message))
		}
	}

	// A JSON array keeps the components apart, whatever they contain.
	encoded, _ := json.Marshal(append([]string{"v" + strconv.Itoa(Version)}, components...))
	sum := sha256.Sum256(encoded)
	return Group{Version: Version, Key: hex.EncodeToString(sum[:]), Components: components}
}

// NormalizeFilename returns filename without scheme, host, query or
// fragment, with bundler hashes replaced by <hash>, cut to its last two
// segments, so the same file groups alike across deploys.
func NormalizeFilename(filename string) string {
	filename = strings.ReplaceAll(filename, `\`, "/")
	if _, rest, ok := strings.Cut(filename, "://"); ok {
		filename = ""
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			filename = rest[slash:]
		}
	}
	if end := strings.IndexAny(filename, "?#"); end >= 0 {
		filename = filename[:end]
	}

	var segments []string
	for _, segment := range strings.Split(filename, "/") {
		if segment != "" {
			segments = append(segments, replaceHashes(segment))
		}
	}
	if len(segments) > 2 {
		segments = segments[len(segments)-2:]
	}
	return strings.Join(segments, "/")
}

// hasFrames reports whether an exception of e has a frame with a filename
// that is an In-app frame, or any such frame unless inApp.
func hasFrames(e event.Event, inApp bool) bool {
	for _, exception := range e.Exceptions {
		for _, frame := range exception.Frames {
			if frame.Filename != "" && (!inApp || frame.IsInApp()) {
				return true
			}
		}
	}
	return false
}

// frameComponents returns the type and the frames of each exception of e,
// only In-app frames if inApp. It drops frames without a filename and
// collapses consecutive equal frames, as recursion repeats them.
func frameComponents(e event.Event, inApp bool) []string {
	var components []string
	for _, exception := range e.Exceptions {
		components = append(components, "type: "+exception.Type)
		previous := ""
		for _, frame := range exception.Frames {
			if frame.Filename == "" || (inApp && !frame.IsInApp()) {
				continue
			}
			component := "frame: " + NormalizeFilename(frame.Filename)
			if function := frame.Function; function != "" && function != "<anonymous>" {
				component = "frame: " + function + " @ " + NormalizeFilename(frame.Filename)
			}
			if component != previous {
				components = append(components, component)
			}
			previous = component
		}
	}
	return components
}

// normalizeMessage replaces the variable parts of message, such as ids and
// numbers, with placeholders.
func normalizeMessage(message string) string {
	for _, p := range placeholders {
		message = p.pattern.ReplaceAllString(message, p.placeholder)
	}
	message = quotedPattern.ReplaceAllString(message, "${1}<str>")
	message = longHexPattern.ReplaceAllStringFunc(message, func(s string) string {
		if strings.ContainsAny(s, "0123456789") {
			return "<hex>"
		}
		return s
	})
	return numberPattern.ReplaceAllString(message, "<num>")
}

// replaceHashes replaces the tokens of segment, split by "." and "-", that
// look like bundler hashes: long, alphanumeric, with a digit.
func replaceHashes(segment string) string {
	tokens := strings.FieldsFunc(segment, func(r rune) bool { return r == '.' || r == '-' })
	for _, token := range tokens {
		if len(token) >= minHash && hashPattern.MatchString(token) && strings.ContainsAny(token, "0123456789") {
			segment = strings.Replace(segment, token, "<hash>", 1)
		}
	}
	return segment
}
