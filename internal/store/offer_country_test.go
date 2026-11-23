package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestOfferCountryFacts checks the offer country split: an empty country list
// does not query, and two countries stay on their own rows. Hits, click revenue
// and conversion profit of one country are not added into the other.
// The fixture countries are the lower-case codes the engine writes. The query
// folds the stored code, so the same country kept in upper case is not a zero.
func TestOfferCountryFacts(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	closed := &fakeConn{fail: errors.New("should not query")}
	quiet := readyClient()
	quiet.clickCols = true
	quiet.conn = closed
	got, err := quiet.OfferCountryFacts(context.Background(), from, to, "o1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed.queries) != 0 || len(got.Clicks) != 0 || len(got.Status) != 0 || got.Ready {
		t.Fatalf("empty list queried or returned rows: %+v queries=%d", got, len(closed.queries))
	}

	one := &fakeConn{results: []*fakeRows{
		{data: [][]any{{"ru", uint64(10), uint64(4), uint64(4), "USD", int64(100), 5.0}}},
		{data: [][]any{{"ru", "o1", "sale", uint64(2), 5.0, 0.0, 0.0}}},
	}}
	single := readyClient()
	single.clickCols, single.costCols, single.revCols, single.depsumConv = true, true, true, true
	single.conn = one
	if _, err := single.OfferCountryFacts(context.Background(), from, to, "o1", []string{"ru"}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.queries[0], "lower(country) IN (?)") || strings.Contains(one.queries[0], "?,?") || strings.Contains(one.queries[0], " country IN (") {
		t.Fatalf("one country is not a single lower-case placeholder:\n%s", one.queries[0])
	}
	if !strings.Contains(one.queries[0], "lower(country) AS cc") || strings.Contains(one.queries[0], "AS country") || !strings.Contains(one.queries[0], "GROUP BY lower(country)") {
		t.Fatalf("one country is not folded:\n%s", one.queries[0])
	}
	if len(one.args[0]) != 4 || one.args[0][2] != "o1" || one.args[0][3] != "ru" {
		t.Fatalf("one country args %#v", one.args[0])
	}

	conn := &fakeConn{results: []*fakeRows{
		{data: [][]any{
			{"ru", uint64(10), uint64(4), uint64(4), "USD", int64(100), 5.0},
			{"ru", uint64(1), uint64(1), uint64(1), "EUR", int64(20), 1.0},
			{"de", uint64(3), uint64(2), uint64(2), "EUR", int64(50), 9.0},
		}},
		{data: [][]any{
			{"ru", "o1", "sale", uint64(2), 5.0, 1.0, 0.0},
			{"de", "o1", "lead", uint64(1), 9.0, 0.0, 0.0},
		}},
	}}
	c := readyClient()
	c.clickCols, c.costCols, c.revCols, c.depsumConv = true, true, true, true
	c.conn = conn
	scope := []Cond{{Field: "campaign_id", Op: "eq", Values: []string{"camp"}, Scope: true}}
	got, err = c.OfferCountryFacts(context.Background(), from, to, "o1", []string{"ru", "de"}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.queries) != 2 {
		t.Fatalf("%d queries, want clicks and status", len(conn.queries))
	}
	for i, q := range conn.queries {
		if !strings.Contains(q, "offer_id = ?") || !strings.Contains(q, "lower(country) IN (?,?)") || !strings.Contains(q, "GROUP BY lower(country)") || strings.Contains(q, " country IN (") {
			t.Fatalf("query %d is not limited to the offer and its countries:\n%s", i, q)
		}
		if strings.Count(q, "?") != len(conn.args[i]) {
			t.Fatalf("query %d: %d placeholders, %d args\n%s", i, strings.Count(q, "?"), len(conn.args[i]), q)
		}
	}
	if !strings.Contains(conn.queries[1], "cid IN (SELECT cid FROM events") || !strings.Contains(conn.queries[1], "group_id IN (?)") {
		t.Fatalf("status query does not keep the campaign scope:\n%s", conn.queries[1])
	}
	var ruHits, deHits int64
	var ruUSD, ruEUR, deEUR int64
	var ruRev, deRev float64
	for _, row := range got.Clicks {
		switch row.Country {
		case "ru":
			ruHits += row.Hits
			ruRev += row.Revenue
			if row.CostCur == "USD" {
				ruUSD += row.CostCents
			}
			if row.CostCur == "EUR" {
				ruEUR += row.CostCents
			}
		case "de":
			deHits += row.Hits
			deRev += row.Revenue
			if row.CostCur == "EUR" {
				deEUR += row.CostCents
			}
			if row.CostCur == "USD" {
				t.Fatalf("de took a usd click price: %+v", row)
			}
		default:
			t.Fatalf("unexpected country %+v", row)
		}
		if row.OfferID != "o1" {
			t.Fatalf("offer id %+v", row)
		}
	}
	if ruHits != 11 || deHits != 3 || ruRev != 6 || deRev != 9 || ruUSD != 100 || ruEUR != 20 || deEUR != 50 {
		t.Fatalf("clicks mixed: ru hits %d rev %v usd %d eur %d; de hits %d rev %v eur %d", ruHits, ruRev, ruUSD, ruEUR, deHits, deRev, deEUR)
	}
	var ruSales, deLeads int64
	var ruProfit, deProfit float64
	for _, row := range got.Status {
		switch row.Country {
		case "ru":
			if row.Status != "sale" {
				t.Fatalf("ru status %+v", row)
			}
			ruSales += row.N
			ruProfit += row.Profit
		case "de":
			if row.Status != "lead" {
				t.Fatalf("de status %+v", row)
			}
			deLeads += row.N
			deProfit += row.Profit
		default:
			t.Fatalf("unexpected status %+v", row)
		}
	}
	if ruSales != 2 || deLeads != 1 || ruProfit != 5 || deProfit != 9 {
		t.Fatalf("status mixed: ru sales %d profit %v; de leads %d profit %v", ruSales, ruProfit, deLeads, deProfit)
	}
}

// TestOfferCountrySQLAlias checks the folded country is not selected under the column name.
// ClickHouse substitutes a SELECT alias for a column of the same name, so AS country
// makes lower(country) in WHERE and GROUP BY read that alias and the query is rejected.
func TestOfferCountrySQLAlias(t *testing.T) {
	click := offerCountryClickSQL(true, true, true, 2, "")
	status := offerCountryStatusSQL(true, 2, " AND campaign_id = ?")
	for i, q := range []string{click, status} {
		if strings.Contains(q, "AS country") {
			t.Fatalf("query %d aliases the column name:\n%s", i, q)
		}
		if !strings.Contains(q, "lower(country) AS cc") || !strings.Contains(q, "GROUP BY lower(country)") || !strings.Contains(q, "lower(country) IN (?,?)") {
			t.Fatalf("query %d does not fold the column:\n%s", i, q)
		}
	}
}
