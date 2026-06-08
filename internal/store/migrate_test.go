package store

import "testing"

// loadMigrations is the parse/validate step that runs without a database. It must
// parse every embedded file, reject malformed names, sort by version, and produce
// a non-empty checksum — the invariants the on-DB Migrate relies on.
func TestLoadMigrationsParsesEmbeddedFiles(t *testing.T) {
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("expected at least one embedded migration")
	}
	var prev int64
	for i, m := range migs {
		if m.version <= 0 {
			t.Errorf("migration %d has non-positive version %d", i, m.version)
		}
		if i > 0 && m.version <= prev {
			t.Errorf("migrations not strictly ascending: %d then %d", prev, m.version)
		}
		prev = m.version
		if m.name == "" || m.sql == "" || m.checksum == "" {
			t.Errorf("migration %04d has an empty field: %+v", m.version, m)
		}
	}
}

func TestParseMigrationName(t *testing.T) {
	ok := map[string]struct {
		version int64
		name    string
	}{
		"0001_owners.sql":             {1, "owners"},
		"0012_add_projects_table.sql": {12, "add_projects_table"},
	}
	for in, want := range ok {
		v, n, err := parseMigrationName(in)
		if err != nil || v != want.version || n != want.name {
			t.Errorf("parseMigrationName(%q) = (%d,%q,%v), want (%d,%q,nil)", in, v, n, err, want.version, want.name)
		}
	}
	for _, bad := range []string{"owners.sql", "_owners.sql", "0001_.sql", "abc_owners.sql", "0001-owners.sql"} {
		if _, _, err := parseMigrationName(bad); err == nil {
			t.Errorf("parseMigrationName(%q) should have errored", bad)
		}
	}
}
