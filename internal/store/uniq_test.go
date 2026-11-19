package store

import (
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/logbuf"
)

// squash collapses whitespace so a query compares by its tokens.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestUniqExprFollowsStepWave(t *testing.T) {
	if got := uniqExpr(false); got != "countIf(uniq = 1)" {
		t.Errorf("without the wave: %q", got)
	}
	if got := uniqExpr(true); got != "countIf(uniq = 1 AND step = 0)" {
		t.Errorf("with the wave: %q", got)
	}
	if (&CH{}).uniqCount(nil) != uniqExpr(false) || (&CH{stepEvents: true}).uniqCount(nil) != uniqExpr(true) {
		t.Error("uniqCount must follow the stepEvents flag")
	}
	var none *CH
	if none.uniqCount(nil) != uniqExpr(false) {
		t.Error("a nil client counts the plain flag")
	}
}

// Every campaign and stream summary takes its unique count from one expression.
func TestSummaryQueriesUseStepFilterOnlyWithWave(t *testing.T) {
	queries := map[string]func(uniq string) string{
		"summary":     func(u string) string { return summarySQL(u, " AND country = ?") },
		"series":      func(u string) string { return seriesSQL(u, " AND country = ?") },
		"performance": func(u string) string { return performanceSQL(u, " AND country = ?") },
		"forwarded":   func(u string) string { return forwardedSQL(u, " AND country = ?") },
		"buckets":     func(u string) string { return bucketsSQL(true, u, bucketWhere(false, true)) },
		"buckets/old": func(u string) string { return bucketsSQL(false, u, bucketWhere(false, true)) },
	}
	for name, build := range queries {
		plain := squash(build(uniqExpr(false)))
		stepped := squash(build(uniqExpr(true)))
		if strings.Contains(plain, "step") || !strings.Contains(plain, "countIf(uniq = 1)") {
			t.Errorf("%s without the wave: %s", name, plain)
		}
		if !strings.Contains(stepped, "countIf(uniq = 1 AND step = 0)") || strings.Contains(stepped, "countIf(uniq = 1)") {
			t.Errorf("%s with the wave: %s", name, stepped)
		}
		if !strings.Contains(plain, "country = ?") && name != "buckets" && name != "buckets/old" {
			t.Errorf("%s lost the filter: %s", name, plain)
		}
	}
}

// The catalog facts count every row on its own: a landing and an offer are
// separate objects and their uniques are not merged. Rows are never filtered by
// step; only the extra uniq_first aggregate looks at it, and only with the wave.
func TestFactsKeepEveryStep(t *testing.T) {
	for _, click := range []bool{false, true} {
		for _, cost := range []bool{false, true} {
			plain := clickFactsSQL(click, cost, false, "")
			if strings.Contains(plain, "step") {
				t.Errorf("clickFactsSQL(%v, %v) without the wave mentions step: %s", click, cost, plain)
			}
			if !strings.Contains(plain, "count(), countIf(uniq = 1), countIf(uniq = 1)") {
				t.Errorf("without the wave uniq_first must equal uniq: %s", plain)
			}
			stepped := clickFactsSQL(click, cost, true, "")
			if !strings.Contains(stepped, "count(), countIf(uniq = 1), countIf(uniq = 1 AND step = 0)") {
				t.Errorf("clickFactsSQL(%v, %v) with the wave: %s", click, cost, stepped)
			}
			if strings.Contains(stepped, "WHERE") && strings.Contains(strings.SplitN(stepped, "GROUP BY", 2)[0], "step = 0 AND") {
				t.Errorf("step must not filter rows: %s", stepped)
			}
		}
	}
}

// A click row without a price has an empty currency key, so the zero-cent
// currency label of the /offer step does not open a currency of its own.
func TestFactsCurrencyKeyNeedsCents(t *testing.T) {
	q := clickFactsSQL(true, true, true, "")
	if !strings.Contains(q, "if(cost_cents > 0, cost_cur, '') AS fact_cur") || !strings.Contains(q, "GROUP BY group_id, stream_name, offer_id, landing_id, network_id, fact_cur") {
		t.Errorf("currency key: %s", q)
	}
	where := clickFactsSQL(true, true, true, " AND cost_cur = ?")
	if !strings.Contains(where, "AND cost_cur = ?") {
		t.Errorf("the cost_cur filter must stay on the column: %s", where)
	}
	for name, q := range map[string]string{
		"performance cost": performanceCostSQL(""),
		"series cost":      costSeriesSQL(""),
	} {
		if !strings.Contains(q, "cost_cents > 0") {
			t.Errorf("%s groups by currency without cost_cents > 0: %s", name, q)
		}
	}
}

