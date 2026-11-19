package admin

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
)

// pageStats answers the paged report with fixed rows, totals and cut flag, and counts its calls.
type pageStats struct {
	fakeStats
	rows      []store.SliceRow
	tot       store.SliceTotals
	truncated bool

	mu       sync.Mutex
	allCalls int
}

func (p *pageStats) SliceAll(context.Context, time.Time, time.Time, []store.Cond) ([]store.SliceRow, store.SliceTotals, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allCalls++
	return slices.Clone(p.rows), p.tot, p.truncated, nil
}

func (p *pageStats) SliceMeta(context.Context, time.Time, time.Time, []store.Cond) ([]store.SliceRow, bool, error) {
	return slices.Clone(p.rows), false, nil
}

// plainSlice answers the report only through SliceFiltered, as a source without SliceAll.
type plainSlice struct {
	fakeStats
	rows []store.SliceRow
}

func (p *plainSlice) SliceFiltered(context.Context, time.Time, time.Time, []store.Cond) ([]store.SliceRow, error) {
	return slices.Clone(p.rows), nil
}

// reportPage is the decoded paged answer of GET /api/report.
type reportPage struct {
	Rows      []store.SliceRow  `json:"rows"`
	Total     int               `json:"total"`
	Offset    int               `json:"offset"`
	Limit     int               `json:"limit"`
	Sort      string            `json:"sort"`
	Dir       string            `json:"dir"`
	SortCur   string            `json:"sort_cur"`
	Totals    store.SliceTotals `json:"totals"`
	Truncated bool              `json:"truncated"`
}

// pageRows is a report set of five cells with a tie on hits.
func pageRows() []store.SliceRow {
	return []store.SliceRow{
		{Group: "b", Hits: 10, Conv: 1},
		{Group: "a", Hits: 10, Conv: 4},
		{Group: "c", Hits: 7, Conv: 7},
		{Group: "d", Hits: 2},
		{Group: "e", Hits: 1, Conv: 1},
	}
}

