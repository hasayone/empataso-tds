package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/egerkuzma/kuztds/internal/store"
)

// maxValueLimit is the largest /api/log-values page.
const maxValueLimit = 500

// parseConds reads repeated f=field:op:value parameters.
// q is the query. Identical field and op pairs become one condition, in the
// order of their first appearance. eq with several values becomes in.
// ne with several values becomes nin. An empty value is kept.
// It returns an error when a value has fewer than three colon-separated parts
// or the operator is not eq, ne, in or nin. It does not check field names.
func parseConds(q url.Values) ([]store.Cond, error) {
	if len(q["f"]) == 0 {
		return nil, nil
	}
	type pair struct{ field, op string }
	var order []pair
	grouped := map[pair][]string{}
	for _, v := range q["f"] {
		parts := strings.SplitN(v, ":", 3)
		if len(parts) < 3 {
			return nil, fmt.Errorf("expected field:op:value")
		}
		field, op, value := parts[0], parts[1], parts[2]
		switch op {
		case "eq", "ne", "in", "nin":
		default:
			return nil, fmt.Errorf("operator %s", op)
		}
		k := pair{field, op}
		if _, ok := grouped[k]; !ok {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], value)
	}
	out := make([]store.Cond, 0, len(order))
	for _, k := range order {
		op := k.op
		vals := grouped[k]
		if op == "eq" && len(vals) > 1 {
			op = "in"
		}
		if op == "ne" && len(vals) > 1 {
			op = "nin"
		}
		out = append(out, store.Cond{Field: k.field, Op: op, Values: vals})
	}
	return out, nil
}

// queryConds parses f= and checks the conditions against one table.
// q is the query. table is events or conversions.
// It returns the conditions. A parse error or a store validation error is returned as-is.
func queryConds(q url.Values, table string) ([]store.Cond, error) {
	conds, err := parseConds(q)
	if err != nil {
		return nil, err
	}
	if err := store.ValidateConds(table, conds); err != nil {
		return nil, err
	}
	return conds, nil
}

// writeBadFilter writes 400 with a bad filter prefix.
// w is the response. err is the parse or validation error. It does not return an error.
func writeBadFilter(w http.ResponseWriter, err error) {
	writeErr(w, http.StatusBadRequest, "bad filter: "+err.Error())
}

// handleLogFields writes the journal field list and wave readiness.
// When stats are not configured the list is the static catalog and ready is empty, still 200.
// It does not fail.
func (s *Server) handleLogFields(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeJSON(w, http.StatusOK, store.FieldsInfo{Fields: store.StaticFields(), Ready: map[string]map[string]bool{}})
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Stats.Fields())
}

// handleLogValues writes the top values of one dimension.
// r supplies table, field, from, to and limit. limit above 500 becomes 500.
// An unknown table is 400. ErrBadCond and ErrUnknownField are 400.
// ErrFieldNotReady is 200 with an empty list. Any other error is 500.
// Missing stats is 503.
func (s *Server) handleLogValues(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	q := r.URL.Query()
	table := q.Get("table")
	if table != "events" && table != "conversions" {
		writeErr(w, http.StatusBadRequest, "bad filter: table")
		return
	}
	field := q.Get("field")
	if hiddenFilterField(s.viewerFrom(r.Context()), field) {
		writeErr(w, http.StatusBadRequest, "bad filter: hidden field "+field)
		return
	}
	conds, ok := s.condsFrom(w, r, table)
	if !ok {
		return
	}
	from, to := parseRange(r)
	limit := clampValueLimit(atoiDef(q.Get("limit"), maxValueLimit))
	rows, err := s.cfg.Stats.Values(r.Context(), store.ValuesQuery{
		Table: table, Field: field, From: from, To: to, Limit: limit, Conds: conds,
	})
	if err != nil {
		if errors.Is(err, store.ErrFieldNotReady) {
			writeJSON(w, http.StatusOK, []store.KV{})
			return
		}
		if errors.Is(err, store.ErrBadCond) || errors.Is(err, store.ErrUnknownField) {
			writeBadFilter(w, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, "logs error")
		return
	}
	if rows == nil {
		rows = []store.KV{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// clampValueLimit bounds a value-list page size.
// n <= 0 or n above 500 becomes 500. It does not fail.
func clampValueLimit(n int) int {
	if n <= 0 || n > maxValueLimit {
		return maxValueLimit
	}
	return n
}
