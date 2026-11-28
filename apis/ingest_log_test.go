package apis_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestIngestRequestsAreLoggedWithoutTheClientIP(t *testing.T) {
	logs := &Logs{}
	a, err := StartApp(t, t.TempDir(), WithLogs(logs))
	if err != nil {
		t.Fatalf("start app: %v", err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, a.URL+"/api/ingest/v1/events?ingest_key=secret-key", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res, err := a.Client.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	_ = res.Body.Close()

	record := logs.Wait(t, "http request")
	if _, ok := record["client_ip"]; ok {
		t.Errorf("record = %v, want no client_ip for ingestion", record)
	}
	logs.mu.Lock()
	out := logs.out.String()
	logs.mu.Unlock()
	if strings.Contains(out, "secret-key") {
		t.Error("logs hold the ingest_key of the query")
	}
}
