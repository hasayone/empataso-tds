package store

import "fmt"

// sortForbidden lists journal fields that must not be used in ORDER BY.
var sortForbidden = map[string]bool{
	"useragent": true,
	"referer":   true,
	"out":       true,
	"page":      true,
}

// SortColumn returns the SQL expression for ordering table by the API field name.
// table is "events" or "conversions". name is the field name or the group alias.
// ready reports whether wave w is on table; a nil function treats every wave
// except base on events and conversions as not ready.
// The expression comes from the static field list only. ErrBadSort covers every
// other case. It does not touch the database.
func SortColumn(table, name string, ready func(w wave) bool) (string, error) {
	f, ok := fieldByName(name)
	if !ok || f.Internal {
		return "", ErrBadSort
	}
	if sortForbidden[f.Name] {
		return "", ErrBadSort
	}
	col, w := f.tableCol(table)
	if col == "" {
		return "", ErrBadSort
	}
	if ready == nil {
		ready = func(w wave) bool { return w == waveBase && (table == "events" || table == "conversions") }
	}
	if !ready(w) {
		return "", ErrBadSort
	}
	return columnExpr(f, col), nil
}

// SortColumnCH is SortColumn with readiness from c.
func (c *CH) SortColumnCH(table, name string) (string, error) {
	return SortColumn(table, name, func(w wave) bool { return c.ready(table, w) })
}

// SortColumnFor is SortColumn with readiness taken from FieldsInfo.
func SortColumnFor(info FieldsInfo, table, name string) (string, error) {
	return SortColumn(table, name, func(w wave) bool {
		if w == waveBase && (table == "events" || table == "conversions") {
			return true
		}
		if info.Ready == nil {
			return false
		}
		m := info.Ready[table]
		if m == nil {
			return false
		}
		return m[string(w)]
	})
}

// journalOrderBy builds ORDER BY for a journal query on table.
// sort is the API field name; empty means ts DESC with a cid tie-breaker.
// sort ts uses dir on ts only. Any other sort adds ts DESC and cid after the
// primary key. ready selects which waves SortColumn may use. The bool is true
// when the query runs under journalSortMaxExecution.
func journalOrderBy(table, sort string, desc bool, ready func(w wave) bool) (string, bool, error) {
	dir := "DESC"
	if !desc {
		dir = "ASC"
	}
	if sort == "" {
		return "ts DESC, cid", false, nil
	}
	if sort == "ts" {
		return fmt.Sprintf("ts %s, cid", dir), true, nil
	}
	expr, err := SortColumn(table, sort, ready)
	if err != nil {
		return "", false, err
	}
	return fmt.Sprintf("%s %s, ts DESC, cid", expr, dir), true, nil
}

// journalOrderByCH is journalOrderBy with readiness from c.
func (c *CH) journalOrderByCH(table, sort string, desc bool) (string, bool, error) {
	return journalOrderBy(table, sort, desc, func(w wave) bool { return c.ready(table, w) })
}
