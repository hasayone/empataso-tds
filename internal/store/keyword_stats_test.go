package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// keywordClient is a client whose events and conversions have every wave the keyword statistics read.
func keywordClient() *CH {
	c := readyClient()
	c.extConv = true
	return c
}

// originKeywordClient is ready for first-click keyword attribution.
func originKeywordClient() *CH {
	c := keywordClient()
	for _, table := range []string{"events", "conversions"} {
		c.readyFlags[table][waveOrigin] = true
		c.readyFlags[table][waveRoot] = true
	}
	c.originEvents.Store(true)
	c.originConv.Store(true)
	c.readyFlags["events"][waveStep] = true
	return c
}

// keywordWindow is a period of two days used by the SQL tests.
func keywordWindow() (time.Time, time.Time) {
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	return from, from.Add(48 * time.Hour)
}

// TestKeywordStatsSQLShape checks the table query: one UNION ALL of humans and conversions,
// the voided filter, the ranking by payout then clicks then keyword, and one row beyond the limit.
func TestKeywordStatsSQLShape(t *testing.T) {
	c := keywordClient()
	p, skip, err := c.planKeywords(nil, KeywordModeLast)
	if err != nil || skip || !p.convReady {
		t.Fatalf("plan: %+v skip=%v err=%v", p, skip, err)
	}
	from, to := keywordWindow()
	union, args := c.keywordUnion(p, from, to)
	q := keywordStatsSQL(union)
	for _, want := range []string{
		"UNION ALL",
		"conversions FINAL",
		voidedConfirmedSQL("cid", "status"),
		keywordHumans,
		"ORDER BY sum(profit_s) DESC, sum(hits) DESC, k ASC",
		fmt.Sprintf("LIMIT %d", KeywordStatsLimit+1),
		"countIf(status = 'lead')",
		"countIf" + saleStatusSQL("status"),
		"countIf(status = 'rejected')",
		"SELECT k, sum(hits)",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query has no %q:\n%s", want, q)
		}
	}
	if got := strings.Count(q, keywordKey+" AS k"); got != 2 {
		t.Errorf("both halves group by the valid-UTF-8 key, found %d:\n%s", got, q)
	}
	if n := strings.Count(q, "?"); n != len(args) || n != 4 {
		t.Errorf("placeholders %d, args %d, want 4 each:\n%s", n, len(args), q)
	}
	if KeywordStatsLimit != 2000 {
		t.Errorf("KeywordStatsLimit = %d, want the report's 2000", KeywordStatsLimit)
	}
}

// TestKeywordLastClickSQLUnchanged fixes the exact visit SQL of the default mode.
func TestKeywordLastClickSQLUnchanged(t *testing.T) {
	c := keywordClient()
	p, skip, err := c.planKeywords(nil, KeywordModeLast)
	if err != nil || skip {
		t.Fatalf("plan skip=%v err=%v", skip, err)
	}
	const want = `SELECT toValidUTF8(keyword) AS k, count() AS hits, countIf(uniq = 1) AS uniq_n, toUInt64(0) AS conv,
		toUInt64(0) AS leads, toUInt64(0) AS sales, toUInt64(0) AS rejected,
		toFloat64(0) AS profit_s, toFloat64(0) AS dep_s
		FROM events WHERE ts >= ? AND ts < ? AND (bot = '' OR bot = '-')
		GROUP BY k`
	if got := keywordStatsEvents(p); got != want {
		t.Errorf("default SQL changed:\n%s", got)
	}
	from, to := keywordWindow()
	union, _ := c.keywordUnion(p, from, to)
	legacy := want + `
		UNION ALL
		` + `SELECT ` + keywordKey + ` AS k, toUInt64(0), toUInt64(0), count(), countIf(status = 'lead'),
		countIf` + saleStatusSQL("status") + `, countIf(status = 'rejected'), sum(profit), ` + sumDep(c.depsumConv, "depsum") + `
		FROM (` + c.keywordConvUnion() + `) WHERE ts >= ? AND ts < ?` +
		voidedConfirmedSQL("cid", "status") + `
		GROUP BY k`
	if union != legacy {
		t.Errorf("default union SQL changed:\n%s", union)
	}
}

