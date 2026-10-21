package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClickFactsSQL(t *testing.T) {
	cases := []struct {
		click, cost bool
		groupBy     string
		selectCost  bool
		offer       bool
	}{
		{false, false, "GROUP BY group_id, stream_name", false, false},
		{true, false, "GROUP BY group_id, stream_name, offer_id, landing_id, network_id", false, true},
		{false, true, "GROUP BY group_id, stream_name, fact_cur", true, false},
		{true, true, "GROUP BY group_id, stream_name, offer_id, landing_id, network_id, fact_cur", true, true},
	}
	for _, tc := range cases {
		q := clickFactsSQL(tc.click, tc.cost, false, "")
		if !strings.Contains(q, tc.groupBy) {
			t.Errorf("click=%v cost=%v group: %s", tc.click, tc.cost, q)
		}
		hasCost := strings.Contains(q, "fact_cur") && strings.Contains(q, "sum(cost_cents)")
		if hasCost != tc.selectCost {
			t.Errorf("click=%v cost=%v cost cols=%v in %s", tc.click, tc.cost, hasCost, q)
		}
		if tc.selectCost && !strings.Contains(q, "count(), countIf(uniq = 1), countIf(uniq = 1), if(cost_cents > 0, cost_cur, '') AS fact_cur, sum(cost_cents)") {
			t.Errorf("click=%v cost=%v select order: %s", tc.click, tc.cost, q)
		}
		if strings.Contains(q, "offer_id") != tc.offer || strings.Contains(q, "landing_id") != tc.offer {
			t.Errorf("click=%v cost=%v offer: %s", tc.click, tc.cost, q)
		}
	}
	placed := clickFactsSQL(false, true, false, " AND country = ?")
	if !strings.Contains(placed, "ts < ? AND country = ? GROUP BY group_id, stream_name, fact_cur") {
		t.Fatalf("where placement: %s", placed)
	}
}

func TestPerformanceCostSQL(t *testing.T) {
	q := performanceCostSQL(" AND country = ?")
	for _, s := range []string{
		"group_id", "group_name", "stream_name", "cost_cur", "sum(cost_cents)",
		"cost_cents > 0", "AND country = ?",
		"GROUP BY group_id, group_name, stream_name, cost_cur",
	} {
		if !strings.Contains(q, s) {
			t.Fatalf("missing %s in %s", s, q)
		}
	}
	if strings.Contains(performanceCostSQL(""), "country") {
		t.Fatal("empty filter leaked into SQL")
	}
}

func TestCostSeriesSQL(t *testing.T) {
	q := costSeriesSQL(" AND country = ?")
	for _, s := range []string{
		"toStartOfInterval", "cost_cur", "sum(cost_cents)", "cost_cents > 0",
		"GROUP BY t, cost_cur", "AND country = ?",
	} {
		if !strings.Contains(q, s) {
			t.Fatalf("missing %s in %s", s, q)
		}
	}
	if strings.Contains(costSeriesSQL(""), "country") {
		t.Fatal("empty filter leaked into SQL")
	}
}

