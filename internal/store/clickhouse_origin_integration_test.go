//go:build integration

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/logbuf"
)

// TestCHOriginRoundTrip checks origin columns after migration 010 and refresh,
// then writes origin on a click and reads it back from the log.
func TestCHOriginRoundTrip(t *testing.T) {
	addr, user, pass := chITEnv(t)
	db := bootstrapITDatabase(t, addr, user, pass, chMigrationNoOrigin)
	ch := openITClient(t, addr, db, user, pass, false)
	if ch.originEvents.Load() || ch.originConv.Load() {
		t.Fatal("origin wave must be off before migration 010")
	}

	applyCHMigrationFile(t, addr, db, user, pass, "010_origin.sql")
	ch.originEvents.Store(false)
	ch.originConv.Store(false)
	ch.maybeRefreshOriginWave()
	if !ch.originEvents.Load() || !ch.originConv.Load() {
		t.Fatal("origin wave refresh after migration 010")
	}

	ctx := context.Background()
	group := fmt.Sprintf("itest_origin_%d", os.Getpid())
	cid := fmt.Sprintf("origin_%d", os.Getpid())
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	t.Cleanup(func() { cleanupGroup(t, ch, group) })

	if err := ch.InsertEvents(ctx, []logbuf.Event{
		{
			Ts: now, GroupID: group, GroupName: group, Stream: "s1", CID: cid,
			OriginID: "entry", OriginName: "Entry",
		},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	rows, total, err := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("Logs: total=%d err=%v rows=%+v", total, err, rows)
	}
	if rows[0].OriginCampaign != "Entry" || rows[0].OriginCampaignID != "entry" {
		t.Fatalf("origin columns: %+v", rows[0])
	}
}
