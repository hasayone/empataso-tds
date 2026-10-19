package store

import (
	"errors"
	"strings"
)

// ScopeNone is a campaign_id value that matches no real id. An empty visibility
// scope uses it so queries return nothing instead of everything.
const ScopeNone = "\x00"

// Cond is one filter on a journal column.
// Field is the API name. Op is eq, ne, in or nin.
// Values are the accepted (eq/in) or rejected (ne/nin) values; an empty
// value is allowed, an empty list is not.
// Scope marks a server-side access restriction: it is not counted toward
// maxConds, Values is not capped by maxCondVal, and parseConds never sets it.
type Cond struct {
	Field  string
	Op     string
	Values []string
	Scope  bool
}

// ErrUnknownField means the name is not a field, or the field has no column
// on the table being queried.
var ErrUnknownField = errors.New("store: unknown field")

// ErrBadCond means the operator is unknown, the value list is empty, or a
// limit is exceeded (20 conditions, 200 values each).
var ErrBadCond = errors.New("store: bad condition")

// ErrFieldNotReady means the field exists but its columns are not on the
// table yet, so a breakdown or value list cannot run.
var ErrFieldNotReady = errors.New("store: field not ready")

// ErrBadSort means the sort field is unknown, forbidden, internal, not on the
// table, or its wave is not ready yet.
var ErrBadSort = errors.New("store: bad sort")

const (
	maxConds   = 20
	maxCondVal = 200
)

// ValidateConds checks conds against the static field list.
// table is "events" or "conversions". It does not look at readiness or at
// the database. ErrUnknownField means a name is missing on that table.
// ErrBadCond means the operator, an empty list, or a limit is wrong.
// A nil or empty conds is valid and returns nil.
func ValidateConds(table string, conds []Cond) error {
	if table != "events" && table != "conversions" {
		return ErrBadCond
	}
	nRegular := 0
	for _, cond := range conds {
		switch cond.Op {
		case "eq", "ne", "in", "nin":
		default:
			return ErrBadCond
		}
		if len(cond.Values) == 0 {
			return ErrBadCond
		}
		f, ok := fieldByName(cond.Field)
		if !ok {
			return ErrUnknownField
		}
		if col, _ := f.tableCol(table); col == "" {
			return ErrUnknownField
		}
		if cond.Scope {
			continue
		}
		nRegular++
		if len(cond.Values) > maxCondVal {
			return ErrBadCond
		}
	}
	if nRegular > maxConds {
		return ErrBadCond
	}
	return nil
}

// buildWhere turns conds into a fragment that can be appended to an existing
// WHERE. c supplies readiness. table is "events" or "conversions".
// sql is empty or starts with " AND ". args are the placeholder values, in
// order. skip is true when a condition names a wave that is not ready: the
// caller returns an empty result and does not run the query.
// ErrUnknownField and ErrBadCond come from ValidateConds. A ready check that
// fails is not an error.
func buildWhere(c *CH, table string, conds []Cond) (sql string, args []any, skip bool, err error) {
	if err = ValidateConds(table, conds); err != nil {
		return "", nil, false, err
	}
	if len(conds) == 0 {
		return "", nil, false, nil
	}
	var b strings.Builder
	for _, cond := range conds {
		f, _ := fieldByName(cond.Field)
		col, w := f.tableCol(table)
		if !c.ready(table, w) {
			return "", nil, true, nil
		}
		expr := columnExpr(f, col)
		vals := cond.Values
		if f.Lower {
			vals = append([]string(nil), cond.Values...)
			for i, v := range vals {
				vals[i] = strings.ToLower(strings.TrimSpace(v))
			}
		}
		if table == "conversions" && f.Name == "campaign_id" && (cond.Op == "eq" || cond.Op == "in") {
			evCol, _ := f.tableCol("events")
			b.WriteString(convCampaignSQL(expr, columnExpr(f, evCol), len(vals)))
			args = append(args, stringArgs(vals)...)
			args = append(args, stringArgs(vals)...)
			continue
		}
		b.WriteString(" AND ")
		b.WriteString(expr)
		if cond.Op == "eq" || cond.Op == "in" {
			b.WriteString(" IN (")
		} else {
			b.WriteString(" NOT IN (")
		}
		b.WriteString(placeholders(len(vals)))
		b.WriteByte(')')
		if cond.Op == "ne" || cond.Op == "nin" {
			b.WriteString(" AND ")
			b.WriteString(expr)
			b.WriteString(" != ''")
		}
		args = append(args, stringArgs(vals)...)
	}
	return b.String(), args, false, nil
}

// convCampaignSQL restricts conversions to the given campaigns, including
// rows imported without a campaign id.
// convExpr is the campaign column of the conversion row. eventsExpr is the
// campaign column of events. n is the number of campaign ids and is at least 1.
// A row with an empty campaign matches when its click cid appears in events
// under one of those campaigns. The caller supplies the ids twice, in that
// order. The fragment starts with " AND ". It does not fail.
func convCampaignSQL(convExpr, eventsExpr string, n int) string {
	list := placeholders(n)
	return " AND (" + convExpr + " IN (" + list + ") OR (" + convExpr + " = '' AND cid IN " +
		"(SELECT cid FROM events WHERE " + eventsExpr + " IN (" + list + ") AND cid != '')))"
}

// stringArgs converts a list of strings to placeholder arguments.
// vals is the list. The result has the same length and order.
func stringArgs(vals []string) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}

// stringType reports whether ClickHouse compares the type without toString.
// t is the Field.Type. LowCardinality(String) counts as a string.
func stringType(t string) bool {
	return t == "String" || t == "LowCardinality(String)"
}

// eventsCidInSQL restricts a query to clicks that appear in events for the period.
// eventsWhere is empty or a fragment from buildWhere on events that starts with " AND ".
// The caller supplies ts >= ? AND ts < ? twice, then the where args, in that order.
func eventsCidInSQL(eventsWhere string) string {
	return ` AND cid IN (SELECT cid FROM events WHERE ts >= ? AND ts < ? AND cid != ''` + eventsWhere + `)`
}

// placeholders returns n question marks separated by commas.
// n is at least 1; the caller has already rejected an empty list.
func placeholders(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
	}
	return b.String()
}
