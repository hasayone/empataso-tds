package store

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // zone names must resolve in an image that ships no zoneinfo

	"github.com/ClickHouse/clickhouse-go/v2"
)

const (
	// KeywordStatsMaxSpan is the longest period the keyword statistics accept.
	KeywordStatsMaxSpan = 92 * 24 * time.Hour
	// KeywordStatsLimit is the most keyword rows one answer keeps. The query asks for one more,
	// so a full answer tells a truncated table from one that fits.
	KeywordStatsLimit = SliceLimit
	// KeywordTrendMaxTop is the most keywords one trend answer carries.
	KeywordTrendMaxTop = 25
	// keywordMaxQuerySize is max_query_size of a keyword query, in bytes. The scope of a caller
	// that sees thousands of campaigns is a campaign_id list the SQL repeats in every half of
	// the union, which passes the 256 KiB server default and fails the parse.
	keywordMaxQuerySize = 8 << 20

	// keywordHumans keeps only clicks of people: bots may send a random keyword on every click.
	keywordHumans = ` AND (bot = '' OR bot = '-')`
	// keywordOrder ranks keywords by payout, then clicks; the keyword itself breaks ties so
	// the trend picks the same top rows as the table.
	keywordOrder = `ORDER BY sum(profit_s) DESC, sum(hits) DESC, k ASC`
	// keywordKey is the grouping key: the keyword with invalid UTF-8 replaced, so two raw
	// strings that differ only in broken bytes are one row and the key is always valid text.
	keywordKey = `toValidUTF8(keyword)`
	// keywordDayLayout is the date format of a trend day.
	keywordDayLayout = "2006-01-02"
)

// KeywordMode selects attribution for keyword statistics.
// The empty value keeps last-click attribution; KeywordModeOrigin uses the first click.
type KeywordMode string

const (
	// KeywordModeLast keeps the keyword and campaign of the last click.
	KeywordModeLast KeywordMode = ""
	// KeywordModeOrigin attributes the keyword and campaign to the first click.
	KeywordModeOrigin KeywordMode = "origin"
)

// KeywordRow is one keyword of the statistics table.
// Hits and Unique count people only. Conv counts conversions of every status
// by the date of the conversion; Leads, Sales and Rejected split it by status.
// DepSum is the sum of deposits and is not the payout in Profit.
type KeywordRow struct {
	Keyword  string  `json:"keyword"`
	Hits     int64   `json:"hits"`
	Unique   int64   `json:"unique"`
	Conv     int64   `json:"conv"`
	Leads    int64   `json:"leads"`
	Sales    int64   `json:"sales"`
	Rejected int64   `json:"rejected"`
	Profit   float64 `json:"profit"`
	DepSum   float64 `json:"depsum"`
}

// KeywordStats is the keyword table of a period.
// Truncated is true when more than KeywordStatsLimit keywords exist and only the
// first ones by payout are kept. ConvReady is false when the conversion columns
// are not available for this query: the money part is then absent, not zero.
type KeywordStats struct {
	Rows      []KeywordRow `json:"rows"`
	Truncated bool         `json:"truncated"`
	ConvReady bool         `json:"conv_ready"`
}

// KeywordSeries is the daily course of one keyword; every array has one cell per day of
// KeywordTrend.Days. Conv and Profit are nil when the conversion columns are not available.
type KeywordSeries struct {
	Keyword string    `json:"keyword"`
	Hits    []int64   `json:"hits"`
	Conv    []int64   `json:"conv,omitempty"`
	Profit  []float64 `json:"profit,omitempty"`
}

// KeywordTrend is the daily course of the top keywords of a period.
// Days lists every calendar day of the period in the requested zone, gaps included.
// Series follow the order of the statistics table.
type KeywordTrend struct {
	Days      []string        `json:"days"`
	Series    []KeywordSeries `json:"series"`
	ConvReady bool            `json:"conv_ready"`
}

// KeywordTrendParams controls the trend size, time zone and attribution.
type KeywordTrendParams struct {
	Top  int
	TZ   string
	Mode KeywordMode
}

