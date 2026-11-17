package store

import (
	"fmt"
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
	db := "emtds_it_unit_test"
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
	db := "emtds_it_reject_test"
	cases := []struct {
		name, sql string
	}{
		{"use", "USE emtds;"},
		{"drop", "DROP TABLE x.events;"},
		{"truncate", "TRUNCATE TABLE " + db + ".events;"},
		{"emtds_qual", "ALTER TABLE EMTDS.events ADD COLUMN IF NOT EXISTS x String;"},
		{"backtick_emtds", "ALTER TABLE `emtds`.events ADD COLUMN IF NOT EXISTS x String;"},
		{"unqualified_alter", "ALTER TABLE events ADD COLUMN IF NOT EXISTS x String;"},
		{"alter_delete", "ALTER TABLE " + db + ".events DELETE WHERE 1;"},
		{"insert", "INSERT INTO " + db + ".events VALUES (1);"},
		{"materialize_index", "ALTER TABLE " + db + ".events MATERIALIZE INDEX idx_cid;"},
		{"add_index_materialize", "ALTER TABLE " + db + ".events ADD INDEX IF NOT EXISTS idx_cid cid TYPE bloom_filter(0.01) GRANULARITY 1, MATERIALIZE INDEX idx_cid;"},
		{"add_index_without_if", "ALTER TABLE " + db + ".events ADD INDEX idx_cid cid TYPE bloom_filter(0.01) GRANULARITY 1;"},
		{"drop_index", "ALTER TABLE " + db + ".events DROP INDEX idx_cid;"},
		{"other_setting", "ALTER TABLE " + db + ".events MODIFY SETTING index_granularity = 1024;"},
		{"setting_not_a_number", "ALTER TABLE " + db + ".events MODIFY SETTING non_replicated_deduplication_window = (SELECT 1);"},
		{"setting_with_reset", "ALTER TABLE " + db + ".events MODIFY SETTING non_replicated_deduplication_window = 10, index_granularity = 1;"},
		{"reset_setting", "ALTER TABLE " + db + ".events RESET SETTING non_replicated_deduplication_window;"},
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
	db := "emtds_it_ok_test"
	sql := "CREATE DATABASE IF NOT EXISTS emtds;\nALTER TABLE emtds.events ADD COLUMN IF NOT EXISTS site String;\nALTER TABLE emtds.events ADD INDEX IF NOT EXISTS idx_cid cid TYPE bloom_filter(0.01) GRANULARITY 1;"
	if err := validateMigrationScript(db, sql); err != nil {
		t.Fatal(err)
	}
}

// A migration may not shrink the deduplication window below the one the engine
// relies on; a larger window is fine.
func TestValidateMigrationScriptDedupWindowFloor(t *testing.T) {
	db := "emtds_it_dedup_test"
	for window, ok := range map[int]bool{1: false, eventsDedupWindow - 1: false, eventsDedupWindow: true, eventsDedupWindow * 2: true} {
		sql := fmt.Sprintf("ALTER TABLE %s.events MODIFY SETTING non_replicated_deduplication_window = %d;", db, window)
		if err := validateMigrationScript(db, sql); (err == nil) != ok {
			t.Errorf("window %d: err = %v, accepted should be %v", window, err, ok)
		}
	}
}

func TestValidateMigrationScriptAllowsDedupWindow(t *testing.T) {
	db := "emtds_it_dedup_test"
	sql := "ALTER TABLE emtds.events\n\tMODIFY SETTING non_replicated_deduplication_window = 10000;"
	if err := validateMigrationScript(db, sql); err != nil {
		t.Fatal(err)
	}
}
