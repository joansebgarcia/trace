package ingest_test

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestIngestKeyMayGoInTheQuery(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)

	res, body := send(t, e, request{Query: "ingest_key=" + key, Body: batchOf(1)})

	if res.StatusCode != http.StatusAccepted {
		t.Errorf("response = %d %s, want 202", res.StatusCode, body)
	}
}

func TestDifferentKeysInHeaderAndQueryConflict(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)
	_, other := createProject(t, e)

	res, body := send(t, e, request{
		Query:   "ingest_key=" + other,
		Headers: map[string]string{"X-Trace-Ingest-Key": key},
		Body:    batchOf(1),
	})

	assertError(t, res, body, http.StatusBadRequest, "ingest_key_conflict")
}

func TestSameKeyInHeaderAndQueryIsAccepted(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)

	res, body := send(t, e, request{
		Query:   "ingest_key=" + key,
		Headers: map[string]string{"X-Trace-Ingest-Key": key},
		Body:    batchOf(1),
	})

	if res.StatusCode != http.StatusAccepted {
		t.Errorf("response = %d %s, want 202", res.StatusCode, body)
	}
}

func TestContentTypes(t *testing.T) {
	cases := []struct {
		contentType string
		status      int
	}{
		{"application/json", http.StatusAccepted},
		{"application/json; charset=utf-8", http.StatusAccepted},
		{"text/plain", http.StatusAccepted},
		{"text/plain;charset=UTF-8", http.StatusAccepted},
		{"application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"application/xml", http.StatusUnsupportedMediaType},
	}

	for _, c := range cases {
		t.Run(c.contentType, func(t *testing.T) {
			e := newEndpoint(t)
			_, key := createProject(t, e)

			res, body := send(t, e, request{
				Headers: map[string]string{"X-Trace-Ingest-Key": key, "Content-Type": c.contentType},
				Body:    batchOf(1),
			})

			if c.status == http.StatusAccepted {
				if res.StatusCode != c.status {
					t.Errorf("response = %d %s, want 202", res.StatusCode, body)
				}
				return
			}
			assertError(t, res, body, c.status, "unsupported_media_type")
		})
	}
}

func TestGzipBodyIsAccepted(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)
	raw, _ := io.ReadAll(batchOf(2))

	res, body := send(t, e, request{
		Headers: map[string]string{"X-Trace-Ingest-Key": key, "Content-Encoding": "gzip"},
		Body:    bytes.NewReader(compress(t, raw)),
	})

	if res.StatusCode != http.StatusAccepted || strings.TrimSpace(body) != `{"accepted":2}` {
		t.Errorf("response = %d %s, want 202 {\"accepted\":2}", res.StatusCode, body)
	}
}

func TestUnsupportedContentEncodingIsRejected(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)

	res, body := send(t, e, request{
		Headers: map[string]string{"X-Trace-Ingest-Key": key, "Content-Encoding": "br"},
		Body:    batchOf(1),
	})

	assertError(t, res, body, http.StatusUnsupportedMediaType, "unsupported_media_type")
}

func TestPreflightAllowsAnyOriginWithoutCredentials(t *testing.T) {
	e := newEndpoint(t)

	res, body := send(t, e, request{
		Method: http.MethodOptions,
		Headers: map[string]string{
			"Origin":                         "https://shop.example.com",
			"Access-Control-Request-Method":  "POST",
			"Access-Control-Request-Headers": "content-type, x-trace-ingest-key",
		},
	})

	if res.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d %s, want 204", res.StatusCode, body)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want none", got)
	}
	allowed := strings.ToLower(res.Header.Get("Access-Control-Allow-Headers"))
	for _, header := range []string{"content-type", "content-encoding", "x-trace-ingest-key"} {
		if !strings.Contains(allowed, header) {
			t.Errorf("Access-Control-Allow-Headers = %q, want it to hold %s", allowed, header)
		}
	}
}

func TestEveryResponseAllowsAnyOrigin(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)

	accepted, _ := send(t, e, request{Headers: map[string]string{"X-Trace-Ingest-Key": key}, Body: batchOf(1)})
	rejected, _ := send(t, e, request{Body: batchOf(1)})

	for _, res := range []*http.Response{accepted, rejected} {
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%d: Access-Control-Allow-Origin = %q, want *", res.StatusCode, got)
		}
	}
}

func TestGzipBombIsRejected(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)
	// 64 MiB of spaces compress to about 64 KiB.
	bomb := compress(t, bytes.Repeat([]byte(" "), 64<<20))

	res, body := send(t, e, request{
		Headers: map[string]string{"X-Trace-Ingest-Key": key, "Content-Encoding": "gzip"},
		Body:    bytes.NewReader(bomb),
	})

	assertError(t, res, body, http.StatusRequestEntityTooLarge, "payload_too_large")
}

func TestBodyOverTheSentLimitIsRejected(t *testing.T) {
	cases := []struct {
		name     string
		encoding string
		body     []byte
	}{
		// Random hex barely compresses, so the gzip body stays over 256 KiB.
		{"gzip", "gzip", compress(t, []byte(randomHex(t, 600<<10)))},
		{"identity", "", bytes.Repeat([]byte(" "), 300<<10)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEndpoint(t)
			_, key := createProject(t, e)

			res, body := send(t, e, request{
				Headers: map[string]string{"X-Trace-Ingest-Key": key, "Content-Encoding": c.encoding},
				Body:    bytes.NewReader(c.body),
			})

			assertError(t, res, body, http.StatusRequestEntityTooLarge, "payload_too_large")
		})
	}
}

func TestEventOverTheSerializedLimitIsRejected(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)
	// extra stays under its own 16 KiB, but the raw_stack of the Event
	// takes it past 128 KiB.
	large := `{
		"event_id": "0190b7a4-7c1e-7000-8000-000000000002",
		"timestamp": "2026-11-27T10:00:00.000Z",
		"platform": "javascript",
		"sdk": {"name": "trace-js", "version": "0.1.0"},
		"exceptions": [{"type": "TypeError", "raw_stack": "` + strings.Repeat("a", 130<<10) + `"}]
	}`
	raw := `{"events":[` + minimalEvent("0190b7a4-7c1e-7000-8000-000000000001") + `,` + large + `]}`

	res, body := send(t, e, request{
		Headers: map[string]string{"X-Trace-Ingest-Key": key, "Content-Encoding": "gzip"},
		Body:    bytes.NewReader(compress(t, []byte(raw))),
	})

	assertError(t, res, body, http.StatusRequestEntityTooLarge, "payload_too_large")
	if !strings.Contains(body, `"index":1`) {
		t.Errorf("body = %s, want details with index 1", body)
	}
	if got := e.Queue.Pending(); got != 0 {
		t.Errorf("pending = %d, want 0", got)
	}
}

// batchOf returns a body with n minimal Events.
func batchOf(n int) io.Reader {
	events := make([]string, n)
	for i := range events {
		events[i] = minimalEvent(eventID(i + 1))
	}
	return strings.NewReader(`{"events":[` + strings.Join(events, ",") + `]}`)
}

func compress(t *testing.T, raw []byte) []byte {
	t.Helper()

	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	return b.Bytes()
}

func randomHex(t *testing.T, n int) string {
	t.Helper()

	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return hex.EncodeToString(b)
}
