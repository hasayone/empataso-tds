//go:build integration

package store

import (
	"context"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// ctsClick writes one click of group with the given cid.
func ctsClick(t *testing.T, ch *CH, group, cid string) {
	t.Helper()
	ev := logbuf.Event{Ts: time.Now().UTC(), GroupID: group, GroupName: group, Stream: "s1", CID: cid, IP: "8.8.8.8"}
	if err := ch.InsertEvents(context.Background(), []logbuf.Event{ev}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
}

// ctsPerf returns the conversions and the payout Performance counts for group.
func ctsPerf(t *testing.T, ch *CH, group string) (conv int64, profit float64) {
	t.Helper()
	now := time.Now()
	rows, err := ch.Performance(context.Background(), now.Add(-2*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Performance: %v", err)
	}
	for _, r := range rows {
		if r.GroupID == group {
			conv += r.Conv
			profit += r.Profit
		}
	}
	return conv, profit
}

// ctsStoredTS reads the time of the stored row for cid and status.
func ctsStoredTS(t *testing.T, ch *CH, cid, status string) time.Time {
	t.Helper()
	var ts time.Time
	err := ch.conn.QueryRow(context.Background(), `SELECT ts FROM conversions WHERE cid = ? AND status = ? ORDER BY ts DESC LIMIT 1`, cid, status).Scan(&ts)
	if err != nil {
		t.Fatalf("read ts of %s/%s: %v", cid, status, err)
	}
	return ts
}

// A conversion keeps the time it arrived with. A lead that waited in the disk
// queue while the network sent a rejection is written after the rejection, and
// it must still lose against it: the lead is not income. The rejected row
// itself stays in Performance, so the voided click must look exactly like a
// click that has only that rejected row.
func TestCHExplicitTimeKeepsRejectedLeadOutOfIncome(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Minute)
	voided, control, rejectedOnly := "cts_voided", "cts_control", "cts_rejected_only"
	ctsClick(t, ch, voided, "cts_c_voided")
	ctsClick(t, ch, control, "cts_c_control")
	ctsClick(t, ch, rejectedOnly, "cts_c_rejected_only")
	time.Sleep(200 * time.Millisecond)

	// The rejection arrived a minute after the lead and was written first.
	if _, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_voided", Profit: 5, Status: "rejected", At: t0.Add(time.Minute)}); err != nil {
		t.Fatalf("rejected: %v", err)
	}
	if _, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_voided", Profit: 7, Status: "lead", At: t0}); err != nil {
		t.Fatalf("late lead: %v", err)
	}
	if _, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_control", Profit: 7, Status: "lead", At: t0}); err != nil {
		t.Fatalf("control lead: %v", err)
	}
	if _, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_rejected_only", Profit: 5, Status: "rejected", At: t0.Add(time.Minute)}); err != nil {
		t.Fatalf("rejected only: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if got := ctsStoredTS(t, ch, "cts_c_voided", "lead"); !got.Equal(t0) {
		t.Fatalf("the late lead is stored at %v, want its arrival %v", got, t0)
	}
	wantConv, wantProfit := ctsPerf(t, ch, rejectedOnly)
	if wantConv != 1 || wantProfit != 5 {
		t.Fatalf("a click with only a rejection: conv=%d profit=%v, want 1 and 5", wantConv, wantProfit)
	}
	if conv, profit := ctsPerf(t, ch, voided); conv != wantConv || profit != wantProfit {
		t.Fatalf("a lead written after its rejection counts as income: conv=%d profit=%v, want %d and %v", conv, profit, wantConv, wantProfit)
	}
	if conv, profit := ctsPerf(t, ch, control); conv != 1 || profit != 7 {
		t.Fatalf("a lead without a rejection must count: conv=%d profit=%v", conv, profit)
	}
}

// Without At the row time is the moment of the write, as before.
func TestCHZeroArrivalTimeIsNow(t *testing.T) {
	ch := chFromEnv(t)
	ctsClick(t, ch, "cts_now", "cts_c_now")
	time.Sleep(200 * time.Millisecond)
	before := time.Now().UTC().Truncate(time.Second)
	if _, err := ch.RecordConversion(context.Background(), PostbackIn{CID: "cts_c_now", Profit: 1, Status: "lead"}); err != nil {
		t.Fatal(err)
	}
	got := ctsStoredTS(t, ch, "cts_c_now", "lead")
	if got.Before(before.Add(-time.Second)) || got.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("row time %v is not the time of the write (%v)", got, before)
	}
}

