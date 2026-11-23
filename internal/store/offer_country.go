package store

import (
	"context"
	"fmt"
	"time"
)

// MaxOfferGeoCountries is the most country codes one offer split may name.
// The list is the countries stored on that offer, not every country in the warehouse.
const MaxOfferGeoCountries = 300

// OfferCountryFacts reads click and conversion facts of one offer, split by country.
// offerID is the offer. countries are the lowercase codes to keep; an empty list
// returns empty facts and runs no query. scope is the campaign scope from the
// caller (buildWhere on events); a nil scope reads every campaign the caller may see.
// Clicks are grouped by country and, when the cost columns exist, by currency, with
// the same counts, unique flag, click price and click revenue as the catalog facts.
// The country code is compared and returned in lower case: the engine writes "ru",
// and a click imported with "RU" is the same country. Status rows are the offer's
// conversions grouped by that code and folded status.
// They are not joined to campaign totals, so a country row matches the offer row's
// filter (this offer, and not the campaign-only status). A country that is not in
// countries is not read. Ready is false when the offer columns are not on the tables.
// ErrUnknownField and ErrBadCond come from scope. A query error is returned as-is.
func (c *CH) OfferCountryFacts(ctx context.Context, from, to time.Time, offerID string, countries []string, scope []Cond) (Facts, error) {
	out := Facts{Clicks: []ClickFact{}, Status: []StatusFact{}}
	if len(countries) > MaxOfferGeoCountries {
		return out, fmt.Errorf("%w: too many countries", ErrBadCond)
	}
	if len(countries) == 0 || offerID == "" || c == nil || !c.clickCols {
		return out, nil
	}
	out.Ready = true
	if c.conn == nil {
		return out, nil
	}
	where, args, skip, err := buildWhere(c, "events", scope)
	if err != nil || skip {
		if skip {
			out.Ready = false
		}
		return out, err
	}
	ctx = c.analyticsCtx(ctx, 0)
	clicks, err := c.offerCountryClicks(ctx, from, to, offerID, countries, where, args)
	if err != nil {
		return Facts{Clicks: []ClickFact{}, Status: []StatusFact{}}, err
	}
	status, err := c.offerCountryStatus(ctx, from, to, offerID, countries, where, args)
	if err != nil {
		return Facts{Clicks: []ClickFact{}, Status: []StatusFact{}}, err
	}
	out.Clicks, out.Status = clicks, status
	return out, nil
}

// offerCountryFilter is the predicate of one offer and its country list.
// n is the number of country placeholders, at least 1. scope is empty or a
// buildWhere fragment that starts with " AND ". The result starts with " AND ".
// Country is folded with lower, so the engine's lower-case code and the same
// code stored in upper case by an import select the same rows.
func offerCountryFilter(n int, scope string) string {
	return " AND offer_id = ? AND lower(country) IN (" + placeholders(n) + ")" + scope
}

// offerCountryArgs is offerID, then each country, then the scope placeholders.
func offerCountryArgs(offerID string, countries []string, scopeArgs []any) []any {
	args := make([]any, 0, 1+len(countries)+len(scopeArgs))
	args = append(args, offerID)
	for _, code := range countries {
		args = append(args, code)
	}
	return append(args, scopeArgs...)
}

// offerCountryClickSQL is the click facts of one offer grouped by country.
// cost adds the currency and the sum of click prices, one row per currency.
// step selects the first-step unique count in the third aggregate, as click facts do.
// rev adds sum(revenue). n is the size of the country list. scope is empty or
// a buildWhere fragment. Placeholders are from, to, offer id, the countries, then scope.
// The folded code is selected as cc, not country. ClickHouse substitutes a
// SELECT alias for a column of the same name, so AS country makes
// lower(country) in WHERE and GROUP BY read that alias and the query is
// rejected. lower(country) still matches "RU" and "ru" as one row.
// It does not fail.
func offerCountryClickSQL(cost, step, rev bool, n int, scope string) string {
	cols := "lower(country) AS cc, count(), countIf(uniq = 1), " + uniqExpr(step)
	group := "lower(country)"
	if cost {
		cols += ", if(cost_cents > 0, cost_cur, '') AS fact_cur, sum(cost_cents)"
		group += ", fact_cur"
	}
	if rev {
		cols += ", sum(revenue)"
	}
	return "SELECT " + cols + " FROM events WHERE ts >= ? AND ts < ?" + offerCountryFilter(n, scope) + " GROUP BY " + group
}

