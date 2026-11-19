package store

import (
	"cmp"
	"slices"
	"strings"
)

const (
	// PageDefaultRows is the page size of a paged table when the request names none.
	PageDefaultRows = 1000
	// PageMaxRows is the largest page one answer carries and the most groups a paged
	// period report reads from the visit side. The export asks for one page of this size.
	PageMaxRows = 100000
)

// PageQuery asks for one page of a table sorted on the server.
// Sort is a column key of the table; empty keeps the table's default order.
// Desc reverses the column order. Offset is the first row, counted from 0.
// Limit is the number of rows, 1..PageMaxRows.
type PageQuery struct {
	Sort   string
	Desc   bool
	Offset int
	Limit  int
}

// Dir is the direction of q as the API names it: "desc" or "asc".
func (q PageQuery) Dir() string {
	if q.Desc {
		return "desc"
	}
	return "asc"
}

// pageBounds clips the window of q to n rows.
// It returns the first and the past-the-end index; both are n when Offset is past the end.
func pageBounds(q PageQuery, n int) (lo, hi int) {
	lo = min(max(q.Offset, 0), n)
	hi = min(lo+max(q.Limit, 0), n)
	return lo, hi
}

// sortRows orders rows in place by one column and makes the order total.
// key compares two rows by the column, ascending. desc reverses that comparison only.
// last, when not nil, marks rows that follow every other row whatever the direction.
// tie compares two rows by their identity, ascending, and decides every remaining tie,
// so a page boundary falls on the same row on every request. It does not fail.
func sortRows[T any](rows []T, key func(a, b T) int, desc bool, last func(T) bool, tie func(a, b T) int) {
	slices.SortFunc(rows, func(a, b T) int {
		if last != nil {
			if la, lb := last(a), last(b); la != lb {
				if la {
					return 1
				}
				return -1
			}
		}
		c := key(a, b)
		if desc {
			c = -c
		}
		if c != 0 {
			return c
		}
		return tie(a, b)
	})
}

// cmpFold compares two labels ignoring letter case, then as they are.
// It does not fail.
func cmpFold(a, b string) int {
	if c := cmp.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return cmp.Compare(a, b)
}
