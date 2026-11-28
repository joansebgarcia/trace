package projects_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/joansgarcia/trace/migrations"
	"github.com/joansgarcia/trace/projects"
	"github.com/joansgarcia/trace/store"
)

func TestCreatedKeyResolvesToItsProject(t *testing.T) {
	p := projects.New(openStore(t).Data)
	project, key := create(t, p)

	got, ok, err := p.Resolve(t.Context(), key)

	if err != nil || !ok || !reflect.DeepEqual(got, project) {
		t.Errorf("resolve = %+v %t %v, want %+v true nil", got, ok, err, project)
	}
	if _, err := uuid.Parse(project.ID); err != nil {
		t.Errorf("project id = %q, want a UUID", project.ID)
	}
}

func TestUnknownOrRevokedKeyDoesNotResolve(t *testing.T) {
	cases := []struct {
		name string
		key  func(t *testing.T, p *projects.Projects) string
	}{
		{
			name: "unknown key",
			key: func(t *testing.T, p *projects.Projects) string {
				create(t, p)
				return "not-a-key"
			},
		},
		{
			name: "revoked key",
			key: func(t *testing.T, p *projects.Projects) string {
				_, key := create(t, p)
				if err := p.Revoke(t.Context(), key); err != nil {
					t.Fatalf("revoke key: %v", err)
				}
				return key
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := projects.New(openStore(t).Data)

			got, ok, err := p.Resolve(t.Context(), c.key(t, p))

			if err != nil || ok {
				t.Errorf("resolve = %+v %t %v, want not found", got, ok, err)
			}
		})
	}
}

func TestKeysAreResolvedFromMemory(t *testing.T) {
	data := openStore(t).Data
	p := projects.New(data)
	_, key := create(t, p)
	resolve(t, p, key)

	// Without data.db, a key resolved from it would fail.
	if _, err := data.Write.Exec("DELETE FROM ingest_keys"); err != nil {
		t.Fatalf("delete keys: %v", err)
	}

	resolve(t, p, key)
}

func create(t *testing.T, p *projects.Projects) (projects.Project, string) {
	t.Helper()

	project, key, err := p.Create(t.Context(), "checkout")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project, key
}

// resolve fails unless key resolves to a Project.
func resolve(t *testing.T, p *projects.Projects, key string) {
	t.Helper()

	if _, ok, err := p.Resolve(t.Context(), key); err != nil || !ok {
		t.Fatalf("resolve = %t %v, want found", ok, err)
	}
}

// openStore opens a temporary data directory with the migrations built into
// the binary applied. It closes when the test ends.
func openStore(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	schema, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if err := s.Migrate(t.Context(), schema); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}