func TestCostJSON(t *testing.T) {
	fact, err := json.Marshal(ClickFact{CostCents: 150, CostCur: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fact), `"cost_cents":150`) || !strings.Contains(string(fact), `"cost_cur":"USD"`) {
		t.Fatalf("click fact: %s", fact)
	}
	perf, err := json.Marshal(PerfRow{Cost: map[string]int64{"USD": 150}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(perf), `"cost"`) {
		t.Fatalf("perf: %s", perf)
	}
	bare, err := json.Marshal(PerfRow{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), `"cost"`) {
		t.Fatalf("perf omitempty: %s", bare)
	}
	point, err := json.Marshal(TSPoint{Cost: map[string]int64{"USD": 150}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(point), `"cost"`) {
		t.Fatalf("point: %s", point)
	}
	quiet, err := json.Marshal(TSPoint{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(quiet), `"cost"`) {
		t.Fatalf("point omitempty: %s", quiet)
	}
}

type stubScan struct {
	vals []any
}

func (s stubScan) Scan(dest ...any) error {
	if len(dest) != len(s.vals) {
		return fmt.Errorf("scan %d dest, %d vals", len(dest), len(s.vals))
	}
	for i := range dest {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(s.vals[i]))
	}
	return nil
}

func TestScanClickFactColumns(t *testing.T) {
	full, err := scanClickFact(stubScan{vals: []any{"g", "s", "o", "l", "n", uint64(4), uint64(3), uint64(2), "USD", int64(150)}}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if full.GroupID != "g" || full.Stream != "s" || full.OfferID != "o" || full.LandingID != "l" || full.NetworkID != "n" || full.Hits != 4 || full.Uniq != 3 || full.UniqFirst != 2 || full.CostCur != "USD" || full.CostCents != 150 {
		t.Fatalf("full %+v", full)
	}
	priced, err := scanClickFact(stubScan{vals: []any{"g", "s", uint64(2), uint64(1), uint64(1), "EUR", int64(20)}}, false, true)
	if err != nil || priced.OfferID != "" || priced.CostCur != "EUR" || priced.CostCents != 20 || priced.Hits != 2 {
		t.Fatalf("priced %+v %v", priced, err)
	}
	click, err := scanClickFact(stubScan{vals: []any{"g", "s", "o", "l", "n", uint64(1), uint64(1), uint64(1)}}, true, false)
	if err != nil || click.CostCents != 0 || click.CostCur != "" || click.OfferID != "o" || click.LandingID != "l" || click.Uniq != 1 {
		t.Fatalf("click %+v %v", click, err)
	}
	plain, err := scanClickFact(stubScan{vals: []any{"g", "s", uint64(1), uint64(0), uint64(0)}}, false, false)
	if err != nil || plain.CostCents != 0 || plain.CostCur != "" || plain.Hits != 1 || plain.Uniq != 0 {
		t.Fatalf("plain %+v %v", plain, err)
	}
}

func TestPerformanceConvSQLGroupID(t *testing.T) {
	q, extra := performanceConvSQL(time.Unix(0, 0), time.Unix(10, 0), "", nil, true)
	if extra != nil {
		t.Fatalf("extra %#v", extra)
	}
	for _, s := range []string{
		"SELECT '' AS group_id, group_name, stream_name, profit, ts FROM postbacks",
		"SELECT group_id, group_name, stream_name, profit, ts FROM conversions FINAL",
		"SELECT group_id, group_name, stream_name, count() AS conv, sum(profit) AS profit",
		"GROUP BY group_id, group_name, stream_name",
	} {
		if !strings.Contains(q, s) {
			t.Fatalf("missing %s in %s", s, q)
		}
	}
	plain, _ := performanceConvSQL(time.Unix(0, 0), time.Unix(10, 0), "", nil, false)
	if strings.Count(plain, "'' AS group_id") != 2 {
		t.Fatalf("both branches: %s", plain)
	}
	if !strings.Contains(plain, "GROUP BY group_id, group_name, stream_name") {
		t.Fatalf("group: %s", plain)
	}
	filt, args := performanceConvSQL(time.Unix(1, 0), time.Unix(2, 0), " AND x = ?", []any{"z"}, true)
	if !strings.Contains(filt, "'' AS group_id, group_name, stream_name, profit, ts, cid FROM postbacks") {
		t.Fatalf("filtered postbacks: %s", filt)
	}
	if !strings.Contains(filt, "SELECT group_id, group_name, stream_name, profit, ts, cid FROM conversions FINAL") {
		t.Fatalf("filtered conversions: %s", filt)
	}
	if len(args) != 3 || args[2] != "z" {
		t.Fatalf("args %#v", args)
	}
}

func TestGroupStatusSQLAlias(t *testing.T) {
	q := groupStatusSQL(" AND group_id IN (?)")
	if strings.Contains(q, "AS group_id") {
		t.Fatalf("alias shadows the column: %s", q)
	}
	if !strings.Contains(q, "any(group_id) AS gid") || !strings.Contains(q, "e.gid") {
		t.Fatalf("gid missing: %s", q)
	}
	if !strings.Contains(q, "AND group_id IN (?)") {
		t.Fatalf("where missing: %s", q)
	}
}

func TestPerfIndexAt(t *testing.T) {
	var idx perfIndex
	var out []PerfRow
	first := idx.at(&out, "a", "old", "s")
	out[first].Hits = 3
	if idx.at(&out, "a", "renamed", "s") != first || out[first].Group != "old" || out[first].Hits != 3 {
		t.Fatalf("id lookup %+v", out)
	}
	if idx.at(&out, "", "old", "s") != first {
		t.Fatalf("empty id %+v", out)
	}
	second := idx.at(&out, "b", "old", "s")
	if second == first || len(out) != 2 || out[second].GroupID != "b" || out[first].GroupID != "a" {
		t.Fatalf("same name collapsed: %+v", out)
	}
}
