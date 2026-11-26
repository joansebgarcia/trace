package cmd_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/joansebgarcia/amber/cmd"
)

func TestServeHTTPPrecedence(t *testing.T) {
	t.Run("env over default", func(t *testing.T) {
		envAddr := findFreeAddr(t)
		t.Setenv(cmd.EnvPrefix+"HTTP", envAddr)

		serve(t, "serve")

		waitHealthy(t, envAddr)
	})

	t.Run("flag over env", func(t *testing.T) {
		envAddr, flagAddr := findFreeAddr(t), findFreeAddr(t)
		t.Setenv(cmd.EnvPrefix+"HTTP", envAddr)

		serve(t, "serve", "--http="+flagAddr)

		waitHealthy(t, flagAddr)
	})
}

func TestServeLogFormatPrecedence(t *testing.T) {
	t.Run("env over default", func(t *testing.T) {
		t.Setenv(cmd.EnvPrefix+"LOG_FORMAT", "json")

		if line := splitLogLines(serveHealthy(t)())[0]; !json.Valid([]byte(line)) {
			t.Errorf("first log line is not JSON: %s", line)
		}
	})

	t.Run("flag over env", func(t *testing.T) {
		t.Setenv(cmd.EnvPrefix+"LOG_FORMAT", "json")

		if out := serveHealthy(t, "--log-format=text")(); !strings.Contains(out, "level=INFO") {
			t.Errorf("output is not text logs:\n%s", out)
		}
	})
}

func TestServeLogLevelPrecedence(t *testing.T) {
	t.Run("env over default", func(t *testing.T) {
		t.Setenv(cmd.EnvPrefix+"LOG_LEVEL", "warn")

		if out := serveHealthy(t)(); strings.Contains(out, "level=INFO") {
			t.Errorf("info logs present with %sLOG_LEVEL=warn:\n%s", cmd.EnvPrefix, out)
		}
	})

	t.Run("flag over env", func(t *testing.T) {
		t.Setenv(cmd.EnvPrefix+"LOG_LEVEL", "warn")

		if out := serveHealthy(t, "--log-level=info")(); !strings.Contains(out, "level=INFO") {
			t.Errorf("no info logs with --log-level=info:\n%s", out)
		}
	})
}

func TestServeShutdownTimeoutPrecedence(t *testing.T) {
	tests := []struct {
		name string
		env  string
		args []string
		want string
	}{
		{name: "default", want: "shutdown_timeout=10s"},
		{name: "env over default", env: "3s", want: "shutdown_timeout=3s"},
		{name: "flag over env", env: "3s", args: []string{"--shutdown-timeout=7s"}, want: "shutdown_timeout=7s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv(cmd.EnvPrefix+"SHUTDOWN_TIMEOUT", tt.env)
			}

			if out := serveHealthy(t, tt.args...)(); !strings.Contains(out, tt.want) {
				t.Errorf("startup log does not report %s:\n%s", tt.want, out)
			}
		})
	}
}

func TestServeRejectsInvalidEnv(t *testing.T) {
	t.Setenv(cmd.EnvPrefix+"SHUTDOWN_TIMEOUT", "soon")

	_, err := execute("serve", "--http="+findFreeAddr(t))

	if want := cmd.EnvPrefix + "SHUTDOWN_TIMEOUT"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want one naming %s", err, want)
	}
}

func TestServeHelpNamesEnv(t *testing.T) {
	out, err := execute("serve", "--help")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	for _, name := range []string{"HTTP", "LOG_FORMAT", "LOG_LEVEL", "SHUTDOWN_TIMEOUT"} {
		if env := cmd.EnvPrefix + name; !strings.Contains(out, env) {
			t.Errorf("help does not name %s:\n%s", env, out)
		}
	}
}
