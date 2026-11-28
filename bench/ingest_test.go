//go:build unix

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/joansgarcia/trace/apis"
	"github.com/joansgarcia/trace/event"
)

const timeout = 5 * time.Second

func TestBuiltEventsAreValidAndUnique(t *testing.T) {
	newBody := bodyBuilder(2048)

	first, second := newBody(), newBody()

	events, err := event.DecodeBatch(first)
	if err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if n := len(first); n < 2000 || n > 2200 {
		t.Errorf("body size = %d, want about 2048", n)
	}
	others, _ := event.DecodeBatch(second)
	if events[0].EventID == others[0].EventID {
		t.Errorf("event_id %s repeats, want one per body", events[0].EventID)
	}
}

func TestPreparedDirectoryAcceptsTheLoad(t *testing.T) {
	dir := t.TempDir()
	key, err := prepare(dir)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	s := apis.NewServer(apis.Config{Addr: "127.0.0.1:0", DataDir: dir, ShutdownTimeout: timeout})
	if err := s.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	req, err := newRequest(t.Context(), "http://"+s.Addr()+ingestPath, key, bodyBuilder(2048)())
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusAccepted || !bytes.Contains(body, []byte(`"accepted":1`)) {
		t.Errorf("response = %d %s, want 202", res.StatusCode, body)
	}
}
