package ingest_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBacklogOverTheMaximumIsOverloaded(t *testing.T) {
	e := newEndpoint(t, withMaxPending(2))
	_, key := createProject(t, e)
	post(t, e, key, 2, http.StatusAccepted)

	res, body := send(t, e, request{Headers: map[string]string{"X-Trace-Ingest-Key": key}, Body: batchOf(1)})

	assertOverloaded(t, res, body)
	if got := e.Queue.Pending(); got != 2 {
		t.Errorf("pending = %d, want 2", got)
	}
}

func TestStalledWriterIsOverloadedAndAcceptedEventsAreProcessed(t *testing.T) {
	e := newEndpoint(t, withProcess(), withRateLimit(1e6))
	project, key := createProject(t, e)
	// Holding the only write connection of ingest.db stalls the writer.
	stall, err := e.Store.Ingest.Write.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("stall writer: %v", err)
	}

	const batches = 120
	type result struct {
		res  *http.Response
		body string
	}
	results := make([]result, batches)
	var wg sync.WaitGroup
	for i := range batches {
		wg.Go(func() {
			res, body := send(t, e, request{Headers: map[string]string{"X-Trace-Ingest-Key": key}, Body: numberedBatch(i*10, 10)})
			results[i] = result{res, body}
		})
	}
	// Release the writer once the requests it cannot admit have failed.
	time.Sleep(500 * time.Millisecond)
	if err := stall.Rollback(); err != nil {
		t.Fatalf("release writer: %v", err)
	}
	wg.Wait()

	var accepted, overloaded int
	for _, r := range results {
		switch r.res.StatusCode {
		case http.StatusAccepted:
			accepted += 10
		default:
			assertOverloaded(t, r.res, r.body)
			overloaded++
		}
	}
	if overloaded == 0 || accepted == 0 {
		t.Fatalf("accepted %d Events and overloaded %d batches, want both", accepted, overloaded)
	}
	waitPending(t, e.Queue, 0)
	if got := len(listEvents(t, e, project.ID)); got != accepted {
		t.Errorf("stored Events = %d, want the %d accepted", got, accepted)
	}
}

func TestFullDiskIsOverloaded(t *testing.T) {
	e := newEndpoint(t)
	_, key := createProject(t, e)
	// ingest.db cannot grow past the pages it has, as on a full disk.
	if _, err := e.Store.Ingest.Write.Exec("PRAGMA max_page_count = 1"); err != nil {
		t.Fatalf("limit ingest.db: %v", err)
	}

	res, body := send(t, e, request{Headers: map[string]string{"X-Trace-Ingest-Key": key}, Body: numberedBatch(0, 100)})

	assertOverloaded(t, res, body)
}

func TestRejectionsAreLoggedByProjectAndCode(t *testing.T) {
	logs := &syncBuffer{}
	e := newEndpoint(t, withRateLimit(1), withLogs(logs, 20*time.Millisecond))
	project, key := createProject(t, e)
	post(t, e, key, 100, http.StatusAccepted)
	post(t, e, key, 50, http.StatusTooManyRequests)
	post(t, e, key, 50, http.StatusTooManyRequests)
	send(t, e, request{Headers: map[string]string{"X-Trace-Ingest-Key": key, "Content-Type": "application/xml"}, Body: batchOf(1)})

	record := waitRecord(t, logs, "ingest rejections")

	want := []any{
		map[string]any{"project_id": project.ID, "code": "rate_limited", "count": float64(2)},
		map[string]any{"project_id": project.ID, "code": "unsupported_media_type", "count": float64(1)},
	}
	if got := record["rejections"]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rejections = %v, want %v", got, want)
	}
}

func TestNoRejectionsLogNothing(t *testing.T) {
	logs := &syncBuffer{}
	e := newEndpoint(t, withLogs(logs, 10*time.Millisecond))
	_, key := createProject(t, e)
	post(t, e, key, 1, http.StatusAccepted)

	time.Sleep(100 * time.Millisecond)

	if record := logs.find(t, "ingest rejections"); record != nil {
		t.Errorf("log record = %v, want none without rejections", record)
	}
}

// numberedBatch returns a body of n minimal Events numbered from first+1.
func numberedBatch(first, n int) *strings.Reader {
	events := make([]string, n)
	for i := range events {
		events[i] = minimalEvent(eventID(first + i + 1))
	}
	return strings.NewReader(`{"events":[` + strings.Join(events, ",") + `]}`)
}

func assertOverloaded(t *testing.T, res *http.Response, body string) {
	t.Helper()

	assertError(t, res, body, http.StatusServiceUnavailable, "ingest_overloaded")
	if got := res.Header.Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want 5", got)
	}
}

// waitRecord returns the first log record with message msg, waiting for it.
func waitRecord(t *testing.T, logs *syncBuffer, msg string) map[string]any {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if record := logs.find(t, msg); record != nil {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %q log record", msg)
	return nil
}
