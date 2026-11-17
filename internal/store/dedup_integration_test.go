//go:build integration

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// countGroup returns the number of events of one group.
func countGroup(t *testing.T, ch *CH, group string) uint64 {
	t.Helper()
	var n uint64
	if err := ch.conn.QueryRow(context.Background(), "SELECT count() FROM events WHERE group_name = ?", group).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// dedupBatch builds a batch of n identical-shape events of one group.
func dedupBatch(group string, n int) []logbuf.Event {
	now := time.Now().UTC().Truncate(time.Second)
	out := make([]logbuf.Event, n)
	for i := range out {
		out[i] = logbuf.Event{Ts: now, GroupID: group, GroupName: group, Stream: "s1", CID: fmt.Sprintf("%s_%d", group, i), IP: "1.2.3.4"}
	}
	return out
}

// TestCHDedupSameTokenWritesOnce repeats an insert under one token, as the
// buffer does when the answer to the first attempt was lost: the second attempt
// must not add rows.
func TestCHDedupSameTokenWritesOnce(t *testing.T) {
	ch := chFromEnv(t)
	if !ch.DedupReady() {
		t.Fatal("the events table does not deduplicate: migration 014 or ensureSchema did not apply the window")
	}
	group := fmt.Sprintf("itest_dedup_%d", os.Getpid())
	batch := dedupBatch(group, 5)
	ctx := logbuf.WithDedupToken(context.Background(), "token-same-batch")
	for i := 0; i < 3; i++ {
		if err := ch.InsertEvents(ctx, batch); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if n := countGroup(t, ch, group); n != 5 {
		t.Fatalf("%d rows after three attempts under one token, want 5", n)
	}
	other := logbuf.WithDedupToken(context.Background(), "token-other-batch")
	if err := ch.InsertEvents(other, batch); err != nil {
		t.Fatal(err)
	}
	if n := countGroup(t, ch, group); n != 10 {
		t.Fatalf("%d rows after a second token, want 10: a new token is a new batch", n)
	}
}

// TestCHDedupWithoutTokenWritesBoth inserts identical batches with no token:
// the import path. Both must land, the window on the table notwithstanding.
func TestCHDedupWithoutTokenWritesBoth(t *testing.T) {
	ch := chFromEnv(t)
	group := fmt.Sprintf("itest_nodedup_%d", os.Getpid())
	batch := dedupBatch(group, 4)
	for i := 0; i < 2; i++ {
		if err := ch.InsertEvents(context.Background(), batch); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if n := countGroup(t, ch, group); n != 8 {
		t.Fatalf("%d rows after two identical batches without a token, want 8", n)
	}
}

// TestCHEnsureDedupReadsTheTableSetting checks that the probe sees the setting
// of this table in system.tables.
func TestCHEnsureDedupReadsTheTableSetting(t *testing.T) {
	ch := chFromEnv(t)
	if !ch.EnsureDedup() {
		t.Fatal("EnsureDedup reports no window after the migration")
	}
	if !ch.probeDedup() {
		t.Fatal("probeDedup does not see non_replicated_deduplication_window in create_table_query")
	}
}

// TestCHEnsureSchemaAppliesDedupWindow starts from a database that never saw
// migration 014 and expects ensureSchema to turn the window on, as the engine
// does at start on a server where the migration was not applied by hand.
func TestCHEnsureSchemaAppliesDedupWindow(t *testing.T) {
	addr, user, pass := chITEnv(t)
	var without []string
	for _, name := range chMigrationAll {
		if name != "014_events_dedup.sql" {
			without = append(without, name)
		}
	}
	db := bootstrapITDatabase(t, addr, user, pass, without)
	raw, err := openCHRaw(addr, db, user, pass)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	if raw.probeDedup() {
		t.Fatal("the window is on before ensureSchema: the test proves nothing")
	}
	ch := openITClient(t, addr, db, user, pass, true)
	if !ch.DedupReady() {
		t.Fatal("ensureSchema did not turn the deduplication window on")
	}
}
