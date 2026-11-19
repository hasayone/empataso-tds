package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/store"
)

// dayStats is a report source that also cuts by day and remembers the zone it was asked for.
type dayStats struct {
	pageStats
	tz    string
	calls int
}

func (d *dayStats) SliceAllDays(_ context.Context, _, _ time.Time, _ []store.Cond, tz string) ([]store.SliceRow, store.SliceTotals, bool, error) {
	d.tz = tz
	d.calls++
	return slices.Clone(d.rows), d.tot, false, nil
}

// TestReportPageByDay checks day=1 reaches the source with the zone (UTC by default), the rows keep
// their day, the day sorts the whole set, a bad zone is 400, and a page without day never asks for days.
func TestReportPageByDay(t *testing.T) {
	st := &dayStats{pageStats: pageStats{rows: []store.SliceRow{
		{Group: "a", Day: "2026-11-18", Hits: 5}, {Group: "a", Day: "2026-11-19", Hits: 3},
	}}}
	base, c := serverWithStats(t, st)

	var got reportPage
	if err := json.Unmarshal(getBody(t, c, base+"/api/report?limit=10&day=1&tz=Europe/Berlin&sort=day&dir=desc"), &got); err != nil {
		t.Fatal(err)
	}
	if st.tz != "Europe/Berlin" || len(got.Rows) != 2 || got.Rows[0].Day != "2026-11-19" || got.Sort != "day" {
		t.Fatalf("zone %q page %+v", st.tz, got)
	}
	if body := string(getBody(t, c, base+"/api/report?limit=10&day=1")); st.tz != "UTC" || !strings.Contains(body, `"day":"2026-11-18"`) {
		t.Fatalf("default zone %q: %s", st.tz, body)
	}

	if code, body := kwGet(t, c, base+"/api/report?limit=10&day=1&tz=Nowhere/Land"); code != http.StatusBadRequest || !strings.Contains(body, "bad tz") {
		t.Fatalf("bad zone → %d %s", code, body)
	}
	before := st.calls
	getBody(t, c, base+"/api/report?limit=10")
	if st.calls != before || st.allCalls == 0 {
		t.Fatalf("a page without day must read through SliceAll: days=%d all=%d", st.calls, st.allCalls)
	}
}
