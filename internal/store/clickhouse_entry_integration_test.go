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

// TestCHJournalEntryCampaign writes a click that the campaign "entry" forwarded to
// "served" and a direct click on "entry", then reads the click journal by campaign.
// Filtering by the entry campaign, by its id or by its name finds both clicks; a Scope
// condition and the serving campaign stay exact.
func TestCHJournalEntryCampaign(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	sfx := fmt.Sprintf("%d", os.Getpid())
	entry, entryName := "entry_"+sfx, "Entry "+sfx
	served, servedName := "served_"+sfx, "Served "+sfx
	now := time.Now().UTC().Truncate(time.Second)
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	t.Cleanup(func() { cleanupGroup(t, ch, servedName); cleanupGroup(t, ch, entryName) })

	if err := ch.InsertEvents(ctx, []logbuf.Event{
		{Ts: now, GroupID: served, GroupName: servedName, OriginID: entry, OriginName: entryName, Stream: "s", CID: "fwd_" + sfx, IP: "1.1.1.1"},
		{Ts: now, GroupID: entry, GroupName: entryName, Stream: "s", CID: "dir_" + sfx, IP: "2.2.2.2"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	count := func(conds ...Cond) int64 {
		t.Helper()
		_, total, err := ch.Logs(ctx, LogFilter{From: from, To: to, Conds: conds, Limit: 10})
		if err != nil {
			t.Fatalf("Logs: %v", err)
		}
		return total
	}
	if n := count(Cond{Field: "campaign_id", Op: "eq", Values: []string{entry}}); n != 2 {
		t.Fatalf("entry id: %d rows, want 2", n)
	}
	if n := count(Cond{Field: "campaign", Op: "eq", Values: []string{entryName}}); n != 2 {
		t.Fatalf("entry name: %d rows, want 2", n)
	}
	if n := count(Cond{Field: "campaign_id", Op: "eq", Values: []string{served}}); n != 1 {
		t.Fatalf("served id: %d rows, want 1", n)
	}
	if n := count(Cond{Field: "campaign_id", Op: "ne", Values: []string{entry}}); n != 0 {
		t.Fatalf("not entry: %d rows, want 0", n)
	}
	if n := count(
		Cond{Field: "campaign_id", Op: "eq", Values: []string{entry}},
		Cond{Field: "campaign_id", Op: "in", Values: []string{entry}, Scope: true},
	); n != 1 {
		t.Fatalf("entry inside a scope of the entry only: %d rows, want 1", n)
	}
	for _, c := range []Cond{
		{Field: "campaign_id", Op: "eq", Values: []string{entry}},
		{Field: "campaign", Op: "in", Values: []string{entryName}},
		{Field: "campaign_id", Op: "nin", Values: []string{entry}},
	} {
		if _, _, err := ch.QueryPostbacks(ctx, PostbackQuery{From: from, To: to, Conds: []Cond{c}}); err != nil {
			t.Fatalf("conversion journal %s %s: %v", c.Field, c.Op, err)
		}
	}
}
