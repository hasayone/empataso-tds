package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestKeywordPageOrder checks every sort key has an expression, a column sort keeps the payout
// order and the keyword as the tie-break, the ratio is taken from sums, and an unknown key is refused.
func TestKeywordPageOrder(t *testing.T) {
	for _, k := range KeywordSortKeys {
		if _, ok := keywordSortExpr[k]; !ok {
			t.Errorf("sort key %q has no expression", k)
		}
	}
	if len(keywordSortExpr) != len(KeywordSortKeys) || !slices.Contains(KeywordSortKeys, KeywordDefaultSort) {
		t.Error("the key list and the expressions differ")
	}
	if got, ok := keywordPageOrder("", true); !ok || got != keywordOrder {
		t.Errorf("default order %q", got)
	}
	got, ok := keywordPageOrder("hits", false)
	if !ok || got != "ORDER BY sum(hits) ASC, sum(profit_s) DESC, sum(hits) DESC, k ASC" {
		t.Errorf("hits asc %q", got)
	}
	if got, _ := keywordPageOrder("cr", true); !strings.HasPrefix(got, "ORDER BY sum(conv) / greatest(sum(hits), 1) DESC,") {
		t.Errorf("cr desc %q", got)
	}
	if _, ok := keywordPageOrder("hits; DROP", true); ok {
		t.Error("an unknown key was accepted")
	}
}

// TestKeywordStatsPageQueries checks the page asks the totals first, then one sorted page with
// LIMIT and OFFSET, both under the keyword query settings; an offset past the end skips the page
// query, and a bad page is ErrBadCond before any query.
func TestKeywordStatsPageQueries(t *testing.T) {
	conn := &fakeConn{results: []*fakeRows{
		{data: [][]any{{uint64(3), uint64(30), uint64(20), uint64(4), uint64(1), uint64(2), uint64(1), 12.5, 3.0}}},
		{data: [][]any{{"b", uint64(5), uint64(4), uint64(1), uint64(0), uint64(1), uint64(0), 2.0, 0.0}}},
	}}
	c := keywordClient()
	c.conn = conn
	from, to := keywordWindow()
	got, err := c.KeywordStatsPage(context.Background(), from, to, nil, KeywordModeLast, PageQuery{Sort: "uniq", Desc: true, Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || got.Totals.Hits != 30 || got.Totals.Unique != 20 || got.Totals.Profit != 12.5 || got.Totals.DepSum != 3 ||
		len(got.Rows) != 1 || got.Rows[0].Keyword != "b" || got.Sort != "uniq" || got.Dir != "desc" || !got.ConvReady || got.Truncated {
		t.Fatalf("page %+v", got)
	}
	if len(conn.queries) != 2 || !strings.Contains(conn.queries[0], "uniqExact(k)") || strings.Contains(conn.queries[0], "LIMIT") {
		t.Fatalf("totals query %q", conn.queries)
	}
	if q := squash(conn.queries[1]); !strings.Contains(q, "ORDER BY sum(uniq_n) DESC, sum(profit_s) DESC") || !strings.HasSuffix(q, "LIMIT 1 OFFSET 1") {
		t.Fatalf("page query %s", q)
	}
	for i, ctx := range conn.ctxs {
		if s, ok := analyticsSettingsFrom(ctx); !ok || s["max_query_size"] != keywordMaxQuerySize {
			t.Errorf("query %d settings %v", i, s)
		}
	}

	past := &fakeConn{results: []*fakeRows{{data: [][]any{{uint64(3), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), 0.0, 0.0}}}}}
	c.conn = past
	got, err = c.KeywordStatsPage(context.Background(), from, to, nil, KeywordModeLast, PageQuery{Offset: 3, Limit: 10})
	if err != nil || len(past.queries) != 1 || got.Rows == nil || len(got.Rows) != 0 || got.Total != 3 || got.Sort != KeywordDefaultSort {
		t.Fatalf("past the end: err=%v queries=%d page=%+v", err, len(past.queries), got)
	}

	none := &fakeConn{}
	c.conn = none
	for _, q := range []PageQuery{{Limit: 0}, {Limit: PageMaxRows + 1}, {Limit: 5, Offset: -1}, {Limit: 5, Sort: "nope"}} {
		if _, err := c.KeywordStatsPage(context.Background(), from, to, nil, KeywordModeLast, q); !errors.Is(err, ErrBadCond) {
			t.Errorf("%+v: err=%v", q, err)
		}
	}
	if len(none.queries) != 0 {
		t.Errorf("a bad page ran %d queries", len(none.queries))
	}
}
