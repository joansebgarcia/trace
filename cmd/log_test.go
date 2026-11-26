package cmd_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServeLogFormat(t *testing.T) {
	t.Run("text by default", func(t *testing.T) {
		out := serveHealthy(t)()

		if !strings.Contains(out, "level=INFO") {
			t.Errorf("output is not text logs:\n%s", out)
		}
	})

	t.Run("json emits one JSON object per line", func(t *testing.T) {
		lines := splitLogLines(serveHealthy(t, "--log-format=json")())

		if len(lines) == 0 {
			t.Fatal("no log lines")
		}
		for _, line := range lines {
			if !json.Valid([]byte(line)) {
				t.Errorf("line is not JSON: %s", line)
			}
		}
	})
}

func TestServeLogLevel(t *testing.T) {
	tests := []struct {
		level     string
		wantInfo  bool
		wantDebug bool
	}{
		{level: "debug", wantInfo: true, wantDebug: true},
		{level: "info", wantInfo: true, wantDebug: false},
		{level: "warn", wantInfo: false, wantDebug: false},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			out := serveHealthy(t, "--log-level="+tt.level)()

			if got := strings.Contains(out, "level=INFO"); got != tt.wantInfo {
				t.Errorf("info logs present = %v, want %v:\n%s", got, tt.wantInfo, out)
			}
			if got := strings.Contains(out, "level=DEBUG"); got != tt.wantDebug {
				t.Errorf("debug logs present = %v, want %v:\n%s", got, tt.wantDebug, out)
			}
		})
	}
}

func TestServeRejectsInvalidLogSettings(t *testing.T) {
	for _, arg := range []string{"--log-format=xml", "--log-level=loud", "--log-level=info+2"} {
		t.Run(arg, func(t *testing.T) {
			if _, err := execute("serve", "--http="+findFreeAddr(t), arg); err == nil {
				t.Errorf("serve %s succeeded, want an error", arg)
			}
		})
	}
}