// TestKeywordOriginSQL uses root clicks, the origin conversion key and origin campaign columns
// in the table, top and trend queries.
func TestKeywordOriginSQL(t *testing.T) {
	c := originKeywordClient()
	conds := []Cond{
		{Field: "campaign_id", Op: "eq", Values: []string{"c1"}},
		{Field: "keyword", Op: "contains", Values: []string{"buy"}},
		{Field: "stream", Op: "eq", Values: []string{"main"}},
	}
	p, skip, err := c.planKeywords(conds, KeywordModeOrigin)
	if err != nil || skip || !p.convReady {
		t.Fatalf("plan: %+v skip=%v err=%v", p, skip, err)
	}
	from, to := keywordWindow()
	union, _ := c.keywordUnion(p, from, to)
	top, _ := c.keywordTopSQL(p, from, to, 5)
	trend, _ := c.keywordTrendSQL(p, "UTC", from, to, []string{"buy"})
	for name, q := range map[string]string{"table": union, "top": top, "trend": trend} {
		for _, want := range []string{
			"root_cid = ''", "step = 0", "origin_group_id",
			`toValidUTF8(if(root_cid = '', keyword, origin_keyword))`,
			`if(root_cid = '', stream_name, origin_stream_name)`,
		} {
			if !strings.Contains(q, want) {
				t.Errorf("%s SQL has no %q:\n%s", name, want, q)
			}
		}
	}
	if got := strings.Count(union, " group_id IN (?)"); got != 0 {
		t.Errorf("user origin campaign filter has %d last-click group_id conditions:\n%s", got, union)
	}
	if got := strings.Count(union, " origin_group_id IN (?)"); got < 2 {
		t.Errorf("user origin campaign filter has %d origin conditions, want both halves:\n%s", got, union)
	}
	if got := strings.Count(trend, p.cvKey+" IN (?)"); got != 1 {
		t.Errorf("trend conversion key cuts %d times, want 1:\n%s", got, trend)
	}
}

// TestKeywordOriginScopeRequiresBothCampaigns checks access scope applies to the current and
// origin campaigns on both events and conversions.
func TestKeywordOriginScopeRequiresBothCampaigns(t *testing.T) {
	c := originKeywordClient()
	scope := Cond{Field: "campaign_id", Op: "in", Values: []string{"c1"}, Scope: true}
	p, skip, err := c.planKeywords([]Cond{scope}, KeywordModeOrigin)
	if err != nil || skip || !p.convReady {
		t.Fatalf("plan: %+v skip=%v err=%v", p, skip, err)
	}
	for name, q := range map[string]string{"events": keywordStatsEvents(p), "conversions": c.keywordStatsConv(p)} {
		if n := strings.Count(q, " group_id IN (?)"); n == 0 {
			t.Errorf("%s scope has no current campaign condition:\n%s", name, q)
		}
		if n := strings.Count(q, " origin_group_id IN (?)"); n == 0 {
			t.Errorf("%s scope has no origin campaign condition:\n%s", name, q)
		}
	}
}

// TestKeywordOriginRejectsStepSpecificFilters checks origin attribution accepts only its
// campaign, key, stream and stable visitor fields; access scope bypasses this public allowlist.
func TestKeywordOriginRejectsStepSpecificFilters(t *testing.T) {
	c := originKeywordClient()
	for _, field := range []string{"campaign_id", "keyword", "stream", "country", "device", "os", "browser"} {
		if _, skip, err := c.planKeywords([]Cond{{Field: field, Op: "eq", Values: []string{"x"}}}, KeywordModeOrigin); err != nil || skip {
			t.Errorf("allowed field %s: skip=%v err=%v", field, skip, err)
		}
	}
	for _, field := range []string{"offer_id", "network_id", "landing_id", "sub_id_1", "source_id", "city"} {
		if _, _, err := c.planKeywords([]Cond{{Field: field, Op: "eq", Values: []string{"x"}}}, KeywordModeOrigin); !errors.Is(err, ErrBadCond) {
			t.Errorf("step-specific field %s: %v", field, err)
		}
		if _, _, err := c.planKeywords([]Cond{{Field: field, Op: "eq", Values: []string{"x"}, Scope: true}}, KeywordModeOrigin); err != nil {
			t.Errorf("scope field %s rejected: %v", field, err)
		}
	}
}

