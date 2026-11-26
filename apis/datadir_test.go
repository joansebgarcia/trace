package apis_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joansebgarcia/amber/tests"
)

func TestStartCreatesDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "amber_data")

	if _, err := tests.StartApp(t, dir); err != nil {
		t.Fatalf("start app: %v", err)
	}

	for _, name := range []string{"ingest.db", "data.db", ".lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("data directory has no %s: %v", name, err)
		}
	}
}

func TestStartRejectsLockedDataDir(t *testing.T) {
	first := tests.NewApp(t)

	_, err := tests.StartApp(t, first.DataDir)

	if err == nil || !strings.Contains(err.Error(), "in use by another process") {
		t.Fatalf("error = %v, want one saying the data directory is in use", err)
	}
}
