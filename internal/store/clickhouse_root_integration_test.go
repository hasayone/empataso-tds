//go:build integration

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestCHRootMigrationDefaults checks migration 015 on a throwaway database.
// A click written before the columns exist must read root_cid as empty.
// The production database kuztds is never used. A missing ClickHouse skips the test.
func TestCHRootMigrationDefaults(t *testing.T) {
	addr, user, pass := chITEnv(t)
	files := make([]string, 0, len(chMigrationAll))
	for _, name := range chMigrationAll {
		if name == "015_root.sql" {
			continue
		}
		files = append(files, name)
	}
	db := bootstrapITDatabase(t, addr, user, pass, files)
	rawCH, err := openCHRaw(addr, db, user, pass)
	if err != nil {
		t.Fatal(err)
	}
	defer rawCH.Close()

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	group := fmt.Sprintf("itest_root_%d", os.Getpid())
	cid := fmt.Sprintf("rootit_%d", os.Getpid())
	if err := rawCH.conn.Exec(ctx, `INSERT INTO events (ts, cid, group_id) VALUES (?, ?, ?)`, now, cid, group); err != nil {
		t.Fatalf("insert before 015: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	applyCHMigrationFile(t, addr, db, user, pass, "015_root.sql")

	var got string
	if err := rawCH.conn.QueryRow(ctx, `SELECT root_cid FROM events WHERE cid = ?`, cid).Scan(&got); err != nil {
		t.Fatalf("read root_cid: %v", err)
	}
	if got != "" {
		t.Fatalf("root_cid = %q, want empty", got)
	}
	for _, col := range []string{"root_cid", "origin_stream_name", "origin_keyword"} {
		var n uint64
		q := `SELECT count() FROM system.columns WHERE database = currentDatabase() AND table = 'conversions' AND name = ?`
		if err := rawCH.conn.QueryRow(ctx, q, col).Scan(&n); err != nil {
			t.Fatalf("column %s: %v", col, err)
		}
		if n != 1 {
			t.Fatalf("conversions.%s count = %d, want 1", col, n)
		}
	}
	live := openITClient(t, addr, db, user, pass, false)
	defer live.Close()
	if !live.ready("events", waveRoot) || !live.ready("conversions", waveRoot) {
		t.Fatal("root wave must be ready after migration 015")
	}
}