// zoneNameRe is the shape of an IANA zone name; it keeps odd names away from the SQL arguments.
var zoneNameRe = regexp.MustCompile(`^[A-Za-z0-9_+\-/]{1,64}$`)

// KeywordZone resolves a time zone name of a trend request.
// name is an IANA name; empty means UTC. "Local" is refused: ClickHouse does not know it.
// The error wraps ErrBadCond.
func KeywordZone(name string) (*time.Location, error) {
	if name == "" {
		name = "UTC"
	}
	if name == "Local" || !zoneNameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: time zone %q", ErrBadCond, name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: time zone %q", ErrBadCond, name)
	}
	return loc, nil
}

// KeywordRangeOK reports whether the period is one the keyword statistics accept:
// from is before to and the span is at most KeywordStatsMaxSpan.
func KeywordRangeOK(from, to time.Time) bool {
	return from.Before(to) && to.Sub(from) <= KeywordStatsMaxSpan
}

// keywordPlan is the filter part shared by the table and the trend.
// ev and cv are the buildWhere fragments of the visit and conversion sides with their
// arguments; uniq is the unique-click aggregate; convReady says the conversion side is read.
type keywordPlan struct {
	ev     string
	evArgs []any
	cv     string
	cvArgs []any
	uniq   string
	// evKey is the events grouping expression.
	evKey string
	// cvKey is the conversions grouping expression.
	cvKey string
	// evRoot restricts origin attribution to the root click's first step.
	evRoot    string
	convReady bool
}

// keywordOriginFilterFields are visitor properties stable across one redirect chain.
// Campaign, keyword and stream have explicit origin semantics; country and the core
// user-agent dimensions describe the same visitor on every step.
var keywordOriginFilterFields = map[string]bool{
	"campaign_id": true,
	"keyword":     true,
	"stream":      true,
	"country":     true,
	"device":      true,
	"os":          true,
	"browser":     true,
}

// keywordModeConds maps user campaign filters to the origin campaign and keeps access scope
// on both the current and origin campaigns. Scope conditions bypass the public filter allowlist.
// Imported conversions without a campaign do not use convCampaignSQL for a user origin filter.
func keywordModeConds(conds []Cond, mode KeywordMode) ([]Cond, error) {
	if mode != KeywordModeOrigin {
		return conds, nil
	}
	out := make([]Cond, 0, len(conds)+1)
	for _, cond := range conds {
		if _, ok := fieldByName(cond.Field); !ok {
			return nil, ErrUnknownField
		}
		if !cond.Scope && !keywordOriginFilterFields[cond.Field] {
			return nil, fmt.Errorf("%w: field %s is unavailable in origin mode", ErrBadCond, cond.Field)
		}
		if cond.Field != "campaign_id" {
			out = append(out, cond)
			continue
		}
		if cond.Scope {
			out = append(out, cond)
		}
		cond.Field = "origin_campaign_id"
		out = append(out, cond)
	}
	return out, nil
}

// keywordExprCond builds one validated keyword or stream condition over expr.
// cond supplies the operator and values. The result starts with AND and does not fail.
func keywordExprCond(cond Cond, expr string) (string, []any) {
	if substrOp(cond.Op) {
		return substrSQL(expr, cond.Op, len(cond.Values)), stringArgs(cond.Values)
	}
	op := " IN ("
	if cond.Op == "ne" || cond.Op == "nin" {
		op = " NOT IN ("
	}
	sql := " AND " + expr + op + placeholders(len(cond.Values)) + ")"
	if cond.Op == "ne" || cond.Op == "nin" {
		sql += " AND " + expr + " != ''"
	}
	return sql, stringArgs(cond.Values)
}

// keywordModeWhere builds the events filter for mode.
// It returns buildWhere's fragment, arguments, skip marker and errors.
func keywordModeWhere(c *CH, conds []Cond, mode KeywordMode) (string, []any, bool, error) {
	mapped, err := keywordModeConds(conds, mode)
	if err != nil {
		return "", nil, false, err
	}
	return buildWhere(c, "events", mapped)
}

