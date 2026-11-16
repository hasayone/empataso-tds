//go:build integration

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egerkuzma/emtds/internal/logbuf"
)

// keywordConv is one conversion row the keyword test inserts.
type keywordConv struct {
	ts      time.Time
	cid     string
	status  string
	profit  float64
	keyword string
}

// insertKeywordConvs writes rows into conversions of the test database for campaign id.
// t fails the test on a batch error. The rows carry the click's keyword as an import does.
func insertKeywordConvs(t *testing.T, ch *CH, name, id string, rows []keywordConv) {
	t.Helper()
	ctx := context.Background()
	batch, err := ch.conn.PrepareBatch(ctx, `INSERT INTO conversions (ts, group_name, stream_name, cid, status, profit, group_id, keyword)`)
	if err != nil {
		t.Fatalf("PrepareBatch: %v", err)
	}
	for _, r := range rows {
		if err := batch.Append(r.ts, name, "s1", r.cid, r.status, r.profit, id, r.keyword); err != nil {
			t.Fatalf("Append %s: %v", r.cid, err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestCHKeywordStats runs the keyword table and trend on a campaign of its own in the
// throwaway test database. Clicks of bots are left out, every conversion status counts,
// a lead of a click rejected later does not, rows come in payout order, and the daily
// trend adds up to the row of the table.
func TestCHKeywordStats(t *testing.T) {
	ch := chFromEnv(t)
	if !ch.extConv {
		t.Fatal("conversions ext wave is not ready")
	}
	ctx := context.Background()
	name := fmt.Sprintf("itest_kw_%d", os.Getpid())
	id := name + "_id"
	now := time.Now().UTC().Truncate(time.Second)
	older := now.Add(-26 * time.Hour)
	from, to := now.Add(-48*time.Hour), now.Add(time.Hour)

	click := func(ts time.Time, cid, kw, bot string, uniq uint8) logbuf.Event {
		return logbuf.Event{Ts: ts, GroupID: id, GroupName: name, Stream: "s1", CID: cid, Keyword: kw, Bot: bot, Uniq: uniq}
	}
	events := []logbuf.Event{
		click(now, "a1", "alpha", "", 1), click(now, "a2", "alpha", "", 1), click(now, "a3", "alpha", "", 0),
		click(now, "a4", "alpha", "google", 1),
		click(now, "b1", "beta", "", 1), click(older, "b2", "beta", "", 1),
		click(now, "n1", "", "", 1),
		click(now, "x1", "botonly", "bing", 1),
	}
	if err := ch.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	insertKeywordConvs(t, ch, name, id, []keywordConv{
		{now, "a1", "sale", 10, "alpha"},
		{now, "a2", "lead", 2, "alpha"},
		{now, "a3", "rejected", 0, "alpha"},
		{older, "b1", "sale", 30, "beta"},
		{now.Add(-2 * time.Minute), "b2", "lead", 5, "beta"},
		{now.Add(-time.Minute), "b2", "rejected", 0, "beta"},
	})
	time.Sleep(300 * time.Millisecond)
	conds := []Cond{{Field: "campaign_id", Op: "eq", Values: []string{id}}}

	got, err := ch.KeywordStats(ctx, from, to, conds, KeywordModeLast)
	if err != nil {
		t.Fatalf("KeywordStats: %v", err)
	}
	if !got.ConvReady || got.Truncated {
		t.Fatalf("ready=%v truncated=%v", got.ConvReady, got.Truncated)
	}
	want := []KeywordRow{
		{Keyword: "beta", Hits: 2, Unique: 2, Conv: 2, Leads: 0, Sales: 1, Rejected: 1, Profit: 30},
		{Keyword: "alpha", Hits: 3, Unique: 2, Conv: 3, Leads: 1, Sales: 1, Rejected: 1, Profit: 12},
		{Keyword: "", Hits: 1, Unique: 1},
	}
	if len(got.Rows) != len(want) {
		t.Fatalf("rows %+v, want %+v (bots out, voided lead out, payout order)", got.Rows, want)
	}
	for i, w := range want {
		if got.Rows[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got.Rows[i], w)
		}
	}

	only, err := ch.KeywordStats(ctx, from, to, append([]Cond{{Field: "keyword", Op: "contains", Values: []string{"alp"}}}, conds...), KeywordModeLast)
	if err != nil || len(only.Rows) != 1 || only.Rows[0].Keyword != "alpha" || only.Rows[0].Conv != 3 {
		t.Fatalf("keyword filter: %+v err=%v", only, err)
	}
	noConv := append([]Cond{{Field: "redirect", Op: "eq", Values: []string{""}}}, conds...)
	clicks, err := ch.KeywordStats(ctx, from, to, noConv, KeywordModeLast)
	if err != nil || clicks.ConvReady || len(clicks.Rows) != 3 {
		t.Fatalf("a filter without a conversion column: %+v err=%v", clicks, err)
	}
	if r := clicks.Rows[0]; r.Keyword != "alpha" || r.Hits != 3 || r.Conv != 0 || r.Profit != 0 {
		t.Errorf("clicks-only first row %+v, want alpha with clicks and no money", r)
	}

	for _, tz := range []string{"", "America/New_York", "Pacific/Kiritimati"} {
		trend, err := ch.KeywordTrend(ctx, from, to, conds, KeywordTrendParams{Top: 2, TZ: tz})
		if err != nil {
			t.Fatalf("KeywordTrend %q: %v", tz, err)
		}
		if !trend.ConvReady || len(trend.Series) != 2 || trend.Series[0].Keyword != "beta" || trend.Series[1].Keyword != "alpha" {
			t.Fatalf("trend %q series %+v", tz, trend.Series)
		}
		if len(trend.Days) < 2 {
			t.Fatalf("trend %q days %v", tz, trend.Days)
		}
		for i, s := range trend.Series {
			row := got.Rows[i]
			if s.Keyword != row.Keyword || sumInt(s.Hits) != row.Hits || sumInt(s.Conv) != row.Conv || sumFloat(s.Profit) != row.Profit {
				t.Errorf("trend %q series %+v does not add up to row %+v", tz, s, row)
			}
			if len(s.Hits) != len(trend.Days) || len(s.Conv) != len(trend.Days) || len(s.Profit) != len(trend.Days) {
				t.Errorf("trend %q series %s is not laid on %d days", tz, s.Keyword, len(trend.Days))
			}
		}
		active := 0
		for _, h := range trend.Series[0].Hits {
			if h > 0 {
				active++
			}
		}
		if active != 2 {
			t.Errorf("trend %q: beta has clicks on %d days, want 2", tz, active)
		}
	}
	one, err := ch.KeywordTrend(ctx, from, to, conds, KeywordTrendParams{Top: 1})
	if err != nil || len(one.Series) != 1 || one.Series[0].Keyword != "beta" {
		t.Fatalf("top 1: %+v err=%v", one, err)
	}
	plain, err := ch.KeywordTrend(ctx, from, to, noConv, KeywordTrendParams{Top: 5})
	if err != nil || plain.ConvReady || len(plain.Series) != 3 {
		t.Fatalf("clicks-only trend: %+v err=%v", plain, err)
	}
	for _, s := range plain.Series {
		if s.Conv != nil || s.Profit != nil {
			t.Errorf("clicks-only series %+v has conversion arrays", s)
		}
	}
}

// TestCHKeywordStatsInvalidUTF8 checks a keyword with broken bytes comes out valid in the
// table and the trend, and clicks and conversions of it land on one row.
func TestCHKeywordStatsInvalidUTF8(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	name := fmt.Sprintf("itest_kw8_%d", os.Getpid())
	id := name + "_id"
	now := time.Now().UTC().Truncate(time.Second)
	bad := "caf\xe9 \xff"
	if err := ch.InsertEvents(ctx, []logbuf.Event{
		{Ts: now, GroupID: id, GroupName: name, Stream: "s1", CID: "u1", Keyword: bad, Uniq: 1},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	insertKeywordConvs(t, ch, name, id, []keywordConv{{now, "u1", "sale", 7, bad}})
	time.Sleep(300 * time.Millisecond)
	conds := []Cond{{Field: "campaign_id", Op: "eq", Values: []string{id}}}
	from, to := now.Add(-time.Hour), now.Add(time.Hour)

	got, err := ch.KeywordStats(ctx, from, to, conds, KeywordModeLast)
	if err != nil || len(got.Rows) != 1 {
		t.Fatalf("rows %+v err=%v, want one row for both halves", got.Rows, err)
	}
	if r := got.Rows[0]; !utf8.ValidString(r.Keyword) || r.Hits != 1 || r.Conv != 1 || r.Profit != 7 {
		t.Errorf("row %+v", r)
	}
	trend, err := ch.KeywordTrend(ctx, from, to, conds, KeywordTrendParams{Top: 5})
	if err != nil || len(trend.Series) != 1 {
		t.Fatalf("trend %+v err=%v", trend, err)
	}
	if s := trend.Series[0]; s.Keyword != got.Rows[0].Keyword || sumInt(s.Hits) != 1 || sumInt(s.Conv) != 1 || sumFloat(s.Profit) != 7 {
		t.Errorf("series %+v does not match the row %+v", s, got.Rows[0])
	}
}

// TestCHKeywordStatsHugeScope checks a caller scoped to thousands of campaigns gets the
// table and the trend: the campaign_id list passes the default max_query_size, which the
// keyword queries raise. A campaign outside the scope stays out of the answer.
func TestCHKeywordStatsHugeScope(t *testing.T) {
	ch := chFromEnv(t)
	ctx := context.Background()
	name := fmt.Sprintf("itest_kws_%d", os.Getpid())
	id := name + "_id"
	now := time.Now().UTC().Truncate(time.Second)
	click := func(group, cid, kw string) logbuf.Event {
		return logbuf.Event{Ts: now, GroupID: group, GroupName: group, Stream: "s1", CID: cid, Keyword: kw, Uniq: 1}
	}
	if err := ch.InsertEvents(ctx, []logbuf.Event{
		click(id, "s1", "mine"), click(id, "s2", "mine"), click(name+"_other", "o1", "outside"),
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	insertKeywordConvs(t, ch, name, id, []keywordConv{{now, "s1", "sale", 9, "mine"}})
	time.Sleep(300 * time.Millisecond)

	ids := make([]string, 0, 30001)
	for i := 0; i < 30000; i++ {
		ids = append(ids, fmt.Sprintf("keitaro_campaign_%08d", i))
	}
	ids = append(ids, id)
	conds := []Cond{{Field: "campaign_id", Op: "in", Values: ids, Scope: true}}
	from, to := now.Add(-time.Hour), now.Add(time.Hour)

	p, _, _ := ch.planKeywords(conds, KeywordModeLast)
	union, args := ch.keywordUnion(p, from, to)
	sql := keywordStatsSQL(union)
	// The driver inlines the list before the send, so the text the server parses is each half's share of it.
	if len(ids)*(len(ids[0])+3) <= 262144 {
		t.Fatal("the scope list is below the server default max_query_size: the test proves nothing")
	}
	if rows, err := ch.conn.Query(ctx, sql, args...); err == nil {
		rows.Close()
		t.Log("the server accepted the query with its default max_query_size")
	}
	got, err := ch.KeywordStats(ctx, from, to, conds, KeywordModeLast)
	if err != nil {
		t.Fatalf("KeywordStats: %v", err)
	}
	if len(got.Rows) != 1 || got.Rows[0].Keyword != "mine" || got.Rows[0].Hits != 2 || got.Rows[0].Conv != 1 || got.Rows[0].Profit != 9 {
		t.Fatalf("rows %+v, want only mine: 2 clicks, 1 conversion, 9", got.Rows)
	}
	trend, err := ch.KeywordTrend(ctx, from, to, conds, KeywordTrendParams{Top: 5, TZ: "UTC"})
	if err != nil {
		t.Fatalf("KeywordTrend: %v", err)
	}
	if len(trend.Series) != 1 || trend.Series[0].Keyword != "mine" || sumInt(trend.Series[0].Hits) != 2 ||
		sumInt(trend.Series[0].Conv) != 1 || sumFloat(trend.Series[0].Profit) != 9 {
		t.Fatalf("trend %+v, want only mine", trend.Series)
	}
}