// TestKeywordOriginNeedsRootWave checks a partially migrated database returns an empty answer
// without issuing SQL in origin mode.
func TestKeywordOriginNeedsRootWave(t *testing.T) {
	from, to := keywordWindow()
	c := keywordClient()
	if p, skip, err := c.planKeywords(nil, KeywordModeOrigin); err != nil || !skip || p.evKey == "" {
		t.Fatalf("plan %+v skip=%v err=%v", p, skip, err)
	}
	got, err := c.KeywordStats(context.Background(), from, to, nil, KeywordModeOrigin)
	if err != nil || got.Rows == nil || len(got.Rows) != 0 {
		t.Fatalf("stats %+v err=%v", got, err)
	}
}

// TestKeywordStatsSQLWithoutConversions checks that a client without the ext wave reads
// clicks only and reports ConvReady false.
func TestKeywordStatsSQLWithoutConversions(t *testing.T) {
	c := readyClient()
	p, _, err := c.planKeywords(nil, KeywordModeLast)
	if err != nil || p.convReady {
		t.Fatalf("plan: %+v err=%v", p, err)
	}
	from, to := keywordWindow()
	union, args := c.keywordUnion(p, from, to)
	q := keywordStatsSQL(union)
	if strings.Contains(q, "UNION") || strings.Contains(q, "conversions") || strings.Contains(q, "postbacks") {
		t.Errorf("clicks-only query reads conversions:\n%s", q)
	}
	if len(args) != 2 || strings.Count(q, "?") != 2 {
		t.Errorf("args %d, placeholders %d, want 2", len(args), strings.Count(q, "?"))
	}
}

// TestKeywordPlanConditionWithoutConversionColumn checks a condition on a field the
// conversions table lacks gives clicks only, and a condition on a text field goes to both halves.
func TestKeywordPlanConditionWithoutConversionColumn(t *testing.T) {
	c := keywordClient()
	p, _, err := c.planKeywords([]Cond{{Field: "bot", Op: "eq", Values: []string{"google"}}}, KeywordModeLast)
	if err != nil || p.convReady {
		t.Fatalf("bot filter: %+v err=%v", p, err)
	}
	p, _, err = c.planKeywords([]Cond{{Field: "keyword", Op: "contains", Values: []string{"win"}}}, KeywordModeLast)
	if err != nil || !p.convReady || p.ev == "" || p.cv == "" {
		t.Fatalf("keyword filter: %+v err=%v", p, err)
	}
	from, to := keywordWindow()
	union, args := c.keywordUnion(p, from, to)
	if strings.Count(union, "?") != len(args) {
		t.Errorf("placeholders %d, args %d", strings.Count(union, "?"), len(args))
	}
	if _, _, err = c.planKeywords([]Cond{{Field: "nope", Op: "eq", Values: []string{"x"}}}, KeywordModeLast); !errors.Is(err, ErrUnknownField) {
		t.Errorf("unknown field: %v", err)
	}
}

// TestKeywordPlanUniqueCountsFirstStep checks that the unique aggregate follows the step wave
// and stays the first-step one while keyword is the grouping.
func TestKeywordPlanUniqueCountsFirstStep(t *testing.T) {
	c := keywordClient()
	p, _, _ := c.planKeywords(nil, KeywordModeLast)
	if p.uniq != "countIf(uniq = 1)" {
		t.Errorf("without the step wave: %q", p.uniq)
	}
	c.stepEvents = true
	p, _, _ = c.planKeywords(nil, KeywordModeLast)
	if !strings.Contains(p.uniq, "step = 0") {
		t.Errorf("with the step wave: %q", p.uniq)
	}
}

