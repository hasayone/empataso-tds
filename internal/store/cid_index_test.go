package store

import (
	"os"
	"strings"
	"testing"
)

// TestEventsCIDIndex keeps the migration and the start-up ensure on the same
// metadata-only statement. t is the test handle. It reads files only and does
// not touch ClickHouse.
func TestEventsCIDIndex(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/clickhouse/013_events_cid_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	var stmts []string
	for _, s := range splitCHMigration(string(raw)) {
		stmts = append(stmts, migrationCollapseWS(s))
	}
	want := "ALTER TABLE emtds.events " + strings.TrimPrefix(eventsCIDIndexSQL, "ALTER TABLE events ") + ";"
	if len(stmts) != 1 || stmts[0] != want {
		t.Fatalf("migration statements %q, want %q", stmts, want)
	}
	if strings.Contains(strings.ToUpper(eventsCIDIndexSQL), "MATERIALIZE") {
		t.Fatal("start-up ensure must not rebuild old parts")
	}
	for _, frag := range []string{"ADD INDEX IF NOT EXISTS idx_cid cid", "TYPE bloom_filter(0.01)", "GRANULARITY 1"} {
		if !strings.Contains(eventsCIDIndexSQL, frag) {
			t.Fatalf("index statement %q lacks %q", eventsCIDIndexSQL, frag)
		}
	}
	schema, err := os.ReadFile("schema.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), "c.schemaExec(eventsCIDIndexSQL)") {
		t.Fatal("ensureSchema must add the cid index")
	}
}