// keywordModeConvWhere builds conversion filters and reports whether all fields are available.
// It delegates validation and readiness to convWhere, then replaces only keyword and stream
// condition expressions with their explicit origin expressions.
func keywordModeConvWhere(c *CH, conds []Cond, mode KeywordMode, key string) (string, []any, bool, error) {
	mapped, err := keywordModeConds(conds, mode)
	if err != nil {
		return "", nil, false, err
	}
	sql, args, ok, err := convWhere(c, mapped)
	if err != nil || !ok || mode != KeywordModeOrigin {
		return sql, args, ok, err
	}
	var b strings.Builder
	args = nil
	for _, cond := range mapped {
		expr := ""
		switch cond.Field {
		case "keyword":
			expr = key
		case "stream":
			expr = `if(root_cid = '', stream_name, origin_stream_name)`
		}
		if expr != "" {
			part, partArgs := keywordExprCond(cond, expr)
			b.WriteString(part)
			args = append(args, partArgs...)
			continue
		}
		part, partArgs, skip, buildErr := buildWhere(c, "conversions", []Cond{cond})
		if buildErr != nil {
			return "", nil, false, buildErr
		}
		if skip {
			return "", nil, false, nil
		}
		b.WriteString(part)
		args = append(args, partArgs...)
	}
	return b.String(), args, true, nil
}

// planKeywords builds the filter and grouping plan for conds and mode.
// skip is true when a condition names a wave that is not ready on events: the caller
// answers empty without a query. The conversion side is read only when conversions
// carry the ext wave (the keyword column) and conds fit there. The error is ErrUnknownField
// or ErrBadCond.
func (c *CH) planKeywords(conds []Cond, mode KeywordMode) (p keywordPlan, skip bool, err error) {
	p.evKey, p.cvKey = keywordKey, keywordKey
	if mode == KeywordModeOrigin {
		if _, err := keywordModeConds(conds, mode); err != nil {
			return keywordPlan{}, false, err
		}
		if !c.ready("events", waveRoot) || !c.ready("events", waveStep) || !c.ready("events", waveOrigin) ||
			!c.ready("conversions", waveRoot) || !c.ready("conversions", waveOrigin) {
			return p, true, nil
		}
		p.cvKey = `toValidUTF8(if(root_cid = '', keyword, origin_keyword))`
		p.evRoot = ` AND root_cid = '' AND step = 0`
	}
	p.ev, p.evArgs, skip, err = keywordModeWhere(c, conds, mode)
	if err != nil || skip {
		return keywordPlan{}, skip, err
	}
	p.uniq = c.uniqCount(conds, "keyword")
	if !c.extConv {
		return p, false, nil
	}
	p.cv, p.cvArgs, p.convReady, err = keywordModeConvWhere(c, conds, mode, p.cvKey)
	if err != nil {
		return keywordPlan{}, false, err
	}
	return p, false, nil
}

// keywordStatsEvents is the visit half of the table: one row per keyword with its
// clicks and uniques, and typed zeros where the conversion half has numbers.
// Placeholders are from, to, then the visit filter args. The string does not fail.
func keywordStatsEvents(p keywordPlan) string {
	return `SELECT ` + p.evKey + ` AS k, count() AS hits, ` + p.uniq + ` AS uniq_n, toUInt64(0) AS conv,
		toUInt64(0) AS leads, toUInt64(0) AS sales, toUInt64(0) AS rejected,
		toFloat64(0) AS profit_s, toFloat64(0) AS dep_s
		FROM events WHERE ts >= ? AND ts < ?` + keywordHumans + p.evRoot + p.ev + `
		GROUP BY k`
}

// keywordConvUnion is the source of the conversion half: the old postbacks and conversions
// with every wave of this client, keyword included. The caller has checked extConv.
func (c *CH) keywordConvUnion() string {
	return conversionUnion(c.clickCols, true, c.modelConv, conversionLateWaves{
		origin: c.originConv.Load(),
		root:   c.ready("conversions", waveRoot),
	}, c.subConv, c.depsumConv)
}

