package cmd_test

import (
	"strings"
	"testing"

	"github.com/joansebgarcia/trace/store"
)

func TestServeAnswersHealth(t *testing.T) {
	serveHealthy(t)
}

func TestServeHTTPDefault(t *testing.T) {
	out, err := execute("serve", "--help")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !strings.Contains(out, `"127.0.0.1:8090"`) {
		t.Errorf("help does not show the default --http 127.0.0.1:8090:\n%s", out)
	}
}

func TestServeDirDefault(t *testing.T) {
	out, err := execute("serve", "--help")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !strings.Contains(out, `"./trace_data"`) {
		t.Errorf("help does not show the default --dir ./trace_data:\n%s", out)
	}
}

func TestServeRejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := s.Data.Write.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatalf("set version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	_, err = execute("serve", "--http="+findFreeAddr(t), "--dir="+dir)

	if err == nil || !strings.Contains(err.Error(), "data.db schema version 999 is newer than this binary supports") {
		t.Fatalf("error = %v, want one saying data.db is newer than the binary", err)
	}
}