// TestConvWhere checks the conversion side of conds: a field with no conversion column and a
// wave that is not ready give ok false without an error; a ready field gives its fragment.
func TestConvWhere(t *testing.T) {
	c := readyClient()
	sql, args, ok, err := convWhere(c, nil)
	if err != nil || !ok || sql != "" || len(args) != 0 {
		t.Fatalf("no conds: %q %v ok=%v err=%v", sql, args, ok, err)
	}
	if _, _, ok, err = convWhere(c, []Cond{{Field: "bot", Op: "eq", Values: []string{"x"}}}); err != nil || ok {
		t.Errorf("field without a conversion column: ok=%v err=%v", ok, err)
	}
	sql, args, ok, err = convWhere(c, []Cond{{Field: "country", Op: "eq", Values: []string{"PL"}}})
	if err != nil || !ok || sql != " AND country IN (?)" || len(args) != 1 {
		t.Errorf("ready field: %q %v ok=%v err=%v", sql, args, ok, err)
	}
	notReady := &CH{readyFlags: map[string]map[wave]bool{"events": {waveBase: true}, "conversions": {waveBase: true}}}
	if _, _, ok, err = convWhere(notReady, []Cond{{Field: "keyword", Op: "eq", Values: []string{"x"}}}); err != nil || ok {
		t.Errorf("wave not ready: ok=%v err=%v", ok, err)
	}
	if _, _, _, err = convWhere(c, []Cond{{Field: "nope", Op: "eq", Values: []string{"x"}}}); err != nil {
		t.Errorf("an unknown field is the events side's error, got %v", err)
	}
	if _, _, _, err = convWhere(c, []Cond{{Field: "country", Op: "bogus", Values: []string{"x"}}}); !errors.Is(err, ErrBadCond) {
		t.Errorf("bad operator: %v", err)
	}
}

// TestKeywordTopSQL checks the first trend query ranks as the table does and keeps only top keys.
func TestKeywordTopSQL(t *testing.T) {
	c := keywordClient()
	p, _, _ := c.planKeywords(nil, KeywordModeLast)
	from, to := keywordWindow()
	q, args := c.keywordTopSQL(p, from, to, 7)
	for _, want := range []string{"SELECT k FROM (", "GROUP BY k ORDER BY sum(profit_s) DESC, sum(hits) DESC, k ASC LIMIT 7", "UNION ALL"} {
		if !strings.Contains(q, want) {
			t.Errorf("top query has no %q:\n%s", want, q)
		}
	}
	if strings.Contains(q, "LIMIT 2001") || strings.Count(q, "?") != len(args) {
		t.Errorf("limit or arguments: %d placeholders, %d args\n%s", strings.Count(q, "?"), len(args), q)
	}
}

// TestKeywordTrendSQLShape checks the daily query: both halves are cut by the keys as one
// slice argument, the filters are the table's, and the zone is an argument, never SQL text.
func TestKeywordTrendSQLShape(t *testing.T) {
	c := keywordClient()
	p, _, _ := c.planKeywords([]Cond{{Field: "country", Op: "eq", Values: []string{"PL"}}}, KeywordModeLast)
	from, to := keywordWindow()
	const tz = "Europe/Warsaw"
	keys := []string{"beta", "alpha"}
	q, args := c.keywordTrendSQL(p, tz, from, to, keys)
	for _, want := range []string{
		voidedConfirmedSQL("cid", "status"),
		keywordHumans,
		keywordKey + " IN (?)",
		"toDate(ts, ?) AS d",
		"GROUP BY k, d",
		"country IN (?)",
		"SELECT k, toString(d) AS day",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("trend query has no %q:\n%s", want, q)
		}
	}
	if got := strings.Count(q, keywordKey+" IN (?)"); got != 2 {
		t.Errorf("both halves are cut by the keys, found %d cuts", got)
	}
	if strings.Contains(q, "SELECT k FROM") || strings.Contains(q, "LIMIT") || strings.Contains(q, tz) || strings.Contains(q, "beta") {
		t.Errorf("the top query, the zone or a key is in the SQL text:\n%s", q)
	}
	if n := strings.Count(q, "?"); n != len(args) {
		t.Fatalf("placeholders %d, args %d", n, len(args))
	}
	// Order: tz, from, to, country, keys; then the same for the conversion half.
	want := []any{tz, from, to, "PL", keys, tz, from, to, "PL", keys}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args %#v, want %#v", args, want)
	}
}