// TestReportPageSortsAndCounts checks a paged request sorts the whole set on the server, cuts the
// window, passes the totals of every row and the cut flag through, and leaves the old answers as they were.
func TestReportPageSortsAndCounts(t *testing.T) {
	tot := store.SliceTotals{Hits: 999, Unique: 500, Conv: 13}
	st := &pageStats{rows: pageRows(), tot: tot, truncated: true}
	base, c := serverWithStats(t, st)

	var got reportPage
	if err := json.Unmarshal(getBody(t, c, base+"/api/report?limit=2&offset=1&sort=cr&dir=desc"), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 5 || got.Offset != 1 || got.Limit != 2 || got.Sort != "cr" || got.Dir != "desc" || !got.Truncated {
		t.Fatalf("page head %+v", got)
	}
	if len(got.Rows) != 2 || got.Rows[0].Group != "e" || got.Rows[1].Group != "a" {
		t.Fatalf("rows %+v, want e then a: the ratio sorts the whole set", got.Rows)
	}
	if got.Totals.Hits != 999 || got.Totals.Unique != 500 || got.Totals.Conv != 13 {
		t.Fatalf("totals %+v must be the store's, not the page sum", got.Totals)
	}

	if err := json.Unmarshal(getBody(t, c, base+"/api/report?offset=0"), &got); err != nil {
		t.Fatal(err)
	}
	if got.Sort != "hits" || got.Dir != "desc" || got.Limit != store.PageDefaultRows || len(got.Rows) != 5 || got.Rows[0].Group != "a" {
		t.Fatalf("default page %+v", got)
	}

	var arr []store.SliceRow
	if err := json.Unmarshal(getBody(t, c, base+"/api/report"), &arr); err != nil {
		t.Fatalf("a request without page parameters must stay an array: %v", err)
	}
	meta := string(getBody(t, c, base+"/api/report?meta=1"))
	if strings.Contains(meta, `"total"`) || !strings.Contains(meta, `"truncated":false`) {
		t.Fatalf("meta=1 changed: %s", meta)
	}
	if st.allCalls != 2 {
		t.Fatalf("SliceAll ran %d times, want only for the paged requests", st.allCalls)
	}
}

// TestReportPageWithoutSliceAll checks a source without SliceAll still pages and sums its rows.
func TestReportPageWithoutSliceAll(t *testing.T) {
	base, c := serverWithStats(t, &plainSlice{rows: []store.SliceRow{{Group: "x", Hits: 2, UniqFirst: 1, Unique: 2}, {Group: "y", Hits: 3, UniqFirst: 3, Unique: 3}}})
	var got reportPage
	if err := json.Unmarshal(getBody(t, c, base+"/api/report?limit=1"), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || len(got.Rows) != 1 || got.Rows[0].Group != "y" || got.Totals.Hits != 5 || got.Totals.Unique != 4 {
		t.Fatalf("page %+v", got)
	}
}

// TestPageParamsRejectBadValues checks every bad limit, offset, sort and dir is 400 before a query.
func TestPageParamsRejectBadValues(t *testing.T) {
	st := &pageStats{rows: pageRows()}
	base, c := serverWithStats(t, st)
	for _, qs := range []string{"limit=0", "limit=100001", "limit=x", "offset=-1", "offset=1.5", "sort=nope", "sort=profit&dir=up", "sort=uniq_first"} {
		code, body := kwGet(t, c, base+"/api/report?"+qs)
		if code != http.StatusBadRequest {
			t.Errorf("%s → %d %s", qs, code, body)
		}
	}
	if code, _ := kwGet(t, c, base+"/api/report?limit=100000&sort=cost&dir=ASC"); code != http.StatusOK {
		t.Errorf("the largest page and an upper-case dir → %d", code)
	}
	if st.allCalls != 1 {
		t.Errorf("SliceAll ran %d times, want once for the one good request", st.allCalls)
	}
}

// TestPageSortHiddenField checks a role cannot sort the report or the keywords by a column it does not see,
// and that a visible column still pages with the hidden keys removed.
func TestPageSortHiddenField(t *testing.T) {
	rig := newRolesRigStats(t, "", &pageStats{rows: []store.SliceRow{{Group: "g", Hits: 1, Profit: 9}}})
	rig.hide(t, "profit")
	for _, path := range []string{"/api/report?sort=profit", "/api/report?sort=depsum", "/api/keywords/stats?sort=revenue&" + kwRange(time.Hour)} {
		if code, body := rig.get(t, rig.user, path); code != http.StatusBadRequest || !strings.Contains(body, "bad sort") {
			t.Errorf("%s → %d %s", path, code, body)
		}
	}
	code, body := rig.get(t, rig.user, "/api/report?sort=hits")
	if code != http.StatusOK || strings.Contains(body, `"profit"`) || !strings.Contains(body, `"total":1`) {
		t.Errorf("visible sort → %d %s", code, body)
	}
	if code, _ := rig.get(t, rig.admin, "/api/report?sort=profit"); code != http.StatusOK {
		t.Errorf("the administrator sees every column → %d", code)
	}
}

// TestReportPageSortsOffersByName checks the offer sort compares the stored offer names, not the ids.
func TestReportPageSortsOffersByName(t *testing.T) {
	rig := newRolesRigStats(t, "", &pageStats{rows: []store.SliceRow{{Group: "g", Offer: "1"}, {Group: "g", Offer: "2"}, {Group: "g", Offer: "3"}}})
	if err := rig.s.cfg.Offers.Save(context.Background(), []config.Offer{{ID: "1", Name: "Zeta"}, {ID: "2", Name: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	code, body := rig.get(t, rig.admin, "/api/report?sort=offer&dir=asc")
	var got reportPage
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("%d %s", code, body)
	}
	var order []string
	for _, r := range got.Rows {
		order = append(order, r.Offer)
	}
	if !slices.Equal(order, []string{"3", "2", "1"}) {
		t.Fatalf("offer order %v, want the id without a name, then alpha, then Zeta", order)
	}
}

// kwPageStub is kwStub that also pages the keyword table and keeps the page it was asked for.
type kwPageStub struct {
	kwStub
	page  store.KeywordPage
	asked store.PageQuery
	calls int
}

func (k *kwPageStub) KeywordStatsPage(_ context.Context, _, _ time.Time, _ []store.Cond, _ store.KeywordMode, q store.PageQuery) (store.KeywordPage, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.asked, k.calls = q, k.calls+1
	return k.page, nil
}

// TestKeywordStatsPageMode checks the keyword table pages on the server only when asked, with the
// parsed page, keeps the old answer otherwise, and that a source without paging answers an empty page.
func TestKeywordStatsPageMode(t *testing.T) {
	st := &kwPageStub{page: store.KeywordPage{Rows: []store.KeywordRow{{Keyword: "k", Hits: 2}}, Total: 40, Sort: "rej", Dir: "asc",
		Totals: store.KeywordTotals{Hits: 80}}}
	_, base, c := kwServer(t, st)
	code, body := kwGet(t, c, base+"/api/keywords/stats?"+kwRange(time.Hour)+"&sort=rej&dir=asc&limit=20&offset=20")
	if code != http.StatusOK || !strings.Contains(body, `"total":40`) || !strings.Contains(body, `"totals":{"hits":80`) {
		t.Fatalf("%d %s", code, body)
	}
	if want := (store.PageQuery{Sort: "rej", Offset: 20, Limit: 20}); st.asked != want || st.calls != 1 {
		t.Fatalf("asked %+v (%d calls), want %+v", st.asked, st.calls, want)
	}
	if code, body := kwGet(t, c, base+"/api/keywords/stats?"+kwRange(time.Hour)); code != http.StatusOK || strings.Contains(body, `"total"`) || st.calls != 1 {
		t.Fatalf("without page parameters: %d %s, %d page calls", code, body, st.calls)
	}
	if code, _ := kwGet(t, c, base+"/api/keywords/stats?"+kwRange(time.Hour)+"&sort=profit"); code != http.StatusBadRequest {
		t.Fatalf("bad sort → %d", code)
	}

	_, base, c = kwServer(t, &kwStub{})
	code, body = kwGet(t, c, base+"/api/keywords/stats?"+kwRange(time.Hour)+"&limit=10")
	if code != http.StatusOK || !strings.Contains(body, `"rows":[]`) || !strings.Contains(body, `"sort":"revenue"`) || !strings.Contains(body, `"total":0`) {
		t.Fatalf("source without paging: %d %s", code, body)
	}
}

// TestPageSortKeysMatchStore checks the sort keys the handlers accept are exactly the ones the store sorts by.
func TestPageSortKeysMatchStore(t *testing.T) {
	if got := slices.Sorted(maps.Keys(reportSortKeys)); !slices.Equal(got, slices.Sorted(slices.Values(store.SliceSortKeys))) {
		t.Errorf("report keys %v vs store %v", got, store.SliceSortKeys)
	}
	if got := slices.Sorted(maps.Keys(keywordSortKeys)); !slices.Equal(got, slices.Sorted(slices.Values(store.KeywordSortKeys))) {
		t.Errorf("keyword keys %v vs store %v", got, store.KeywordSortKeys)
	}
}