// keywordStatsConv is the conversion half of the table: every status counts, confirmed
// leads and sales of a click that was rejected later do not, and the date is the conversion's.
// Placeholders are from, to, then the conversion filter args. The string does not fail.
func (c *CH) keywordStatsConv(p keywordPlan) string {
	return `SELECT ` + p.cvKey + ` AS k, toUInt64(0), toUInt64(0), count(), countIf(status = 'lead'),
		countIf` + saleStatusSQL("status") + `, countIf(status = 'rejected'), sum(profit), ` + sumDep(c.depsumConv, "depsum") + `
		FROM (` + c.keywordConvUnion() + `) WHERE ts >= ? AND ts < ?` +
		voidedConfirmedSQL("cid", "status") + p.cv + `
		GROUP BY k`
}

// keywordUnion is the table source: the visit half, plus the conversion half when convReady.
// It returns the SQL and its arguments in placeholder order. The SQL does not fail.
func (c *CH) keywordUnion(p keywordPlan, from, to time.Time) (string, []any) {
	q := keywordStatsEvents(p)
	args := append([]any{from, to}, p.evArgs...)
	if p.convReady {
		q += "\n\t\tUNION ALL\n\t\t" + c.keywordStatsConv(p)
		args = append(args, from, to)
		args = append(args, p.cvArgs...)
	}
	return q, args
}

// keywordRankedSQL ranks the union by keywordOrder and keeps limit keywords.
// sel is the select list over the grouped union. It is the one place that fixes the
// order of the table, and the trend selects its keywords through it.
func keywordRankedSQL(sel, union string, limit int) string {
	return keywordOrderedSQL(sel, union, keywordOrder, limit, 0)
}

// keywordOrderedSQL groups union by keyword, orders it by order and keeps limit rows from offset.
// sel is the select list over the grouped union and order a full ORDER BY clause.
// A zero offset is left out of the SQL. The SQL does not fail.
func keywordOrderedSQL(sel, union, order string, limit, offset int) string {
	q := fmt.Sprintf(`SELECT %s FROM (
		%s
		) GROUP BY k %s LIMIT %d`, sel, union, order, limit)
	if offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", offset)
	}
	return q
}

// keywordRows runs a keyword table query and scans at most limit rows of it.
// It returns the rows, never nil, cut true when the query had a row beyond limit,
// and any query or scan error.
func (c *CH) keywordRows(ctx context.Context, q string, args []any, limit int) ([]KeywordRow, bool, error) {
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []KeywordRow{}
	for rows.Next() {
		if len(out) >= limit {
			return out, true, nil
		}
		var r KeywordRow
		var hits, uniq, conv, leads, sales, rejected uint64
		if err := rows.Scan(&r.Keyword, &hits, &uniq, &conv, &leads, &sales, &rejected, &r.Profit, &r.DepSum); err != nil {
			return nil, false, err
		}
		r.Hits, r.Unique, r.Conv = int64(hits), int64(uniq), int64(conv)
		r.Leads, r.Sales, r.Rejected = int64(leads), int64(sales), int64(rejected)
		out = append(out, r)
	}
	return out, false, rows.Err()
}

// keywordStatsSQL is the table query over union, asking one row beyond KeywordStatsLimit.
func keywordStatsSQL(union string) string {
	return keywordRankedSQL(`k, sum(hits), sum(uniq_n), sum(conv), sum(leads),
		sum(sales), sum(rejected), sum(profit_s), sum(dep_s)`, union, KeywordStatsLimit+1)
}