// TestKeywordTrendSQLWithoutConversions checks the trend of a clicks-only client has one half.
func TestKeywordTrendSQLWithoutConversions(t *testing.T) {
	c := readyClient()
	p, _, _ := c.planKeywords(nil, KeywordModeLast)
	from, to := keywordWindow()
	q, args := c.keywordTrendSQL(p, "UTC", from, to, []string{"a"})
	if strings.Contains(q, "UNION") || strings.Contains(q, "conversions") {
		t.Errorf("clicks-only trend reads conversions:\n%s", q)
	}
	if !reflect.DeepEqual(args, []any{"UTC", from, to, []string{"a"}}) {
		t.Errorf("args %#v", args)
	}
}

// TestKeywordTrendRejectsBadInput checks top, zone and range are refused before any query.
// The client has no connection, so a query would panic.
func TestKeywordTrendRejectsBadInput(t *testing.T) {
	c := keywordClient()
	from, to := keywordWindow()
	for _, top := range []int{0, -1, 26, 1000} {
		if _, err := c.KeywordTrend(context.Background(), from, to, nil, KeywordTrendParams{Top: top}); !errors.Is(err, ErrBadCond) {
			t.Errorf("top %d: %v", top, err)
		}
	}
	if _, err := c.KeywordTrend(context.Background(), from, to, nil, KeywordTrendParams{Top: 5, TZ: "Mars/Base"}); !errors.Is(err, ErrBadCond) {
		t.Errorf("unknown zone: %v", err)
	}
	if _, err := c.KeywordTrend(context.Background(), to, from, nil, KeywordTrendParams{Top: 5}); !errors.Is(err, ErrBadCond) {
		t.Errorf("reversed range: %v", err)
	}
	if _, err := c.KeywordStats(context.Background(), from, from.Add(KeywordStatsMaxSpan+time.Second), nil, KeywordModeLast); !errors.Is(err, ErrBadCond) {
		t.Errorf("93-day range: %v", err)
	}
	if !KeywordRangeOK(from, from.Add(KeywordStatsMaxSpan)) || KeywordRangeOK(from, from) {
		t.Error("KeywordRangeOK: 92 days must pass, an empty range must not")
	}
}

