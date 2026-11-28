package event_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// protocolDir holds the protocol contract.
const protocolDir = "../docs/protocol/v1"

// schemaFixture is a fixture of the protocol contract with its body decoded
// for the schema.
type schemaFixture struct {
	Body any
	// BeyondSchema explains why the schema cannot reject an invalid body.
	BeyondSchema string
}

func TestValidFixturesMatchTheSchema(t *testing.T) {
	batch := compileBatch(t)

	for name, f := range loadFixtures(t, "valid") {
		t.Run(name, func(t *testing.T) {
			if err := batch.Validate(f.Body); err != nil {
				t.Errorf("body breaks the schema: %v", err)
			}
		})
	}
}

func TestInvalidFixturesBreakTheSchema(t *testing.T) {
	batch := compileBatch(t)

	for name, f := range loadFixtures(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			if f.BeyondSchema != "" {
				t.Skip(f.BeyondSchema)
			}
			if err := batch.Validate(f.Body); err == nil {
				t.Error("body matches the schema, want it to break it")
			}
		})
	}
}

// compileBatch compiles the schema of a batch from event.schema.json.
func compileBatch(t *testing.T) *jsonschema.Schema {
	t.Helper()

	path, err := filepath.Abs(filepath.Join(protocolDir, "event.schema.json"))
	if err != nil {
		t.Fatalf("resolve schema path: %v", err)
	}
	schema, err := jsonschema.NewCompiler().Compile(path + "#/$defs/batch")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

// loadFixtures loads the fixtures in kind, "valid" or "invalid", by name.
func loadFixtures(t *testing.T, kind string) map[string]schemaFixture {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(protocolDir, "fixtures", kind, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("find %s fixtures: %v", kind, err)
	}
	fixtures := make(map[string]schemaFixture, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		var raw struct {
			Body         json.RawMessage `json:"body"`
			BeyondSchema string          `json:"beyond_schema"`
		}
		if err := json.Unmarshal(content, &raw); err != nil {
			t.Fatalf("decode fixture %s: %v", path, err)
		}
		body, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw.Body))
		if err != nil {
			t.Fatalf("decode body of %s: %v", path, err)
		}
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		fixtures[name] = schemaFixture{Body: body, BeyondSchema: raw.BeyondSchema}
	}
	return fixtures
}