// KeywordStats groups the clicks of people and the conversions by keyword for a period.
// ctx bounds the one query. from and to are the window; the span is at most
// KeywordStatsMaxSpan. conds filter both sides; a condition on a field the conversions
// table lacks gives clicks only and ConvReady false. Rows are ordered by payout, then
// clicks. mode selects origin or last-click attribution.
// An empty result is an empty slice. Errors are ErrUnknownField and ErrBadCond
// (a bad range included) and any query or scan error, which is never turned into zeros.
func (c *CH) KeywordStats(ctx context.Context, from, to time.Time, conds []Cond, mode KeywordMode) (KeywordStats, error) {
	if !KeywordRangeOK(from, to) {
		return KeywordStats{}, fmt.Errorf("%w: bad range", ErrBadCond)
	}
	p, skip, err := c.planKeywords(conds, mode)
	if err != nil {
		return KeywordStats{}, err
	}
	out := KeywordStats{Rows: []KeywordRow{}}
	if skip {
		return out, nil
	}
	union, args := c.keywordUnion(p, from, to)
	out.Rows, out.Truncated, err = c.keywordRows(c.keywordCtx(ctx), keywordStatsSQL(union), args, KeywordStatsLimit)
	if err != nil {
		return KeywordStats{}, err
	}
	out.ConvReady = p.convReady
	return out, nil
}

// keywordCtx returns ctx with the analytics limits and the query size a keyword query needs.
// It does not fail.
func (c *CH) keywordCtx(ctx context.Context) context.Context {
	return c.analyticsCtxWith(ctx, 0, clickhouse.Settings{"max_query_size": keywordMaxQuerySize})
}

// keywordTopSQL selects the keys of the first top rows of the table, ranked as the table is.
// It returns the SQL and its arguments. The SQL does not fail.
func (c *CH) keywordTopSQL(p keywordPlan, from, to time.Time, top int) (string, []any) {
	union, args := c.keywordUnion(p, from, to)
	return keywordRankedSQL("k", union, top), args
}

// keywordTrendSQL is the daily query of the given keywords.
// tz is the zone name, passed as an argument; days are cut by toDate in it. keys are the
// keys of keywordTopSQL (at most KeywordTrendMaxTop), passed as one slice argument per half.
// Placeholders in order of appearance: tz, from, to, the visit filter args, keys, then
// the same for the conversion half. The SQL does not fail.
func (c *CH) keywordTrendSQL(p keywordPlan, tz string, from, to time.Time, keys []string) (string, []any) {
	q := `SELECT ` + p.evKey + ` AS k, toDate(ts, ?) AS d, count() AS hits, toUInt64(0) AS conv, toFloat64(0) AS profit_s
		FROM events WHERE ts >= ? AND ts < ?` + keywordHumans + p.evRoot + p.ev + ` AND ` + p.evKey + ` IN (?)
		GROUP BY k, d`
	args := append([]any{tz, from, to}, p.evArgs...)
	args = append(args, keys)
	if p.convReady {
		q += `
		UNION ALL
		SELECT ` + p.cvKey + ` AS k, toDate(ts, ?) AS d, toUInt64(0), count(), sum(profit)
		FROM (` + c.keywordConvUnion() + `) WHERE ts >= ? AND ts < ?` + voidedConfirmedSQL("cid", "status") + p.cv +
			` AND ` + p.cvKey + ` IN (?)
		GROUP BY k, d`
		args = append(args, tz, from, to)
		args = append(args, p.cvArgs...)
		args = append(args, keys)
	}
	return `SELECT k, toString(d) AS day, sum(hits), sum(conv), sum(profit_s) FROM (
		` + q + `
		) GROUP BY k, d ORDER BY k, d`, args
}

// trendCell is one keyword on one day, as the trend query returns it.
type trendCell struct {
	keyword string
	day     string
	hits    int64
	conv    int64
	profit  float64
}

// keywordDays lists every calendar day from from to to (exclusive) in loc, as 2006-01-02.
// It is empty when to is not after from. The days are counted on the calendar, so a
// daylight saving shift does not skip or repeat one.
func keywordDays(from, to time.Time, loc *time.Location) []string {
	f, l := from.In(loc), to.In(loc).Add(-time.Nanosecond)
	day := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, time.UTC)
	last := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
	out := []string{}
	for ; !day.After(last); day = day.AddDate(0, 0, 1) {
		out = append(out, day.Format(keywordDayLayout))
	}
	return out
}