// With the wave not ready the spend weights read exactly the query that served
// them before the step column existed.
func TestBucketsSQLWithoutWaveIsUnchanged(t *testing.T) {
	where := bucketWhere(false, true)
	oldClick := `SELECT stream_name, offer_id, network_id, count(), countIf(uniq = 1)
			FROM events WHERE ` + where + `
			GROUP BY stream_name, offer_id, network_id`
	if got := squash(bucketsSQL(true, uniqExpr(false), where)); got != squash(oldClick) {
		t.Errorf("click columns:\n got %s\nwant %s", got, squash(oldClick))
	}
	oldPlain := `SELECT stream_name, count(), countIf(uniq = 1) FROM events WHERE ` + where + ` GROUP BY stream_name`
	if got := squash(bucketsSQL(false, uniqExpr(false), where)); got != squash(oldPlain) {
		t.Errorf("plain:\n got %s\nwant %s", got, squash(oldPlain))
	}
}

// The window filter of a manual spend skips a click that came with a price:
// cents or the currency label. The /offer step keeps the label at zero cents, so
// it is skipped like the landing row. The filter never looks at step.
func TestBucketWhereSkipsPricedClicks(t *testing.T) {
	for _, bots := range []bool{false, true} {
		if w := bucketWhere(bots, true); !strings.Contains(w, " AND cost_cents = 0 AND cost_cur = ''") {
			t.Errorf("bucketWhere(%v, true) = %s", bots, w)
		}
		if w := bucketWhere(bots, false); strings.Contains(w, "cost_") {
			t.Errorf("bucketWhere(%v, false) = %s", bots, w)
		}
		for _, cost := range []bool{false, true} {
			if w := bucketWhere(bots, cost); strings.Contains(w, "step") {
				t.Errorf("bucketWhere(%v, %v) = %s", bots, cost, w)
			}
		}
	}
}

