package store

import (
	"os"
	"testing"
)

// TestEventsTTLMigration checks migrations/clickhouse/009_events_ttl.sql contains
// only the MODIFY TTL statement for kuztds.events (365 days, no materialize).
func TestEventsTTLMigration(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/clickhouse/009_events_ttl.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := "ALTER TABLE kuztds.events MODIFY TTL ts + INTERVAL 365 DAY SETTINGS materialize_ttl_after_modify = 0;"
	got := normSQL(migrationSQLBody(string(raw)))
	if got != normSQL(want) {
		t.Fatalf("migration\n%s\nwant\n%s", got, normSQL(want))
	}
}
