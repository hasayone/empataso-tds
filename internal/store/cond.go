package store

import (
	"errors"
	"fmt"
	"strings"
)

// ScopeNone is a campaign_id value that matches no real id. An empty visibility
// scope uses it so queries return nothing instead of everything.
const ScopeNone = "\x00"

// Cond is one filter on a journal column.
// Field is the API name. Op is one of CondOps.
// Values are the accepted (eq/in) or rejected (ne/nin) values; an empty
// value is allowed, an empty list is not. For contains and not_contains the
// values are substrings: a row matches contains when any of them occurs in
// the column, not_contains when none does; they need a text field and a value
// that is not blank.
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

// Substring operators. They apply only to fields with Text set.
const (
	OpContains    = "contains"
	OpNotContains = "not_contains"
)

// CondOps lists every filter operator the API accepts, in display order.
var CondOps = []string{"eq", "ne", "in", "nin", OpContains, OpNotContains}

// ValidOp reports whether op is one of CondOps.
// op is the operator from the request. It does not fail.
func ValidOp(op string) bool {
	for _, o := range CondOps {
		if o == op {
			return true
		}
	}
	return false
}

// substrOp reports whether op compares substrings rather than whole values.
func substrOp(op string) bool {
	return op == OpContains || op == OpNotContains
}

// ValidateConds checks conds against the static field list.
// table is "events" or "conversions". It does not look at readiness or at
// the database. ErrUnknownField means a name is missing on that table.
// ErrBadCond means the operator, an empty list, or a limit is wrong, or a
// substring operator names a field that is not text or carries a blank value.
// A nil or empty conds is valid and returns nil.
func ValidateConds(table string, conds []Cond) error {
	if table != "events" && table != "conversions" {
		return ErrBadCond
	}
	nRegular := 0
	for _, cond := range conds {
		if !ValidOp(cond.Op) {
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
		if substrOp(cond.Op) {
			if err := validateSubstr(f, cond.Values); err != nil {
				return err
			}
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

// validateSubstr checks a contains or not_contains condition.
// f is the field, vals the substrings. ErrBadCond, wrapped with the reason,
// means f is not a text field or a value is blank.
func validateSubstr(f Field, vals []string) error {
	if !f.Text {
		return fmt.Errorf("%w: %s is not a text field", ErrBadCond, f.Name)
	}
	for _, v := range vals {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%w: empty substring", ErrBadCond)
		}
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
	return buildWhereMode(c, table, conds, false)
}

// buildJournalWhere is buildWhere for the click and conversion journals.
// A click that one campaign forwarded to another is stored under the campaign
// that served it, with the campaign that received it in origin_*. For the
// fields campaign and campaign_id a condition that is not a Scope therefore
// matches either column, so the journal of the entry campaign shows its clicks
// and conversions. Scope conditions and every other field stay exact.
// Parameters and results are those of buildWhere.
func buildJournalWhere(c *CH, table string, conds []Cond) (sql string, args []any, skip bool, err error) {
	return buildWhereMode(c, table, conds, true)
}

// buildWhereMode is the body of buildWhere and buildJournalWhere.
// entry true widens the campaign conditions to the origin columns, see buildJournalWhere.
func buildWhereMode(c *CH, table string, conds []Cond, entry bool) (sql string, args []any, skip bool, err error) {
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
		if entry && !cond.Scope {
			if frag, copies, ok := entryCampaignSQL(c, table, f, expr, cond.Op, len(vals)); ok {
				b.WriteString(frag)
				for i := 0; i < copies; i++ {
					args = append(args, stringArgs(vals)...)
				}
				continue
			}
		}
		if table == "conversions" && f.Name == "campaign_id" && (cond.Op == "eq" || cond.Op == "in") {
			evCol, _ := f.tableCol("events")
			b.WriteString(convCampaignSQL(expr, columnExpr(f, evCol), len(vals)))
			args = append(args, stringArgs(vals)...)
			args = append(args, stringArgs(vals)...)
			continue
		}
		if substrOp(cond.Op) {
			b.WriteString(substrSQL(expr, cond.Op, len(vals)))
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

// entryOrigin names the origin field that pairs with a campaign field.
var entryOrigin = map[string]string{"campaign": "origin_campaign", "campaign_id": "origin_campaign_id"}

// entryCampaignSQL is the journal fragment of a campaign or campaign_id condition
// that also looks at the campaign the click entered through.
// c supplies readiness, table is events or conversions, f is the field, expr its
// column expression, op the operator and n the number of values, at least 1.
// eq and in match the serving or the entry campaign. ne and nin match neither
// and keep the non-empty requirement of the serving column. On conversions,
// campaign_id keeps the rule of convCampaignSQL for rows without a campaign id.
// copies says how many times the caller supplies the values, in the same order
// for every placeholder. ok is false when f is not a campaign field, the op is
// not one of the four, or the origin column is not ready on table; the caller
// then builds the ordinary fragment. The fragment starts with " AND ".
func entryCampaignSQL(c *CH, table string, f Field, expr, op string, n int) (sql string, copies int, ok bool) {
	name, paired := entryOrigin[f.Name]
	if !paired || (op != "eq" && op != "in" && op != "ne" && op != "nin") {
		return "", 0, false
	}
	of, found := fieldByName(name)
	if !found {
		return "", 0, false
	}
	ocol, ow := of.tableCol(table)
	if ocol == "" || !c.ready(table, ow) {
		return "", 0, false
	}
	oexpr := columnExpr(of, ocol)
	list := placeholders(n)
	if op == "ne" || op == "nin" {
		return " AND " + expr + " NOT IN (" + list + ") AND " + oexpr + " NOT IN (" + list + ") AND " + expr + " != ''", 2, true
	}
	if table != "conversions" || f.Name != "campaign_id" {
		return " AND (" + expr + " IN (" + list + ") OR " + oexpr + " IN (" + list + "))", 2, true
	}
	evCol, _ := f.tableCol("events")
	evExpr := columnExpr(f, evCol)
	evMatch, evCopies := evExpr+" IN ("+list+")", 1
	if col, w := of.tableCol("events"); col != "" && c.ready("events", w) {
		evMatch = "(" + evExpr + " IN (" + list + ") OR " + columnExpr(of, col) + " IN (" + list + "))"
		evCopies = 2
	}
	sql = " AND (" + expr + " IN (" + list + ") OR " + oexpr + " IN (" + list + ") OR (" + expr + " = '' AND cid IN " +
		"(SELECT cid FROM events WHERE " + evMatch + " AND cid != '')))"
	return sql, 2 + evCopies, true
}

// substrSQL is the fragment of a case-insensitive substring condition.
// expr is the column expression, op is contains or not_contains, n is the
// number of substrings and is at least 1. Each substring is one placeholder,
// so no value reaches the SQL text. contains matches when any substring
// occurs; not_contains when none does and the column is not empty, as ne does.
// The fragment starts with " AND ". It does not fail.
func substrSQL(expr, op string, n int) string {
	hit := "positionCaseInsensitiveUTF8(" + expr + ", ?)"
	parts := make([]string, n)
	if op == OpContains {
		for i := range parts {
			parts[i] = hit + " > 0"
		}
		return " AND (" + strings.Join(parts, " OR ") + ")"
	}
	for i := range parts {
		parts[i] = hit + " = 0"
	}
	return " AND " + strings.Join(parts, " AND ") + " AND " + expr + " != ''"
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