// TestKeywordZone checks the zone names that reach the SQL arguments.
func TestKeywordZone(t *testing.T) {
	for _, name := range []string{"", "UTC", "Europe/Moscow", "America/Argentina/Buenos_Aires", "Etc/GMT+3"} {
		if _, err := KeywordZone(name); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
	for _, name := range []string{"Local", "x'; DROP TABLE events; --", "../etc/passwd", "/etc/localtime", "Europe/Nowhere", "Europe Moscow", strings.Repeat("a", 65)} {
		if _, err := KeywordZone(name); !errors.Is(err, ErrBadCond) {
			t.Errorf("%q accepted: %v", name, err)
		}
	}
}

// TestKeywordDays checks the days of a period are counted on the calendar of the zone,
// the end is exclusive, and a daylight saving shift does not skip a day.
func TestKeywordDays(t *testing.T) {
	utc := time.UTC
	day := func(d int) time.Time { return time.Date(2026, 3, d, 0, 0, 0, 0, utc) }
	if got := keywordDays(day(1), day(4), utc); !reflect.DeepEqual(got, []string{"2026-03-01", "2026-03-02", "2026-03-03"}) {
		t.Errorf("midnight end is exclusive: %v", got)
	}
	if got := keywordDays(day(1), day(3).Add(time.Hour), utc); len(got) != 3 {
		t.Errorf("end inside the last day: %v", got)
	}
	warsaw, _ := KeywordZone("Europe/Warsaw")
	got := keywordDays(time.Date(2026, 3, 27, 12, 0, 0, 0, utc), time.Date(2026, 3, 30, 12, 0, 0, 0, utc), warsaw)
	if !reflect.DeepEqual(got, []string{"2026-03-27", "2026-03-28", "2026-03-29", "2026-03-30"}) {
		t.Errorf("across the spring shift: %v", got)
	}
	ahead, _ := KeywordZone("Pacific/Kiritimati")
	if got := keywordDays(time.Date(2026, 3, 1, 12, 0, 0, 0, utc), time.Date(2026, 3, 1, 13, 0, 0, 0, utc), ahead); !reflect.DeepEqual(got, []string{"2026-03-02"}) {
		t.Errorf("zone ahead of UTC: %v", got)
	}
	if got := keywordDays(day(2), day(1), utc); got == nil || len(got) != 0 {
		t.Errorf("reversed range: %#v", got)
	}
}

// TestAssembleKeywordTrend checks gaps become zeros, series follow the order of the keys
// and a clicks-only trend has no conversion arrays.
func TestAssembleKeywordTrend(t *testing.T) {
	days := []string{"2026-03-01", "2026-03-02", "2026-03-03"}
	keys := []string{"rich", "b", "a", "quiet"}
	cells := []trendCell{
		{keyword: "b", day: "2026-03-01", hits: 5},
		{keyword: "a", day: "2026-03-03", hits: 5},
		{keyword: "rich", day: "2026-03-02", hits: 1, conv: 1, profit: 9.5},
		{keyword: "a", day: "2026-03-01", hits: 2, conv: 1, profit: 0},
		{keyword: "gone", day: "2026-03-01", hits: 100},
		{keyword: "b", day: "2026-04-01", hits: 100},
	}
	got := assembleKeywordTrend(days, keys, cells, true)
	if !got.ConvReady || !reflect.DeepEqual(got.Days, days) || len(got.Series) != len(keys) {
		t.Fatalf("days/ready/series: %+v", got)
	}
	for i, s := range got.Series {
		if s.Keyword != keys[i] {
			t.Errorf("series %d is %q, want %q: the order is the keys'", i, s.Keyword, keys[i])
		}
	}
	a := got.Series[2]
	if !reflect.DeepEqual(a.Hits, []int64{2, 0, 5}) || !reflect.DeepEqual(a.Conv, []int64{1, 0, 0}) || len(a.Profit) != 3 {
		t.Errorf("series a: %+v", a)
	}
	if sumInt(got.Series[1].Hits) != 5 || sumInt(got.Series[3].Hits) != 0 || sumFloat(got.Series[0].Profit) != 9.5 {
		t.Errorf("a day outside the period and a key outside the list must be dropped: %+v", got.Series)
	}
	plain := assembleKeywordTrend(days, keys, cells, false)
	for _, s := range plain.Series {
		if s.Conv != nil || s.Profit != nil || len(s.Hits) != 3 {
			t.Errorf("clicks-only series %+v", s)
		}
	}
	if empty := assembleKeywordTrend(days, nil, nil, true); empty.Series == nil || len(empty.Series) != 0 {
		t.Errorf("no keys: %#v", empty.Series)
	}
}

// fakeRows serves canned rows to Scan; every cell is a string, uint64, int64 or float64.
type fakeRows struct {
	driver.Rows
	data [][]any
	pos  int
}

func (r *fakeRows) Next() bool   { r.pos++; return r.pos <= len(r.data) }
func (r *fakeRows) Close() error { return nil }
func (r *fakeRows) Err() error   { return nil }
func (r *fakeRows) Scan(dest ...any) error {
	for i, v := range r.data[r.pos-1] {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *uint64:
			*d = v.(uint64)
		case *int64:
			*d = v.(int64)
		case *float64:
			*d = v.(float64)
		}
	}
	return nil
}

