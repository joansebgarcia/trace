// Package redaction replaces the sensitive values of an Event with a fixed
// marker before it is stored, by the rules of docs/protocol/v1/redaction.md.
package redaction

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/joansgarcia/trace/event"
)

// Marker replaces every redacted value or fragment.
const Marker = "[redacted]"

// DefaultTerms are the key terms every Project redacts. A key is sensitive
// when, lowercased and without - or _, it holds one of them.
var DefaultTerms = []string{
	"password", "passwd", "pwd", "secret", "token", "apikey", "auth",
	"authorization", "cookie", "session", "csrf", "xsrf", "credential",
	"privatekey", "card", "cvv", "ssn",
}

// The patterns of sensitive fragments of a free string, applied in order.
var (
	jwtPattern      = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	bearerPattern   = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9\-._~+/]+=*`)
	userinfoPattern = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.-]*://)[^\s/?#@:]+:[^\s/?#@]*@`)
	// cardPattern finds runs of 13 to 19 digits, optionally grouped by
	// spaces or dashes; isCard then checks their Luhn digit.
	cardPattern = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)
	// urlPattern tells a string that is a URL with a scheme.
	urlPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)
)

// rules are the key terms of one Project, normalized.
type rules struct {
	terms []string
}

// Apply returns e with its sensitive values redacted, using DefaultTerms
// plus terms, the extra key terms of its Project. It leaves e as it is.
//
// Values under a sensitive key are redacted whole, in extra (at any
// depth), contexts, tags, Breadcrumb data and the query of URLs. Cards,
// Bearer tokens, JWTs and the user:pass@ of URLs are redacted as fragments
// of messages, raw stacks, Breadcrumb messages, extra and URLs. type,
// frames, event_id, timestamp, platform, sdk, level, environment, release
// and the Custom Group Key are never redacted.
func Apply(e event.Event, terms []string) event.Event {
	r := newRules(terms)

	e.Exceptions = slices.Clone(e.Exceptions)
	for i := range e.Exceptions {
		exception := &e.Exceptions[i]
		exception.Message = redactText(exception.Message)
		exception.RawStack = redactText(exception.RawStack)
	}
	e.Tags = r.redactMap(e.Tags)
	if e.Contexts != nil {
		contexts := *e.Contexts
		contexts.Runtime = r.redactMap(contexts.Runtime)
		contexts.OS = r.redactMap(contexts.OS)
		contexts.Browser = r.redactMap(contexts.Browser)
		e.Contexts = &contexts
	}
	if e.Request != nil {
		request := *e.Request
		request.URL = r.redactURL(request.URL)
		e.Request = &request
	}
	e.Extra = r.redactObject(e.Extra, r.redactString)
	e.Breadcrumbs = slices.Clone(e.Breadcrumbs)
	for i := range e.Breadcrumbs {
		breadcrumb := &e.Breadcrumbs[i]
		breadcrumb.Message = redactText(breadcrumb.Message)
		breadcrumb.Data = r.redactObject(breadcrumb.Data, r.redactDataString)
	}
	return e
}

func newRules(terms []string) rules {
	all := make([]string, 0, len(DefaultTerms)+len(terms))
	for _, term := range slices.Concat(DefaultTerms, terms) {
		if term := normalize(term); term != "" {
			all = append(all, term)
		}
	}
	return rules{terms: all}
}

// isSensitive reports whether key holds one of the terms of r.
func (r rules) isSensitive(key string) bool {
	key = normalize(key)
	for _, term := range r.terms {
		if strings.Contains(key, term) {
			return true
		}
	}
	return false
}

// redactMap returns m with the values of its sensitive keys redacted.
func (r rules) redactMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	m = maps.Clone(m)
	for key := range m {
		if r.isSensitive(key) {
			m[key] = Marker
		}
	}
	return m
}

// redactObject returns raw, a JSON object, with the values of its sensitive
// keys redacted at any depth, and every other string passed through text.
func (r rules) redactObject(raw json.RawMessage, text func(string) string) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		// Validation compacted raw as a JSON object, so this never happens;
		// redact it whole rather than store it unredacted.
		return json.RawMessage(`{}`)
	}

	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(r.redactValue(value, text)); err != nil {
		return json.RawMessage(`{}`)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

func (r rules) redactValue(value any, text func(string) string) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if r.isSensitive(key) {
				v[key] = Marker
				continue
			}
			v[key] = r.redactValue(child, text)
		}
	case []any:
		for i, child := range v {
			v[i] = r.redactValue(child, text)
		}
	case string:
		return text(v)
	}
	return value
}

// redactString redacts a free string of extra: a URL as a URL, any other
// by its patterns.
func (r rules) redactString(s string) string {
	if urlPattern.MatchString(s) {
		return r.redactURL(s)
	}
	return redactText(s)
}

// redactDataString redacts a string of Breadcrumb data, which only URLs
// have rules for.
func (r rules) redactDataString(s string) string {
	if urlPattern.MatchString(s) {
		return r.redactURL(s)
	}
	return s
}

// redactURL redacts the values of the sensitive query parameters of u and
// its fragments. It edits u as text, so the rest stays byte for byte.
func (r rules) redactURL(u string) string {
	u = redactText(u)
	start := strings.IndexByte(u, '?')
	if start < 0 {
		return u
	}
	end := len(u)
	if hash := strings.IndexByte(u[start:], '#'); hash >= 0 {
		end = start + hash
	}

	params := strings.Split(u[start+1:end], "&")
	for i, param := range params {
		key, _, hasValue := strings.Cut(param, "=")
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		if hasValue && r.isSensitive(key) {
			name, _, _ := strings.Cut(param, "=")
			params[i] = name + "=" + Marker
		}
	}
	return u[:start+1] + strings.Join(params, "&") + u[end:]
}

// redactText redacts the sensitive fragments of s.
func redactText(s string) string {
	if s == "" {
		return s
	}
	s = jwtPattern.ReplaceAllString(s, Marker)
	s = bearerPattern.ReplaceAllString(s, "${1} "+Marker)
	s = userinfoPattern.ReplaceAllString(s, "${1}"+Marker+"@")
	return cardPattern.ReplaceAllStringFunc(s, func(digits string) string {
		if isCard(digits) {
			return Marker
		}
		return digits
	})
}

// isCard reports whether s, digits grouped by spaces or dashes, is a card
// number: 13 to 19 digits that pass the Luhn check.
func isCard(s string) bool {
	digits := strings.NewReplacer(" ", "", "-", "").Replace(s)
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	for i := range len(digits) {
		d := int(digits[len(digits)-1-i] - '0')
		if i%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// normalize lowercases s and drops its - and _.
func normalize(s string) string {
	return strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(s))
}
