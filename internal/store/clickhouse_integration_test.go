//go:build integration

// Integration tests for the ClickHouse store. Require a running ClickHouse
// (make infra-up). Run with:
//
//	go test -tags=integration ./internal/store/ -run CH
//
// Connection parameters are taken from KUZTDS_CLICKHOUSE_* (default — dev-docker).
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/logbuf"
)

func chFromEnv(t *testing.T) *CH {
	t.Helper()
	addr := envDef("KUZTDS_CLICKHOUSE_ADDR", "localhost:9000")
	db := envDef("KUZTDS_CLICKHOUSE_DB", "kuztds")
	user := envDef("KUZTDS_CLICKHOUSE_USER", "kuztds")
	pass := envDef("KUZTDS_CLICKHOUSE_PASSWORD", "devpassword")
	ch, err := OpenCH(addr, db, user, pass)
	if err != nil {
		t.Skipf("ClickHouse unavailable (%s): %v — skipping integration tests", addr, err)
	}
	t.Cleanup(func() { ch.Close() })
	return ch
}

func envDef(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestCHRoundTrip inserts events into a unique group, checks aggregates and
// reads, runs a postback, then cleans up after itself.
func TestCHRoundTrip(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()

	group := fmt.Sprintf("itest_%d", os.Getpid())
	now := time.Now()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	t.Cleanup(func() { cleanupGroup(t, ch, group) })

	events := []logbuf.Event{
		{Ts: now, GroupID: group, GroupName: group, Stream: "s1", Out: "o1",
			Device: "phone", Country: "ru", City: "moscow", OS: "Android", Browser: "Chrome",
			Brand: "Samsung", Uniq: 1, IP: "1.2.3.4", CID: "cid_a", Keyword: "kw"},
		{Ts: now, GroupID: group, GroupName: group, Stream: "s1", Out: "o2",
			Device: "computer", Country: "us", Uniq: 0, IP: "5.6.7.8", CID: "cid_b"},
		{Ts: now, GroupID: group, GroupName: group, Stream: "s2", Out: "o3",
			Device: "phone", Country: "ru", Bot: "google", Uniq: 0, IP: "9.9.9.9", CID: "cid_c"},
	}
	if err := ch.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	// give the insert time to become visible
	time.Sleep(200 * time.Millisecond)

	// Summary (across all groups in the window) — must be at least our 3.
	sum, err := ch.Summary(ctx, from, to)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Total < 3 {
		t.Errorf("Summary.Total = %d, expected >= 3", sum.Total)
	}

	// Logs filtered by group — exactly 3 rows.
	rows, total, err := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, Limit: 100})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if total != 3 || len(rows) != 3 {
		t.Errorf("Logs: total=%d len=%d, expected 3", total, len(rows))
	}

	// Logs bots only — 1 row.
	_, botTotal, err := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, BotsOnly: true, Limit: 100})
	if err != nil {
		t.Fatalf("Logs bots: %v", err)
	}
	if botTotal != 1 {
		t.Errorf("BotsOnly total = %d, expected 1", botTotal)
	}

	// Logs humans only — 2 rows.
	_, humanTotal, _ := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, HumansOnly: true, Limit: 100})
	if humanTotal != 2 {
		t.Errorf("HumansOnly total = %d, expected 2", humanTotal)
	}

	// Logs filtered by country ru — 2.
	_, ruTotal, _ := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, Country: []string{"ru"}, Limit: 100})
	if ruTotal != 2 {
		t.Errorf("Country=ru total = %d, expected 2", ruTotal)
	}

	// TimeSeries — at least one point.
	ts, err := ch.TimeSeries(ctx, from, to, 3600)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(ts) == 0 {
		t.Error("TimeSeries is empty")
	}

	// Breakdown by device — contains phone.
	kv, err := ch.Breakdown(ctx, from, to, "device", 50)
	if err != nil {
		t.Fatalf("Breakdown: %v", err)
	}
	if !containsKey(kv, "phone") {
		t.Errorf("Breakdown device does not contain phone: %v", kv)
	}
	// unknown dimension → error.
	if _, err := ch.Breakdown(ctx, from, to, "evil; DROP", 50); err == nil {
		t.Error("Breakdown with an unknown dim must return an error")
	}

	// Postback for cid_a.
	if _, err := ch.RecordPostback(ctx, "cid_a", 2.5, ""); err != nil {
		t.Fatalf("RecordPostback: %v", err)
	}
	// nonexistent cid → error.
	if _, err := ch.RecordPostback(ctx, "nope_cid", 1.0, ""); err == nil {
		t.Error("RecordPostback for an unknown cid must return an error")
	}
	time.Sleep(200 * time.Millisecond)

	pb, profit, err := ch.Postbacks(ctx, from, to, group, 100)
	if err != nil {
		t.Fatalf("Postbacks: %v", err)
	}
	if len(pb) != 1 || profit != 2.5 {
		t.Errorf("Postbacks: rows=%d profit=%v, expected 1 row and 2.5", len(pb), profit)
	}
	if len(pb) == 1 && pb[0].CID != "cid_a" {
		t.Errorf("Postback cid = %q, expected cid_a", pb[0].CID)
	}

	// The network columns exist after ensureSchema and round-trip through Logs.
	if !ch.NetworkColumns() {
		t.Fatal("asn/org/timezone/via columns missing after ensureSchema")
	}
	// A visit that entered through <group>/s1 and was served by another group:
	// it is logged there, and counted here as forwarded by s1.
	fwd := logbuf.Event{Ts: now, GroupID: group + "_b", GroupName: group, Stream: "landed", Device: "phone",
		Country: "de", Uniq: 1, IP: "3.3.3.3", CID: "cid_f", ASN: 3320, Org: "Deutsche Telekom AG",
		Timezone: "Europe/Berlin", Via: group + "/s1"}
	if err := ch.InsertEvents(ctx, []logbuf.Event{fwd}); err != nil {
		t.Fatalf("InsertEvents (forwarded): %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	netRows, _, err := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, Country: []string{"de"}, Limit: 10})
	if err != nil || len(netRows) != 1 {
		t.Fatalf("Logs (network row): %v, %d rows", err, len(netRows))
	}
	if r := netRows[0]; r.ASN != 3320 || r.Org != "Deutsche Telekom AG" || r.Timezone != "Europe/Berlin" {
		t.Errorf("network fields = %d %q %q", r.ASN, r.Org, r.Timezone)
	}
	if kv, err := ch.Breakdown(ctx, from, to, "org", 1000); err != nil || !containsKey(kv, "Deutsche Telekom AG") {
		t.Errorf("Breakdown by org: %v, %v", kv, err)
	}
	if kv, err := ch.Breakdown(ctx, from, to, "asn", 1000); err != nil || !containsKey(kv, "AS3320") {
		t.Errorf("Breakdown by asn: %v, %v", kv, err)
	}

	// Performance: s1 = 2 own hits + 1 forwarded / 2 unique / 0 bots / 1
	// conversion of 2.5; s2 = 1 hit, a bot, no conversions.
	perf, err := ch.Performance(ctx, from, to)
	if err != nil {
		t.Fatalf("Performance: %v", err)
	}
	got := map[string]PerfRow{}
	for _, p := range perf {
		if p.GroupID == group {
			got[p.Stream] = p
		}
	}
	if s1 := got["s1"]; s1.Hits != 3 || s1.Forwarded != 1 || s1.Unique != 2 || s1.Bots != 0 || s1.Conv != 1 || s1.Profit != 2.5 {
		t.Errorf("Performance s1 = %+v, expected 3 hits (1 forwarded) / 2 unique / 0 bots, 1 conv of 2.5", s1)
	}
	if s2 := got["s2"]; s2.Hits != 1 || s2.Bots != 1 || s2.Conv != 0 {
		t.Errorf("Performance s2 = %+v, expected 1 hit, 1 bot, 0 conv", s2)
	}
	if _, err := ch.RecordPostback(ctx, "cid_a", 1, "lead"); err != nil {
		t.Fatalf("lead: %v", err)
	}
	if _, err := ch.RecordPostback(ctx, "cid_a", 9, "sale"); err != nil {
		t.Fatalf("sale: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	pb, _, err = ch.Postbacks(ctx, from, to, group, 100)
	if err != nil {
		t.Fatalf("Postbacks after status: %v", err)
	}
	seen := map[string]float64{}
	for _, row := range pb {
		if row.CID == "cid_a" {
			seen[row.Status] = row.Profit
		}
	}
	if seen["lead"] != 1 || seen["sale"] != 9 {
		t.Fatalf("lead payout 1 and sale payout 9, got %v", seen)
	}

	// DeleteGroupLogs keys on group_id: after the mutation the group's rows are gone.
	if err := ch.DeleteGroupLogs(ctx, group); err != nil {
		t.Fatalf("DeleteGroupLogs: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		// The forwarded visit belongs to another group id and must stay.
		_, left, _ := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, Stream: []string{"s1", "s2"}, Limit: 10})
		if left == 0 || time.Now().After(deadline) {
			if left != 0 {
				t.Errorf("DeleteGroupLogs: %d rows still present after 10 s", left)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, kept, _ := ch.Logs(ctx, LogFilter{From: from, To: to, Group: []string{group}, Stream: []string{"landed"}, Limit: 10}); kept != 1 {
		t.Errorf("DeleteGroupLogs removed another group's row: %d left, expected 1", kept)
	}
}

// TestCHExtColumns inserts a click with the ext fields, records two
// conversions and checks the journal, value list and a field filter.
func TestCHExtColumns(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	group := fmt.Sprintf("itest_ext_%d", os.Getpid())
	cid := fmt.Sprintf("ext_%d", os.Getpid())
	now := time.Now().UTC().Truncate(time.Second)
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)
	t.Cleanup(func() { cleanupGroup(t, ch, group) })
	ev := logbuf.Event{
		Ts: now, GroupID: group, GroupName: group, Stream: "s1", Country: "pl",
		CID: cid, Site: "exland", XRW: "XMLHttpRequest", IP: "8.8.8.8",
	}
	if err := ch.InsertEvents(ctx, []logbuf.Event{ev}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := ch.RecordConversion(ctx, PostbackIn{CID: cid, Profit: 3, Status: "Approved", TID: "T1"}); err != nil {
		t.Fatalf("RecordConversion: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	rows, _, err := ch.QueryPostbacks(ctx, PostbackQuery{
		From: from, To: to, Group: group, Limit: 20,
		Conds: []Cond{{Field: "status", Op: "in", Values: []string{"Sale", "Approved"}}},
	})
	if err != nil {
		t.Fatalf("QueryPostbacks: %v", err)
	}
	var approved *PostbackRow
	for i := range rows {
		if rows[i].CID == cid && rows[i].Status == "approved" {
			approved = &rows[i]
		}
	}
	if approved == nil {
		t.Fatalf("approved row missing: %+v", rows)
	}
	if approved.OriginalStatus != "Approved" || approved.Status != "approved" || approved.PrevStatus != "" {
		t.Fatalf("status fields: %+v", approved)
	}
	if approved.ClickTS.Unix() != now.Unix() {
		t.Fatalf("click_ts %s, want %s", approved.ClickTS, now)
	}
	if approved.Site != "exland" || approved.XRW != "XMLHttpRequest" {
		t.Fatalf("site/xrw: %+v", approved)
	}
	if _, err := ch.RecordConversion(ctx, PostbackIn{CID: cid, Profit: 4, Status: "sale", TID: "T2"}); err != nil {
		t.Fatalf("second conversion: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	rows, _, err = ch.QueryPostbacks(ctx, PostbackQuery{
		From: from, To: to, Group: group, Limit: 20,
		Conds: []Cond{{Field: "status", Op: "in", Values: []string{"Sale", "Approved"}}},
	})
	if err != nil {
		t.Fatalf("QueryPostbacks sale: %v", err)
	}
	var sale *PostbackRow
	for i := range rows {
		if rows[i].CID == cid && rows[i].Status == "sale" {
			sale = &rows[i]
		}
	}
	if sale == nil || sale.PrevStatus != "approved" {
		t.Fatalf("sale prev: %+v", sale)
	}
	if _, err := ch.Values(ctx, ValuesQuery{Table: "events", Field: "country", From: from, To: to, Limit: 50}); err != nil {
		t.Fatalf("Values country: %v", err)
	}
	if _, err := ch.Values(ctx, ValuesQuery{Table: "events", Field: "ip", From: from, To: to, Limit: 50}); !errors.Is(err, ErrBadCond) {
		t.Fatalf("Values ip: %v", err)
	}
	got, _, err := ch.Logs(ctx, LogFilter{
		From: from, To: to, Group: []string{group}, Limit: 10,
		Conds: []Cond{{Field: "xrw", Op: "eq", Values: []string{"XMLHttpRequest"}}},
	})
	if err != nil || len(got) != 1 || got[0].XRW != "XMLHttpRequest" || got[0].Site != "exland" {
		t.Fatalf("Logs xrw: %v %+v", err, got)
	}
}

// TestCHCost inserts one priced click and one without a price, then checks
// facts, performance and the time series. Cleanup deletes the group's events.
func TestCHCost(t *testing.T) {
	ch := chFromEnv(t)
	if !ch.costCols {
		t.Fatal("cost columns are not ready")
	}
	ctx := context.Background()
	group := fmt.Sprintf("itest_cost_%d", os.Getpid())
	now := time.Now()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)
	t.Cleanup(func() { cleanupGroup(t, ch, group) })
	events := []logbuf.Event{
		{Ts: now, GroupID: group, GroupName: group, Stream: "s1", IP: "10.0.0.1", CID: group + "_a",
			CostCents: 150, CostCur: "USD"},
		{Ts: now, GroupID: group, GroupName: group, Stream: "s1", IP: "10.0.0.2", CID: group + "_b"},
	}
	if err := ch.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	facts, err := ch.ClickFacts(ctx, from, to)
	if err != nil {
		t.Fatalf("ClickFacts: %v", err)
	}
	var usd int64
	var bare bool
	for _, row := range facts.Clicks {
		if row.GroupID != group {
			continue
		}
		if row.CostCur == "USD" {
			usd += row.CostCents
		}
		if row.CostCur == "" {
			bare = true
			if row.CostCents != 0 {
				t.Fatalf("unpriced row cost_cents = %d", row.CostCents)
			}
		}
	}
	if usd != 150 || !bare {
		t.Fatalf("facts usd=%d bare=%v rows=%+v", usd, bare, facts.Clicks)
	}

	perf, err := ch.Performance(ctx, from, to)
	if err != nil {
		t.Fatalf("Performance: %v", err)
	}
	var row PerfRow
	var found bool
	for _, p := range perf {
		if p.GroupID == group && p.Stream == "s1" {
			row = p
			found = true
		}
	}
	if !found || row.Cost["USD"] != 150 {
		t.Fatalf("performance found=%v row=%+v", found, row)
	}

	pts, err := ch.TimeSeriesFiltered(ctx, from, to, 3600, []Cond{{
		Field: "campaign_id", Op: "eq", Values: []string{group},
	}})
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	var sum int64
	for _, p := range pts {
		sum += p.Cost["USD"]
	}
	if sum != 150 {
		t.Fatalf("series USD = %d", sum)
	}
}

// TestCHPerformanceByGroupID checks two campaigns that share a display name
// keep their own conversion. Cleanup deletes the group's events and conversions.
func TestCHPerformanceByGroupID(t *testing.T) {
	ch := chFromEnv(t)
	if !ch.extConv {
		t.Fatal("conversions group_id is not ready")
	}
	ctx := context.Background()
	name := fmt.Sprintf("itest_perf_%d", os.Getpid())
	idA, idB := name+"_a", name+"_b"
	now := time.Now()
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	t.Cleanup(func() { cleanupGroup(t, ch, name) })
	events := []logbuf.Event{
		{Ts: now, GroupID: idA, GroupName: name, Stream: "s1", IP: "10.1.0.1", CID: idA},
		{Ts: now, GroupID: idB, GroupName: name, Stream: "s1", IP: "10.1.0.2", CID: idB},
	}
	if err := ch.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := ch.RecordPostback(ctx, idA, 1, "lead"); err != nil {
		t.Fatalf("conv a: %v", err)
	}
	if _, err := ch.RecordPostback(ctx, idB, 2, "sale"); err != nil {
		t.Fatalf("conv b: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	perf, err := ch.Performance(ctx, from, to)
	if err != nil {
		t.Fatalf("Performance: %v", err)
	}
	got := map[string]PerfRow{}
	for _, p := range perf {
		if p.GroupID == idA || p.GroupID == idB {
			got[p.GroupID] = p
		}
	}
	if a, b := got[idA], got[idB]; a.Conv != 1 || b.Conv != 1 || len(got) != 2 {
		t.Fatalf("rows %+v", got)
	}
	facts, err := ch.ClickFactsFiltered(ctx, from, to, []Cond{{
		Field: "campaign_id", Op: "eq", Values: []string{idA},
	}})
	if err != nil {
		t.Fatalf("ClickFactsFiltered: %v", err)
	}
	if len(facts.Clicks) != 1 || facts.Clicks[0].GroupID != idA || facts.Clicks[0].Hits != 1 {
		t.Fatalf("clicks %+v", facts.Clicks)
	}
	var saw bool
	for _, st := range facts.Status {
		if st.GroupID == idA && st.N == 1 {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("status %+v", facts.Status)
	}
}

// cleanupGroup deletes one test group's rows from events, postbacks and conversions.
// t receives a log line when a mutation is refused. ch is the open client.
// name is the group_name the test inserted and contains no quotes.
// It does not fail the test.
func cleanupGroup(t *testing.T, ch *CH, name string) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"events", "postbacks", "conversions"} {
		q := fmt.Sprintf("ALTER TABLE %s DELETE WHERE group_name = '%s'", table, name)
		if err := ch.conn.Exec(ctx, q); err != nil {
			t.Logf("cleanup %s %s: %v", table, name, err)
		}
	}
}

func containsKey(kv []KV, key string) bool {
	for _, x := range kv {
		if x.Key == key {
			return true
		}
	}
	return false
}