// fakeConn records every query with its context and answers with the next canned result.
// A nil entry of fail is a success; a non-nil one is the error of that query.
type fakeConn struct {
	driver.Conn
	ctxs    []context.Context
	queries []string
	args    [][]any
	results []*fakeRows
	fail    error
}

func (f *fakeConn) Query(ctx context.Context, q string, args ...any) (driver.Rows, error) {
	f.ctxs, f.queries, f.args = append(f.ctxs, ctx), append(f.queries, q), append(f.args, args)
	if f.fail != nil {
		return nil, f.fail
	}
	return f.results[len(f.ctxs)-1], nil
}

// TestKeywordTrendRunsTwoQueriesUnderOneContext checks the trend asks the top keys first and
// then the days of exactly those keys, as a slice argument, both under the same context (so
// the same analytics settings and one deadline), and lays the series in the order of the top query.
func TestKeywordTrendRunsTwoQueriesUnderOneContext(t *testing.T) {
	conn := &fakeConn{results: []*fakeRows{
		{data: [][]any{{"beta"}, {"alpha"}}},
		{data: [][]any{
			{"alpha", "2026-03-01", uint64(2), uint64(1), 2.5},
			{"beta", "2026-03-01", uint64(1), uint64(0), 0.0},
			{"beta", "2026-03-02", uint64(4), uint64(2), 30.0},
		}},
	}}
	c := keywordClient()
	c.conn, c.limits = conn, AnalyticsLimits{MaxExecution: time.Minute, Priority: 10}
	from, to := keywordWindow()
	got, err := c.KeywordTrend(context.Background(), from, to, nil, KeywordTrendParams{Top: 2, TZ: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.queries) != 2 {
		t.Fatalf("%d queries, want the top and the days", len(conn.queries))
	}
	if !strings.Contains(conn.queries[0], "LIMIT 2") || strings.Contains(conn.queries[0], "toDate(ts") {
		t.Errorf("first query is not the top:\n%s", conn.queries[0])
	}
	if !strings.Contains(conn.queries[1], "toDate(ts, ?)") || strings.Contains(conn.queries[1], "LIMIT") {
		t.Errorf("second query is not the days:\n%s", conn.queries[1])
	}
	if conn.ctxs[0] != conn.ctxs[1] {
		t.Error("the two queries run under different contexts")
	}
	settings, ok := analyticsSettingsFrom(conn.ctxs[1])
	if !ok || settings["max_execution_time"] != 60 || settings["priority"] != 10 || settings["max_query_size"] != keywordMaxQuerySize {
		t.Errorf("settings of the second query: %v %v", settings, ok)
	}
	if _, ok := conn.ctxs[1].Deadline(); !ok {
		t.Error("the trend has no common deadline")
	}
	wantKeys := []string{"beta", "alpha"}
	if args := conn.args[1]; !reflect.DeepEqual(args[len(args)-1], wantKeys) || !reflect.DeepEqual(args[len(args)/2-1], wantKeys) {
		t.Errorf("keys are not passed as one slice per half: %#v", args)
	}
	if len(got.Series) != 2 || got.Series[0].Keyword != "beta" || got.Series[1].Keyword != "alpha" {
		t.Fatalf("series %+v, want the order of the top query", got.Series)
	}
	if !reflect.DeepEqual(got.Series[0].Hits, []int64{1, 4}) || !reflect.DeepEqual(got.Series[0].Profit, []float64{0, 30}) ||
		!reflect.DeepEqual(got.Series[1].Conv, []int64{1, 0}) {
		t.Errorf("series %+v", got.Series)
	}
}

// TestKeywordTrendEmptyTopSkipsTheDays checks a period with no keywords asks once and
// answers empty series, and a failed first query is returned with no second one.
func TestKeywordTrendEmptyTopSkipsTheDays(t *testing.T) {
	conn := &fakeConn{results: []*fakeRows{{}}}
	c := keywordClient()
	c.conn = conn
	from, to := keywordWindow()
	got, err := c.KeywordTrend(context.Background(), from, to, nil, KeywordTrendParams{Top: 5})
	if err != nil || len(conn.queries) != 1 || got.Series == nil || len(got.Series) != 0 || len(got.Days) != 2 || !got.ConvReady {
		t.Fatalf("%+v err=%v queries=%d", got, err, len(conn.queries))
	}
	boom := errors.New("boom")
	conn = &fakeConn{fail: boom}
	c.conn = conn
	if _, err := c.KeywordTrend(context.Background(), from, to, nil, KeywordTrendParams{Top: 5}); !errors.Is(err, boom) || len(conn.queries) != 1 {
		t.Errorf("err=%v queries=%d", err, len(conn.queries))
	}
}

// sumInt returns the sum of v.
func sumInt(v []int64) (n int64) {
	for _, x := range v {
		n += x
	}
	return n
}

// sumFloat returns the sum of v.
func sumFloat(v []float64) (n float64) {
	for _, x := range v {
		n += x
	}
	return n
}

// TestKeywordQueriesCarryQuerySizeAndLimits checks the stats query and both queries of the
// trend run with max_query_size next to the analytics limits, which the driver would otherwise
// lose when it replaces the settings of a context.
func TestKeywordQueriesCarryQuerySizeAndLimits(t *testing.T) {
	conn := &fakeConn{results: []*fakeRows{{}, {data: [][]any{{"a"}}}, {}}}
	c := keywordClient()
	c.conn, c.limits = conn, AnalyticsLimits{MaxExecution: 30 * time.Second, Priority: 7, MaxThreads: 2, MaxMemory: 1 << 30}
	from, to := keywordWindow()
	if _, err := c.KeywordStats(context.Background(), from, to, nil, KeywordModeLast); err != nil {
		t.Fatal(err)
	}
	if _, err := c.KeywordTrend(context.Background(), from, to, nil, KeywordTrendParams{Top: 3, TZ: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if len(conn.ctxs) != 3 {
		t.Fatalf("%d queries, want stats and the two of the trend", len(conn.ctxs))
	}
	for i, ctx := range conn.ctxs {
		s, ok := analyticsSettingsFrom(ctx)
		if !ok || s["max_query_size"] != keywordMaxQuerySize || s["max_execution_time"] != 30 ||
			s["priority"] != 7 || s["max_threads"] != 2 || s["max_memory_usage"] != int64(1<<30) {
			t.Errorf("query %d settings: %v %v", i, s, ok)
		}
	}
	if keywordMaxQuerySize <= 262144 {
		t.Errorf("max_query_size %d is not above the server default", keywordMaxQuerySize)
	}
}

// TestAnalyticsCtxWithKeepsPlainCallsUnchanged checks extra settings join the limits, win over
// a limit of the same name, and that no extra leaves analyticsCtx as it was.
func TestAnalyticsCtxWithKeepsPlainCallsUnchanged(t *testing.T) {
	c := &CH{limits: AnalyticsLimits{MaxExecution: time.Minute, Priority: 10}}
	plain, _ := analyticsSettingsFrom(c.analyticsCtx(context.Background(), 0))
	if len(plain) != 2 || plain["max_query_size"] != nil {
		t.Errorf("plain settings %v", plain)
	}
	got, _ := analyticsSettingsFrom(c.analyticsCtxWith(context.Background(), 0, clickhouse.Settings{"max_query_size": 5, "priority": 1}))
	if len(got) != 3 || got["max_execution_time"] != 60 || got["priority"] != 1 || got["max_query_size"] != 5 {
		t.Errorf("merged settings %v", got)
	}
	wrapped := c.analyticsCtx(context.Background(), 0)
	if c.analyticsCtx(wrapped, 0) != wrapped {
		t.Error("an already wrapped context was rewrapped")
	}
}