// Fresh is true for the first arrival of a (cid, status) pair, also when the
// statuses alternate, and false for every later arrival of the pair, also when
// the previous row has another status.
func TestCHFreshMeansFirstArrivalOfThePair(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	ctsClick(t, ch, "cts_fresh", "cts_c_fresh")
	time.Sleep(200 * time.Millisecond)
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	steps := []struct {
		status string
		at     time.Duration
		fresh  bool
	}{
		{"lead", 0, true},
		{"sale", 10 * time.Second, true},
		{"lead", 20 * time.Second, false}, // the previous row is a sale; the pair (lead) exists
		{"sale", 30 * time.Second, false},
		{"rejected", 40 * time.Second, true},
	}
	for i, s := range steps {
		res, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_fresh", Profit: 1, Status: s.status, At: t0.Add(s.at)})
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if res.Fresh != s.fresh {
			t.Fatalf("step %d (%s): Fresh=%v, want %v", i, s.status, res.Fresh, s.fresh)
		}
		if res.GroupID != "cts_fresh" {
			t.Fatalf("step %d: group %q", i, res.GroupID)
		}
	}
	// The same postback written again (its first answer was lost) is still the
	// first arrival: the row it finds is its own.
	res, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_fresh", Profit: 1, Status: "rejected", At: t0.Add(40 * time.Second)})
	if err != nil || !res.Fresh {
		t.Fatalf("a repeat of the same arrival: Fresh=%v err=%v, want true", res.Fresh, err)
	}
}

// prev_status follows the row time, so a postback written late does not take a
// newer row as its predecessor.
func TestCHPrevStatusFollowsRowTime(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	ctsClick(t, ch, "cts_prev", "cts_c_prev")
	time.Sleep(200 * time.Millisecond)
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for _, s := range []struct {
		status string
		at     time.Duration
	}{{"sale", 60 * time.Second}, {"lead", 0}} {
		if _, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_prev", Profit: 1, Status: s.status, At: t0.Add(s.at)}); err != nil {
			t.Fatal(err)
		}
	}
	var prev string
	err := ch.conn.QueryRow(ctx, `SELECT prev_status FROM conversions WHERE cid = ? AND status = 'lead'`, "cts_c_prev").Scan(&prev)
	if err != nil {
		t.Fatal(err)
	}
	if prev != "" {
		t.Fatalf("the late lead (first by time) has prev_status %q, want empty", prev)
	}
}

// A pair written first with the later time (directly) and then with the
// earlier time (from the disk queue) is not new the second time: only one of
// the two postbacks may send the outbound templates.
func TestCHFreshOutOfOrderArrivalIsNotFreshTwice(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	ctsClick(t, ch, "cts_ooo", "cts_c_ooo")
	time.Sleep(200 * time.Millisecond)
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	later, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_ooo", Profit: 1, Status: "lead", At: t0.Add(30 * time.Second)})
	if err != nil || !later.Fresh {
		t.Fatalf("direct write: Fresh=%v err=%v, want true", later.Fresh, err)
	}
	earlier, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_ooo", Profit: 1, Status: "lead", At: t0})
	if err != nil || earlier.Fresh {
		t.Fatalf("write from the queue: Fresh=%v err=%v, want false", earlier.Fresh, err)
	}
}

// prev_status of a postback written twice in the same second is the status
// before it, not the first write of itself; a different status stored in that
// same second still counts as the predecessor.
func TestCHPrevStatusIgnoresOwnRowInTheSameSecond(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	ctsClick(t, ch, "cts_prev2", "cts_c_prev2")
	time.Sleep(200 * time.Millisecond)
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for _, s := range []struct {
		status string
		at     time.Duration
	}{{"lead", 0}, {"sale", 10 * time.Second}, {"sale", 10 * time.Second}, {"rejected", 10 * time.Second}} {
		if _, err := ch.RecordConversion(ctx, PostbackIn{CID: "cts_c_prev2", Profit: 1, Status: s.status, At: t0.Add(s.at)}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := ch.conn.Query(ctx, `SELECT status, prev_status FROM conversions WHERE cid = ? ORDER BY ts, status, prev_status`, "cts_c_prev2")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][]string{}
	for rows.Next() {
		var status, prev string
		if err := rows.Scan(&status, &prev); err != nil {
			t.Fatal(err)
		}
		got[status] = append(got[status], prev)
	}
	for _, p := range got["sale"] {
		if p != "lead" {
			t.Fatalf("sale rows have prev_status %v, want every one to be lead", got["sale"])
		}
	}
	for _, p := range got["rejected"] {
		if p != "sale" && p != "lead" {
			t.Fatalf("rejected in the same second as a sale has prev_status %q, want sale", p)
		}
	}
}
