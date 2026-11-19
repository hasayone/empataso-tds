package store

import (
	"cmp"
	"maps"
	"slices"
)

// SliceSortKeys are the report columns a page can be sorted by, in the order of the table.
var SliceSortKeys = []string{"group", "stream", "country", "offer", "source", "hits", "uniq", "bots", "conv", "cr", "profit", "depsum", "cost"}

// SliceDefaultSort is the report order when the request names none: the most visits first.
const SliceDefaultSort = "hits"

// SlicePage is one page of the period report.
// Rows are the page, Total the number of rows in the whole sorted set and Totals its sums.
// Sort and Dir are the order applied. SortCur is the currency the cost column sorts by;
// it is set only for that sort. Truncated is true when more than PageMaxRows visit groups
// exist and the set stops there; the totals still count every visit.
type SlicePage struct {
	Rows      []SliceRow  `json:"rows"`
	Total     int         `json:"total"`
	Offset    int         `json:"offset"`
	Limit     int         `json:"limit"`
	Sort      string      `json:"sort"`
	Dir       string      `json:"dir"`
	SortCur   string      `json:"sort_cur,omitempty"`
	Totals    SliceTotals `json:"totals"`
	Truncated bool        `json:"truncated"`
}

// SliceLabel names an entity id for sorting. field is "offer" or "source"; the result is
// the name to compare, or the id itself when it has none.
type SliceLabel func(field, id string) string

// SumSlice adds rows up into report totals, the same sums as SliceAll returns when the
// visit groups are not cut: Unique sums UniqFirst, the summable uniques. It does not fail.
func SumSlice(rows []SliceRow) SliceTotals {
	var t SliceTotals
	for _, r := range rows {
		t.Hits += r.Hits
		t.Bots += r.Bots
		t.Conv += r.Conv
		t.Profit += r.Profit
		t.DepSum += r.DepSum
		t.Unique += r.UniqFirst
		for cur, cents := range r.Cost {
			if t.Cost == nil {
				t.Cost = map[string]int64{}
			}
			t.Cost[cur] += cents
		}
	}
	return t
}

// PageSlice sorts every report row and cuts one page.
// rows is the whole set and is reordered in place. tot and truncated come from SliceAll.
// q.Sort is one of SliceSortKeys or empty for SliceDefaultSort; the caller has checked it.
// Text columns compare case-insensitively; offer and source compare by label when it is not nil.
// The cost column sorts by the currency with the largest total cost, and rows without cost
// in it follow every other row in both directions. Every tie falls back to the cell identity
// (campaign, stream, country, offer, source), so the order is the same on every request.
// It returns the page with the total row count. It does not fail.
func PageSlice(rows []SliceRow, tot SliceTotals, truncated bool, q PageQuery, label SliceLabel) SlicePage {
	if q.Sort == "" {
		q.Sort, q.Desc = SliceDefaultSort, true
	}
	if label == nil {
		label = func(_, id string) string { return id }
	}
	out := SlicePage{Total: len(rows), Offset: q.Offset, Limit: q.Limit, Sort: q.Sort, Dir: q.Dir(), Totals: tot, Truncated: truncated}
	var last func(SliceRow) bool
	if q.Sort == "cost" {
		out.SortCur = topCurrency(tot.Cost)
		last = func(r SliceRow) bool { return r.Cost[out.SortCur] <= 0 }
	}
	sortRows(rows, sliceCompare(q.Sort, out.SortCur, label), q.Desc, last, sliceIdentity)
	lo, hi := pageBounds(q, len(rows))
	out.Rows = slices.Clone(rows[lo:hi])
	if out.Rows == nil {
		out.Rows = []SliceRow{}
	}
	return out
}

// sliceCompare is the ascending comparison of one report column.
// key is a SliceSortKeys entry, cur the cost currency and label the entity namer.
// An unknown key compares every row equal, so the identity decides. It does not fail.
func sliceCompare(key, cur string, label SliceLabel) func(a, b SliceRow) int {
	num := func(f func(SliceRow) float64) func(a, b SliceRow) int {
		return func(a, b SliceRow) int { return cmp.Compare(f(a), f(b)) }
	}
	text := func(f func(SliceRow) string) func(a, b SliceRow) int {
		return func(a, b SliceRow) int { return cmpFold(f(a), f(b)) }
	}
	switch key {
	case "group":
		return text(func(r SliceRow) string { return r.Group })
	case "stream":
		return text(func(r SliceRow) string { return r.Stream })
	case "country":
		return text(func(r SliceRow) string { return r.Country })
	case "offer":
		return text(func(r SliceRow) string { return label("offer", r.Offer) })
	case "source":
		return text(func(r SliceRow) string { return label("source", r.Source) })
	case "hits":
		return num(func(r SliceRow) float64 { return float64(r.Hits) })
	case "uniq":
		return num(func(r SliceRow) float64 { return float64(r.Unique) })
	case "bots":
		return num(func(r SliceRow) float64 { return float64(r.Bots) })
	case "conv":
		return num(func(r SliceRow) float64 { return float64(r.Conv) })
	case "cr":
		return num(func(r SliceRow) float64 { return float64(r.Conv) / float64(max(r.Hits, 1)) })
	case "profit":
		return num(func(r SliceRow) float64 { return r.Profit })
	case "depsum":
		return num(func(r SliceRow) float64 { return r.DepSum })
	case "cost":
		return num(func(r SliceRow) float64 { return float64(r.Cost[cur]) })
	}
	return func(SliceRow, SliceRow) int { return 0 }
}

// sliceIdentity orders two report rows by their cell: campaign, stream, country, offer, source.
// Two rows of one answer never share a cell. It does not fail.
func sliceIdentity(a, b SliceRow) int {
	return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Stream, b.Stream), cmp.Compare(a.Country, b.Country),
		cmp.Compare(a.Offer, b.Offer), cmp.Compare(a.Source, b.Source))
}

// topCurrency is the code with the largest amount in by; the smaller code wins a tie and
// an empty map gives the empty code, the tracker currency. It does not fail.
func topCurrency(by map[string]int64) string {
	best, most := "", int64(0)
	for _, code := range slices.Sorted(maps.Keys(by)) {
		if by[code] > most {
			best, most = code, by[code]
		}
	}
	return best
}
