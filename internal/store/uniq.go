package store

import "fmt"

// uniqExpr is the SQL aggregate of unique clicks for a campaign or stream summary.
//
// step says whether events carry the step column. With it a unique counts only on
// the first step (step = 0): the /offer step logs its own uniqueness of the offer
// visit, and adding it would count one visitor twice in a campaign that has a
// landing. Without it the plain flag is counted. The expression has no placeholders.
func uniqExpr(step bool) string {
	if step {
		return "countIf(uniq = 1 AND step = 0)"
	}
	return "countIf(uniq = 1)"
}

// uniqCount is uniqExpr for this client: it filters on step once the wave is ready.
// conds are the conditions of the query and dims the fields it groups by. A
// selection or a grouping by offer or network keeps the plain flag: those ids
// sit on the /offer step, whose own uniques are what the row asks for. It is
// the one place that decides which uniques a summary counts. A nil client counts
// the plain flag.
func (c *CH) uniqCount(conds []Cond, dims ...string) string {
	return uniqExpr(c != nil && c.stepEvents && !selectsOfferStep(conds) && !groupsByOfferStep(dims))
}

// firstStepUniq is the first-step unique aggregate of this client: it filters on
// step once the wave is ready, whatever the query selects or groups by.
func (c *CH) firstStepUniq() string { return uniqExpr(c != nil && c.stepEvents) }

// groupsByOfferStep reports whether dims name an offer or network id column.
func groupsByOfferStep(dims []string) bool {
	for _, d := range dims {
		if d == "offer_id" || d == "network_id" {
			return true
		}
	}
	return false
}

// StepReady reports whether events carry the step column, so uniques of the
// /offer step can be told from uniques of the first step. A nil client is not
// ready. It does not query.
func (c *CH) StepReady() bool { return c != nil && c.stepEvents }

// selectsOfferStep reports whether conds pick events by offer or network id with
// eq or in. A scope condition is a role limit, not a selection, and an exclusion
// (ne, nin) still leaves the first step in the rows.
func selectsOfferStep(conds []Cond) bool {
	for _, cond := range conds {
		if cond.Scope || (cond.Op != "eq" && cond.Op != "in") {
			continue
		}
		if cond.Field == "offer_id" || cond.Field == "network_id" {
			return true
		}
	}
	return false
}

// summarySQL is the dashboard summary query over events.
// uniq is the unique-click aggregate (uniqExpr) and where is empty or a buildWhere
// fragment that starts with " AND ". Placeholders are from, to, then the where args.
func summarySQL(uniq, where string) string {
	return `SELECT count() AS total,
		        ` + uniq + ` AS uniques,
		        countIf(bot != '' AND bot != '-') AS bots
		 FROM events
		 WHERE ts >= ? AND ts < ?` + where
}

// seriesSQL is the chart query over events, one row per bucket.
// uniq is the unique-click aggregate and where is empty or a buildWhere fragment.
// Placeholders are the bucket seconds, from, to, then the where args.
func seriesSQL(uniq, where string) string {
	return `SELECT toStartOfInterval(ts, INTERVAL ? second) AS t,
		        count() AS total, ` + uniq + ` AS uniques,
		        countIf(bot!='' AND bot!='-') AS bots
		 FROM events WHERE ts >= ? AND ts < ?` + where + `
		 GROUP BY t ORDER BY t`
}

// performanceSQL is the traffic query of the performance table.
// uniq is the unique-click aggregate and where is empty or a buildWhere fragment.
// Placeholders are from, to, then the where args.
func performanceSQL(uniq, where string) string {
	return `SELECT group_id, group_name, stream_name,
		        count() AS hits, ` + uniq + ` AS uniques,
		        countIf(bot != '' AND bot != '-') AS bots
		 FROM events WHERE ts >= ? AND ts < ?` + where + `
		 GROUP BY group_id, group_name, stream_name
		 ORDER BY hits DESC LIMIT 2000`
}

// forwardedSQL is the query of visits that streams handed to another group.
// uniq is the unique-click aggregate and where is empty or a buildWhere fragment.
// Placeholders are from, to, then the where args.
func forwardedSQL(uniq, where string) string {
	return `SELECT via, count() AS hits, ` + uniq + ` AS uniques,
		        countIf(bot != '' AND bot != '-') AS bots
		 FROM events WHERE ts >= ? AND ts < ? AND via != ''` + where + `
		 GROUP BY via`
}

// bucketsSQL is the click count of one campaign for a manual spend window.
// clickCols groups also by offer and network. uniq is the unique-click aggregate
// and where comes from bucketWhere. Placeholders are group id, from, to.
func bucketsSQL(clickCols bool, uniq, where string) string {
	if clickCols {
		return fmt.Sprintf(`SELECT stream_name, offer_id, network_id, count(), %s
			FROM events WHERE %s
			GROUP BY stream_name, offer_id, network_id`, uniq, where)
	}
	return fmt.Sprintf(`SELECT stream_name, count(), %s FROM events WHERE %s GROUP BY stream_name`, uniq, where)
}