// offerCountryClicks reads the click rows of OfferCountryFacts.
// scope is the events fragment and scopeArgs its placeholders. A query or scan error is returned.
func (c *CH) offerCountryClicks(ctx context.Context, from, to time.Time, offerID string, countries []string, scope string, scopeArgs []any) ([]ClickFact, error) {
	q := offerCountryClickSQL(c.costCols, c.stepEvents, c.revCols, len(countries), scope)
	qargs := append([]any{from, to}, offerCountryArgs(offerID, countries, scopeArgs)...)
	rows, err := c.conn.Query(ctx, q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClickFact
	for rows.Next() {
		row, err := scanOfferCountryClick(rows, c.costCols, c.revCols)
		if err != nil {
			return nil, err
		}
		row.OfferID = offerID
		out = append(out, row)
	}
	if out == nil {
		out = []ClickFact{}
	}
	return out, rows.Err()
}

// scanOfferCountryClick reads one offerCountryClickSQL row.
// cost and rev select the same trailing columns as the query. A scan error is returned.
func scanOfferCountryClick(rows factRow, cost, rev bool) (ClickFact, error) {
	var row ClickFact
	var hits, uniq, first uint64
	dest := []any{&row.Country, &hits, &uniq, &first}
	if cost {
		dest = append(dest, &row.CostCur, &row.CostCents)
	}
	if rev {
		dest = append(dest, &row.Revenue)
	}
	if err := rows.Scan(dest...); err != nil {
		return ClickFact{}, err
	}
	row.Hits, row.Uniq, row.UniqFirst = int64(hits), int64(uniq), int64(first)
	return row, nil
}

// offerCountryStatusSQL is the conversion facts of one offer grouped by country.
// dep selects the deposit sums; a closed wave uses typed zeros. n is the size of
// the country list. scope is empty or a buildWhere fragment on events. When scope
// is set, conversions are limited to clicks of that offer and those countries
// inside the scope, so a campaign the caller cannot see does not leak. The
// scope fragment is not pasted onto conversions: campaign_id there is a later wave.
// Placeholders are from, to, the offer id and the countries. When scope is set,
// a second from, to, the offer id, the countries and the scope args follow.
// The country column is lower(country), the same fold as the click query,
// and the selected code is aliased cc so the column name is not shadowed.
// It does not fail.
func offerCountryStatusSQL(dep bool, n int, scope string) string {
	q := `SELECT lower(country) AS cc, offer_id, ` + foldedStatusSQL("status") + ` AS folded, count(), sum(profit), ` + sumDep(dep, "depsum") + `, ` + sumDep(dep, "dep2sum") + `
		FROM conversions FINAL
		WHERE ts >= ? AND ts < ?` + offerCountryFilter(n, "") + voidedConfirmedSQL("cid", "status")
	if scope != "" {
		q += eventsCidInSQL(offerCountryFilter(n, scope))
	}
	return q + ` GROUP BY lower(country), offer_id, folded`
}

// offerCountryStatus reads the conversion rows of OfferCountryFacts.
// scope is the events fragment and scopeArgs its placeholders. A query or scan error is returned.
func (c *CH) offerCountryStatus(ctx context.Context, from, to time.Time, offerID string, countries []string, scope string, scopeArgs []any) ([]StatusFact, error) {
	q := offerCountryStatusSQL(c.depsumConv, len(countries), scope)
	qargs := append([]any{from, to}, offerCountryArgs(offerID, countries, nil)...)
	if scope != "" {
		qargs = append(qargs, from, to)
		qargs = append(qargs, offerCountryArgs(offerID, countries, scopeArgs)...)
	}
	rows, err := c.conn.Query(ctx, q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusFact
	for rows.Next() {
		var row StatusFact
		var n uint64
		if err := rows.Scan(&row.Country, &row.OfferID, &row.Status, &n, &row.Profit, &row.DepSum, &row.Dep2Sum); err != nil {
			return nil, err
		}
		row.N = int64(n)
		out = append(out, row)
	}
	if out == nil {
		out = []StatusFact{}
	}
	return out, rows.Err()
}
