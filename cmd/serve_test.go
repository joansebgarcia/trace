package cmd_test

import (
	"strings"
	"testing"
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

	if !strings.Contains(out, `"./amber_data"`) {
		t.Errorf("help does not show the default --dir ./amber_data:\n%s", out)
	}
}
