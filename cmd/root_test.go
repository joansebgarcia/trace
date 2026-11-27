package cmd_test

import (
	"strings"
	"testing"

	"github.com/joansgarcia/trace/web"
)

func TestVersion(t *testing.T) {
	out, err := execute("--version")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !strings.Contains(out, "1.2.3") {
		t.Errorf("output = %q, want it to contain the version 1.2.3", out)
	}
	want := "without the web UI"
	if web.Embedded {
		want = "with the web UI embedded"
	}
	if !strings.Contains(out, want) {
		t.Errorf("output = %q, want it to contain %q", out, want)
	}
}
