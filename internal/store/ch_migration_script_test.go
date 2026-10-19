package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func migrationDirForTest() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations", "clickhouse")
}

func TestValidateMigrationScriptRealFiles(t *testing.T) {
	db := "kuztds_it_unit_test"
	dir := migrationDirForTest()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateMigrationScript(db, string(raw)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
}

func TestValidateMigrationScriptRejectsUnsafe(t *testing.T) {
	db := "kuztds_it_reject_test"
	cases := []struct {
		name, sql string
	}{
		{"use", "USE kuztds;"},
		{"drop", "DROP TABLE x.events;"},
		{"truncate", "TRUNCATE TABLE " + db + ".events;"},
		{"kuztds_qual", "ALTER TABLE KUZTDS.events ADD COLUMN IF NOT EXISTS x String;"},
		{"backtick_kuztds", "ALTER TABLE `kuztds`.events ADD COLUMN IF NOT EXISTS x String;"},
		{"unqualified_alter", "ALTER TABLE events ADD COLUMN IF NOT EXISTS x String;"},
		{"alter_delete", "ALTER TABLE " + db + ".events DELETE WHERE 1;"},
		{"insert", "INSERT INTO " + db + ".events VALUES (1);"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMigrationScript(db, tc.sql); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestValidateMigrationScriptAllowsSubstituted(t *testing.T) {
	db := "kuztds_it_ok_test"
	sql := "CREATE DATABASE IF NOT EXISTS kuztds;\nALTER TABLE kuztds.events ADD COLUMN IF NOT EXISTS site String;"
	if err := validateMigrationScript(db, sql); err != nil {
		t.Fatal(err)
	}
}
