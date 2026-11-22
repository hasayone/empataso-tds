package store

import (
	"strings"
	"testing"
	"time"
)

func TestRejectedClickDropsEarlierSale(t *testing.T) {
	sql := voidedCIDSelect()
	if !strings.Contains(sql, "maxIf(ts, status = 'rejected') >= maxIf(ts, "+confirmedStatusSQL("status")+")") {
		t.Fatalf("a later sale must stay: %s", sql)
	}
	drop := voidedConfirmedSQL("cid", "status")
	if !strings.Contains(drop, confirmedStatusSQL("status")) || strings.Contains(drop, "NOT (status = 'rejected'") {
		t.Fatalf("rejected row must stay in the log totals path: %s", drop)
	}
}

func TestConfirmedSeriesSQL(t *testing.T) {
	for _, s := range []string{"FINAL", "'lead'", "'sale'", "conv", "revenue", "status = 'rejected'"} {
		if !strings.Contains(confirmedSeriesSQL, s) {
			t.Fatalf("query missing %s", s)
		}
	}
	if strings.Contains(confirmedSeriesSQL, "postbacks") {
		t.Fatal("confirmed series must not read postbacks")
	}
}

func TestFillSeriesKeepsConversionGap(t *testing.T) {
	from := time.Date(2026, 10, 14, 10, 30, 0, 0, time.UTC)
	to := time.Date(2026, 10, 14, 13, 30, 0, 0, time.UTC)
	hits := map[int64]seriesHit{
		time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC).Unix(): {total: 4, unique: 3, bots: 1},
		time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).Unix(): {conv: 2, revenue: 5.5},
	}
	pts := fillSeries(from, to, 3600, hits)
	if len(pts) != 4 {
		t.Fatalf("buckets = %d, want 4", len(pts))
	}
	if pts[0].Total != 4 || pts[0].Unique != 3 || pts[0].Bots != 1 {
		t.Fatalf("first bucket = %+v", pts[0])
	}
	if pts[1].Total != 0 || pts[1].Conv != 0 {
		t.Fatalf("quiet hour was dropped or shifted: %+v", pts[1])
	}
	if pts[2].Conv != 2 || pts[2].Revenue != 5.5 || pts[2].Total != 0 {
		t.Fatalf("conversion-only bucket = %+v", pts[2])
	}
}

func TestFillSeriesCopiesCost(t *testing.T) {
	from := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	to := from.Add(2 * time.Hour)
	hits := map[int64]seriesHit{
		from.Unix(): {total: 1, cost: map[string]int64{"USD": 150, "EUR": 20}},
	}
	pts := fillSeries(from, to, 3600, hits)
	if len(pts) != 2 {
		t.Fatalf("buckets = %d, want 2", len(pts))
	}
	if pts[0].Cost["USD"] != 150 || pts[0].Cost["EUR"] != 20 {
		t.Fatalf("cost = %#v", pts[0].Cost)
	}
	if pts[1].Cost != nil {
		t.Fatalf("quiet bucket cost = %#v", pts[1].Cost)
	}
}

func TestCapSeriesStep(t *testing.T) {
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour * 800)
	step := capSeriesStep(from, to, 3600)
	if step < 3600 {
		t.Fatalf("step %d is below 3600", step)
	}
	if int(to.Sub(from).Seconds())/step+2 > 400 {
		t.Fatalf("step %d still yields more than 400 buckets", step)
	}
	if capSeriesStep(from, to, 1) < 3600 {
		t.Fatal("a short step was kept")
	}
}