// assembleKeywordTrend lays the cells on the days: a missing day is zero and a cell outside
// days or outside keys is dropped. There is one series per key, in the order of keys
// (the order of the table). convReady false leaves Conv and Profit nil. It does not fail.
func assembleKeywordTrend(days, keys []string, cells []trendCell, convReady bool) KeywordTrend {
	index := make(map[string]int, len(days))
	for i, d := range days {
		index[d] = i
	}
	out := KeywordTrend{Days: days, Series: make([]KeywordSeries, len(keys)), ConvReady: convReady}
	byKey := make(map[string]*KeywordSeries, len(keys))
	for i, key := range keys {
		s := &out.Series[i]
		s.Keyword, s.Hits = key, make([]int64, len(days))
		if convReady {
			s.Conv, s.Profit = make([]int64, len(days)), make([]float64, len(days))
		}
		byKey[key] = s
	}
	for _, cell := range cells {
		i, ok := index[cell.day]
		s := byKey[cell.keyword]
		if !ok || s == nil {
			continue
		}
		s.Hits[i] += cell.hits
		if convReady {
			s.Conv[i] += cell.conv
			s.Profit[i] += cell.profit
		}
	}
	return out
}

// KeywordTrend is the day-by-day course of the top keywords of the table.
// ctx bounds both queries together: they run one after the other under one deadline of
// the analytics limit. from and to are the window (at most KeywordStatsMaxSpan);
// conds filter both sides as in KeywordStats. params.Top is 1..KeywordTrendMaxTop keywords,
// chosen by the same ranking as the table. params.TZ is an IANA zone name, empty for UTC;
// days are cut in it. params.Mode selects attribution; empty keeps last-click attribution.
// Days are filled from the first to the last of the window; Conv and Profit are nil when
// ConvReady is false. Errors are ErrUnknownField and ErrBadCond (a bad range, top or zone
// included) and any query or scan error.
func (c *CH) KeywordTrend(ctx context.Context, from, to time.Time, conds []Cond, params KeywordTrendParams) (KeywordTrend, error) {
	if !KeywordRangeOK(from, to) || params.Top < 1 || params.Top > KeywordTrendMaxTop {
		return KeywordTrend{}, fmt.Errorf("%w: bad range or top", ErrBadCond)
	}
	loc, err := KeywordZone(params.TZ)
	if err != nil {
		return KeywordTrend{}, err
	}
	days := keywordDays(from, to, loc)
	p, skip, err := c.planKeywords(conds, params.Mode)
	if err != nil {
		return KeywordTrend{}, err
	}
	if skip {
		return KeywordTrend{Days: days, Series: []KeywordSeries{}}, nil
	}
	ctx = c.keywordCtx(ctx)
	if limit := c.limits.MaxExecution; limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	topSQL, topArgs := c.keywordTopSQL(p, from, to, params.Top)
	keys, err := c.keywordTopKeys(ctx, topSQL, topArgs)
	if err != nil {
		return KeywordTrend{}, err
	}
	if len(keys) == 0 {
		return KeywordTrend{Days: days, Series: []KeywordSeries{}, ConvReady: p.convReady}, nil
	}
	q, args := c.keywordTrendSQL(p, loc.String(), from, to, keys)
	cells, err := c.keywordTrendCells(ctx, q, args)
	if err != nil {
		return KeywordTrend{}, err
	}
	return assembleKeywordTrend(days, keys, cells, p.convReady), nil
}

// keywordTopKeys runs the top query and returns its keys in the order of the table.
// ctx carries the deadline of the whole trend. Any query or scan error is returned.
func (c *CH) keywordTopKeys(ctx context.Context, q string, args []any) ([]string, error) {
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// keywordTrendCells runs the daily query and returns one cell per keyword and day.
// ctx carries the deadline of the whole trend. Any query or scan error is returned.
func (c *CH) keywordTrendCells(ctx context.Context, q string, args []any) ([]trendCell, error) {
	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cells []trendCell
	for rows.Next() {
		var cell trendCell
		var hits, conv uint64
		if err := rows.Scan(&cell.keyword, &cell.day, &hits, &conv, &cell.profit); err != nil {
			return nil, err
		}
		cell.hits, cell.conv = int64(hits), int64(conv)
		cells = append(cells, cell)
	}
	return cells, rows.Err()
}