// A selection by offer or network keeps the plain unique flag; scope, exclusions
// and other fields keep the first-step filter.
func TestUniqCountByConds(t *testing.T) {
	c := &CH{stepEvents: true}
	plain, first := uniqExpr(false), uniqExpr(true)
	cases := []struct {
		name  string
		conds []Cond
		want  string
	}{
		{"none", nil, first},
		{"campaign", []Cond{{Field: "campaign_id", Op: "eq", Values: []string{"7"}}}, first},
		{"landing", []Cond{{Field: "landing_id", Op: "eq", Values: []string{"2"}}}, first},
		{"offer eq", []Cond{{Field: "offer_id", Op: "eq", Values: []string{"4"}}}, plain},
		{"offer in", []Cond{{Field: "campaign_id", Op: "eq", Values: []string{"7"}}, {Field: "offer_id", Op: "in", Values: []string{"4", "5"}}}, plain},
		{"network eq", []Cond{{Field: "network_id", Op: "eq", Values: []string{"9"}}}, plain},
		{"offer ne", []Cond{{Field: "offer_id", Op: "ne", Values: []string{"4"}}}, first},
		{"network nin", []Cond{{Field: "network_id", Op: "nin", Values: []string{"9"}}}, first},
		{"offer scope", []Cond{{Field: "offer_id", Op: "in", Values: []string{"4"}, Scope: true}}, first},
	}
	for _, tc := range cases {
		if got := c.uniqCount(tc.conds); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	if (&CH{}).uniqCount([]Cond{{Field: "campaign_id", Op: "eq", Values: []string{"7"}}}) != plain {
		t.Error("without the wave every selection counts the plain flag")
	}
}

func TestStepColumnIsAddedWithoutMutation(t *testing.T) {
	if got := alterSQL("events", waveStep); got != "ADD COLUMN IF NOT EXISTS step UInt8 DEFAULT 0" {
		t.Errorf("events ALTER = %q", got)
	}
	if got := alterSQL("conversions", waveStep); got != "" {
		t.Errorf("conversions must not get step: %q", got)
	}
	if strings.Contains(strings.ToUpper(alterSQL("events", waveStep)), "UPDATE") {
		t.Error("the step ALTER must not touch existing data")
	}
	if cols := columnsOf("events", waveStep); len(cols) != 1 || cols[0] != "step" {
		t.Errorf("step wave columns = %v", cols)
	}
}

func TestStepWaveReadiness(t *testing.T) {
	have := map[string]map[string]bool{"events": {"step": true}}
	if !readyFromColumns(have)["events"][waveStep] {
		t.Error("step present must make the wave ready")
	}
	if readyFromColumns(map[string]map[string]bool{"events": {}})["events"][waveStep] {
		t.Error("step absent must leave the wave not ready")
	}
	c := &CH{readyFlags: readyFromColumns(have)}
	if !c.ready("events", waveStep) || !c.Fields().Ready["events"]["step"] {
		t.Error("ready and the fields API must report the step wave")
	}
	if (&CH{}).Fields().Ready["events"]["step"] {
		t.Error("an unprobed client has no step wave")
	}
}

// A grouping by offer or network counts the row's own uniques, like a selection.
func TestUniqCountByDims(t *testing.T) {
	c := &CH{stepEvents: true}
	plain, first := uniqExpr(false), uniqExpr(true)
	if got := c.uniqCount(nil, "offer_id", "source_id"); got != plain {
		t.Errorf("offer grouping: %s", got)
	}
	if got := c.uniqCount(nil, "network_id"); got != plain {
		t.Errorf("network grouping: %s", got)
	}
	if got := c.uniqCount(nil, "source_id", "country"); got != first {
		t.Errorf("source grouping: %s", got)
	}
	if got := c.uniqCount(nil); got != first {
		t.Errorf("no grouping: %s", got)
	}
}

// The report counts uniques by the shared rule: its own row uniques while the
// offer column is grouped on, first-step uniques without it, and always carries
// the summable first-step count next to it.
func TestSliceEventsSQLUniques(t *testing.T) {
	stepped := &CH{stepEvents: true, clickCols: true}
	q := squash(sliceEventsSQL(true, stepped.uniqCount(nil, sliceDims(true)...), stepped.firstStepUniq(), " AND country = ?", SliceLimit, false))
	if !strings.Contains(q, "countIf(uniq = 1) AS uniq_hits, countIf(uniq = 1 AND step = 0) AS uniq_first") {
		t.Errorf("with click columns: %s", q)
	}
	if !strings.Contains(q, "GROUP BY group_name, stream_name, country, offer_id, source_id") || !strings.Contains(q, "AND country = ?") {
		t.Errorf("grouping or filter lost: %s", q)
	}
	bare := &CH{stepEvents: true}
	q = squash(sliceEventsSQL(false, bare.uniqCount(nil, sliceDims(false)...), bare.firstStepUniq(), "", SliceLimit, false))
	if !strings.Contains(q, "countIf(uniq = 1 AND step = 0) AS uniq_hits, countIf(uniq = 1 AND step = 0) AS uniq_first") {
		t.Errorf("without click columns: %s", q)
	}
	old := &CH{clickCols: true}
	q = squash(sliceEventsSQL(true, old.uniqCount(nil, sliceDims(true)...), old.firstStepUniq(), "", SliceLimit, false))
	if strings.Contains(q, "step") {
		t.Errorf("without the wave the report must not mention step: %s", q)
	}
}

// The step column is internal: the field list of the interface and the roles
// leaves it out, while the server registry still knows it.
func TestStepFieldIsInternal(t *testing.T) {
	for _, f := range StaticFields() {
		if f.Name == "step" {
			t.Fatal("step is listed for the interface")
		}
	}
	if f, ok := fieldByName("step"); !ok || !f.Internal {
		t.Fatalf("step in the server registry: ok=%v internal=%v", ok, f.Internal)
	}
	if !(&CH{stepEvents: true}).StepReady() || (&CH{}).StepReady() {
		t.Error("StepReady must follow the wave")
	}
	var none *CH
	if none.StepReady() {
		t.Error("a nil client has no step wave")
	}
}

// The step column is an events-only filter field: it is accepted on events,
// rejected on conversions, and skips the query while the wave is not ready.
func TestStepFilterField(t *testing.T) {
	conds := []Cond{{Field: "step", Op: "eq", Values: []string{"1"}}}
	if err := ValidateConds("events", conds); err != nil {
		t.Fatalf("events: %v", err)
	}
	if err := ValidateConds("conversions", conds); err != ErrUnknownField {
		t.Fatalf("conversions: %v, want ErrUnknownField", err)
	}
	ready := &CH{readyFlags: readyFromColumns(map[string]map[string]bool{"events": {"step": true}})}
	sql, args, skip, err := buildWhere(ready, "events", conds)
	if err != nil || skip || !strings.Contains(sql, "step") || len(args) != 1 {
		t.Fatalf("ready: sql=%q args=%v skip=%v err=%v", sql, args, skip, err)
	}
	if _, _, skip, _ := buildWhere(&CH{}, "events", conds); !skip {
		t.Error("a missing wave must skip the query")
	}
}

func TestEventColumnsAndValuesCarryStep(t *testing.T) {
	without := eventColumns(&CH{}, false)
	for _, col := range without {
		if col == "step" {
			t.Fatal("step written before the wave is ready")
		}
	}
	c := &CH{stepEvents: true}
	cols := eventColumns(c, false)
	if cols[len(cols)-1] != "step" || len(cols) != len(without)+1 {
		t.Fatalf("columns %v", cols)
	}
	first := eventValues(cols, logbuf.Event{Ts: time.Unix(1, 0)})
	offer := eventValues(cols, logbuf.Event{Ts: time.Unix(1, 0), Step: 1})
	if first[len(first)-1] != uint8(0) || offer[len(offer)-1] != uint8(1) {
		t.Errorf("step values: first=%v offer=%v", first[len(first)-1], offer[len(offer)-1])
	}
}

// Imported clicks have no steps: they are written as step 0, so they count in
// every unique summary.
func TestImportedEventsAreStepZero(t *testing.T) {
	cols := eventColumns(&CH{stepEvents: true}, false)
	vals := eventValues(cols, logbuf.Event{Ts: time.Unix(1, 0), Uniq: 1})
	if vals[len(vals)-1] != uint8(0) {
		t.Errorf("imported event step = %v, want 0", vals[len(vals)-1])
	}
}
