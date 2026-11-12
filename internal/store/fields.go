package store

import (
	"fmt"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

// wave names one generation of columns. base is the original table,
// the others are added by ALTER and may be absent on an old database.
type wave string

const (
	waveBase  wave = "base"
	waveNet   wave = "net"
	waveClick wave = "click"
	waveCost  wave = "cost"
	waveExt   wave = "ext"
	// waveModel adds device_model to events and conversions.
	waveModel wave = "model"
	// waveStep adds step to events: 0 for the first step, 1 for the /offer step.
	waveStep wave = "step"
	// waveSub adds sub_id_13 through sub_id_30. ensureSchema does not ALTER
	// this wave: the columns appear only after migrations/clickhouse/011_sub_id_30.sql.
	waveSub wave = "sub"
	// clickWaveSubs is how many sub_id columns the click wave already owns.
	clickWaveSubs = 12
	// waveOrigin adds origin_group_name and origin_group_id to events and conversions.
	waveOrigin wave = "origin"
	// waveDep adds depsum to conversions. It is the deposit, not the payout.
	waveDep wave = "dep"
)

// allWaves lists every wave in the order probes and the fields API use.
var allWaves = []wave{waveBase, waveNet, waveClick, waveCost, waveExt, waveModel, waveStep, waveOrigin, waveDep, waveSub}

// Field is one journal column shared by filters, breakdown and the SPA.
// Name is the API name. Events and Conv are the physical columns; an empty
// string means the field has no column on that table. Type is the ClickHouse
// type used by ALTER. EventsWave and ConvWave say which generation owns the
// column; the wave for a table the field does not have is empty.
// Dim allows GROUP BY and /api/log-values. Lower trims and lowercases filter
// values (status only). Expr, when set, replaces the column in breakdown and
// filters. Default, when set, is the constant DEFAULT clause of ADD COLUMN;
// a constant default is stored in the table metadata and rewrites no data.
type Field struct {
	Name       string
	Events     string
	Conv       string
	Type       string
	EventsWave wave
	ConvWave   wave
	Dim        bool
	Lower      bool
	Expr       string
	Default    string
	// Internal marks a column the server reads and writes but the interface and
	// the access roles never see: StaticFields leaves it out.
	Internal bool
}

// lc is LowCardinality(String), the usual type of a short repeated label.
const lc = "LowCardinality(String)"

// fields is the only list of journal columns. ALTER, SELECT and INSERT
// follow this order.
var fields = buildFields()

// buildFields returns the journal columns in contract order.
// It has no parameters. The slice is the source for ALTER and filters.
// It does not fail: the names are fixed.
func buildFields() []Field {
	out := []Field{
		{Name: "ts", Events: "ts", Conv: "ts", Type: "DateTime", EventsWave: waveBase, ConvWave: waveBase},
		{Name: "campaign", Events: "group_name", Conv: "group_name", Type: lc, EventsWave: waveBase, ConvWave: waveBase, Dim: true},
		{Name: "stream", Events: "stream_name", Conv: "stream_name", Type: lc, EventsWave: waveBase, ConvWave: waveBase, Dim: true},
		{Name: "country", Events: "country", Conv: "country", Type: lc, EventsWave: waveBase, ConvWave: waveBase, Dim: true},
		{Name: "city", Events: "city", Conv: "city", Type: "String", EventsWave: waveBase, ConvWave: waveBase, Dim: true},
		{Name: "device", Events: "device", Conv: "device", Type: lc, EventsWave: waveBase, ConvWave: waveBase, Dim: true},
		{Name: "operator", Events: "operator", Conv: "operator", Type: lc, EventsWave: waveBase, ConvWave: waveBase, Dim: true},
		{Name: "domain", Events: "domain", Conv: "domain", Type: "String", EventsWave: waveBase, ConvWave: waveBase, Dim: true},
		{Name: "page", Events: "page", Conv: "page", Type: "String", EventsWave: waveBase, ConvWave: waveBase},
		{Name: "cid", Events: "cid", Conv: "cid", Type: "String", EventsWave: waveBase, ConvWave: waveBase},
		{Name: "region", Events: "region", Conv: "region", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "lang", Events: "lang", Conv: "lang", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "os", Events: "os", Conv: "os", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "os_version", Events: "os_version", Conv: "os_version", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "browser", Events: "browser", Conv: "browser", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "browser_ver", Events: "browser_ver", Conv: "browser_ver", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "brand", Events: "brand", Conv: "brand", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "device_model", Events: "device_model", Conv: "device_model", Type: lc, EventsWave: waveModel, ConvWave: waveModel, Dim: true},
		{Name: "ip", Events: "ip", Conv: "ip", Type: "String", EventsWave: waveBase, ConvWave: waveExt},
		{Name: "keyword", Events: "keyword", Conv: "keyword", Type: "String", EventsWave: waveBase, ConvWave: waveExt},
		{Name: "se", Events: "se", Conv: "se", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "referer", Events: "referer", Conv: "referer", Type: "String", EventsWave: waveBase, ConvWave: waveExt},
		{Name: "useragent", Events: "useragent", Conv: "useragent", Type: "String", EventsWave: waveBase, ConvWave: waveExt},
		{Name: "campaign_id", Events: "group_id", Conv: "group_id", Type: lc, EventsWave: waveBase, ConvWave: waveExt, Dim: true},
		{Name: "bot", Events: "bot", Type: lc, EventsWave: waveBase, Dim: true},
		{Name: "uniq", Events: "uniq", Type: "UInt8", EventsWave: waveBase, Dim: true},
		{Name: "redirect", Events: "redirect", Type: lc, EventsWave: waveBase, Dim: true},
		{Name: "out", Events: "out", Type: "String", EventsWave: waveBase, Dim: true},
		{Name: "status", Conv: "status", Type: lc, ConvWave: waveBase, Dim: true, Lower: true},
		{Name: "profit", Conv: "profit", Type: "Float64", ConvWave: waveBase},
		{Name: "depsum", Conv: "depsum", Type: "Float64", ConvWave: waveDep, Default: "0"},
		{Name: "asn", Events: "asn", Conv: "asn", Type: "UInt32", EventsWave: waveNet, ConvWave: waveExt, Dim: true,
			Expr: "if(asn = 0, '', concat('AS', toString(asn)))"},
		{Name: "org", Events: "org", Conv: "org", Type: lc, EventsWave: waveNet, ConvWave: waveExt, Dim: true},
		{Name: "timezone", Events: "timezone", Type: lc, EventsWave: waveNet, Dim: true},
		{Name: "via", Events: "via", Type: lc, EventsWave: waveNet, Dim: true},
		{Name: "origin_campaign", Events: "origin_group_name", Conv: "origin_group_name", Type: lc, EventsWave: waveOrigin, ConvWave: waveOrigin, Dim: true, Default: "group_name"},
		{Name: "origin_campaign_id", Events: "origin_group_id", Conv: "origin_group_id", Type: lc, EventsWave: waveOrigin, ConvWave: waveOrigin, Dim: true, Default: "group_id"},
	}
	for i := 1; i <= clickWaveSubs; i++ {
		name := fmt.Sprintf("sub_id_%d", i)
		out = append(out, Field{Name: name, Events: name, Conv: name, Type: "String", EventsWave: waveClick, ConvWave: waveClick, Dim: true})
	}
	for _, name := range []string{"offer_id", "landing_id", "source_id", "network_id"} {
		out = append(out, Field{Name: name, Events: name, Conv: name, Type: lc, EventsWave: waveClick, ConvWave: waveClick, Dim: true})
	}
	out = append(out,
		Field{Name: "cost_cents", Events: "cost_cents", Type: "Int64", EventsWave: waveCost},
		Field{Name: "cost_cur", Events: "cost_cur", Type: lc, EventsWave: waveCost, Dim: true},
		Field{Name: "site", Events: "site", Conv: "site", Type: "String", EventsWave: waveExt, ConvWave: waveExt, Dim: true},
		Field{Name: "ad_campaign_id", Events: "ad_campaign_id", Conv: "ad_campaign_id", Type: "String", EventsWave: waveExt, ConvWave: waveExt, Dim: true},
		Field{Name: "creative_id", Events: "creative_id", Conv: "creative_id", Type: "String", EventsWave: waveExt, ConvWave: waveExt, Dim: true},
		Field{Name: "external_id", Events: "external_id", Conv: "external_id", Type: "String", EventsWave: waveExt, ConvWave: waveExt, Dim: true},
		Field{Name: "xrw", Events: "xrw", Conv: "xrw", Type: lc, EventsWave: waveExt, ConvWave: waveExt, Dim: true},
		Field{Name: "click_ts", Conv: "click_ts", Type: "DateTime", ConvWave: waveExt},
		Field{Name: "original_status", Conv: "original_status", Type: "String", ConvWave: waveExt, Dim: true},
		Field{Name: "prev_status", Conv: "prev_status", Type: lc, ConvWave: waveExt, Dim: true},
		Field{Name: "tid", Conv: "tid", Type: "String", ConvWave: waveExt},
		Field{Name: "step", Events: "step", Type: "UInt8", EventsWave: waveStep, Default: "0", Internal: true},
	)
	for i := clickWaveSubs + 1; i <= config.MaxSubID; i++ {
		name := fmt.Sprintf("sub_id_%d", i)
		out = append(out, Field{Name: name, Events: name, Conv: name, Type: "String", EventsWave: waveSub, ConvWave: waveSub, Dim: true})
	}
	return out
}

// fieldAliases maps an old breakdown name onto Name.
var fieldAliases = map[string]string{"group": "campaign"}

// fieldByName returns the field called name.
// name is the API name, or the alias group for campaign.
// The bool is false when nothing matches. It does not touch the database.
func fieldByName(name string) (Field, bool) {
	if a, ok := fieldAliases[name]; ok {
		name = a
	}
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// tableCol returns the physical column and its wave on table.
// table is "events" or "conversions". An unknown table or a field that
// has no column there returns "", "".
func (f Field) tableCol(table string) (string, wave) {
	switch table {
	case "events":
		return f.Events, f.EventsWave
	case "conversions":
		return f.Conv, f.ConvWave
	default:
		return "", ""
	}
}

// columnsOf lists physical columns of one wave on one table, in fields order.
// table is "events" or "conversions". w selects the generation.
// The slice is empty when that wave adds nothing to the table.
func columnsOf(table string, w wave) []string {
	var out []string
	for _, f := range fields {
		col, fw := f.tableCol(table)
		if col != "" && fw == w {
			out = append(out, col)
		}
	}
	return out
}

// expectedCount is how many columns columnsOf would return.
// table and w are the same as for columnsOf. It does not touch the database.
func expectedCount(table string, w wave) int {
	return len(columnsOf(table, w))
}

// alterSQL is the body of one ALTER: ADD COLUMN IF NOT EXISTS for every
// column of wave w on table, joined by commas. An empty wave returns "".
// The caller prefixes "ALTER TABLE "+table+" ".
func alterSQL(table string, w wave) string {
	var parts []string
	for _, f := range fields {
		col, fw := f.tableCol(table)
		if col == "" || fw != w {
			continue
		}
		part := "ADD COLUMN IF NOT EXISTS " + col + " " + f.Type
		if f.Default != "" {
			part += " DEFAULT " + f.Default
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ")
}

// convStub is the postbacks-side stand-in for one conversions column.
// f.Conv is the alias. Numeric and time columns get a typed zero;
// every other type gets an empty string. f.Conv must not be empty.
func convStub(f Field) string {
	switch f.Conv {
	case "origin_group_name":
		return "group_name AS origin_group_name"
	case "origin_group_id":
		return "'' AS origin_group_id"
	}
	switch f.Type {
	case "UInt32":
		return "toUInt32(0) AS " + f.Conv
	case "UInt8":
		return "toUInt8(0) AS " + f.Conv
	case "Int64":
		return "toInt64(0) AS " + f.Conv
	case "Float64":
		return "toFloat64(0) AS " + f.Conv
	case "DateTime":
		return "toDateTime(0) AS " + f.Conv
	default:
		return "'' AS " + f.Conv
	}
}

// FieldInfo is one field as the admin API returns it.
// Events and Conv are true when that table has the column.
// EventsWave and ConvWave are empty when the field has no column there.
type FieldInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Events     bool   `json:"events"`
	Conv       bool   `json:"conv"`
	Dim        bool   `json:"dim"`
	EventsWave string `json:"events_wave"`
	ConvWave   string `json:"conv_wave"`
}

// FieldsInfo is the field list plus which waves are actually on the server.
// Ready[table][wave] is true when every column of that wave is present.
type FieldsInfo struct {
	Fields []FieldInfo                `json:"fields"`
	Ready  map[string]map[string]bool `json:"ready"`
}

// StaticFields returns the field list without readiness.
// Internal columns are left out: the interface and the roles do not list them,
// while the server still accepts them in conditions.
// It takes nothing and does not touch the database. Used by tests and fakes.
func StaticFields() []FieldInfo {
	out := make([]FieldInfo, 0, len(fields))
	for _, f := range fields {
		if f.Internal {
			continue
		}
		out = append(out, FieldInfo{
			Name:       f.Name,
			Type:       f.Type,
			Events:     f.Events != "",
			Conv:       f.Conv != "",
			Dim:        f.Dim,
			EventsWave: string(f.EventsWave),
			ConvWave:   string(f.ConvWave),
		})
	}
	return out
}

// Fields returns the field list and which waves this connection has seen.
// Base is always ready on events and conversions. A nil receiver reports
// every later wave as not ready. It does not query.
func (c *CH) Fields() FieldsInfo {
	info := FieldsInfo{Fields: StaticFields(), Ready: map[string]map[string]bool{}}
	for _, table := range []string{"events", "conversions"} {
		info.Ready[table] = map[string]bool{}
		for _, w := range allWaves {
			info.Ready[table][string(w)] = c.ready(table, w)
		}
	}
	return info
}

// columnExpr is the SQL text of a field in a filter, breakdown or value list.
// f is the field. col is its physical column on the table being queried.
// Expr replaces the column. A non-string type is wrapped in toString so the
// value scans and compares as text. A string type is the column name.
// It does not fail.
func columnExpr(f Field, col string) string {
	if f.Expr != "" {
		return f.Expr
	}
	if !stringType(f.Type) {
		return "toString(" + col + ")"
	}
	return col
}

// dimColumn resolves a breakdown or values column on table.
// c carries readiness. table is "events" or "conversions". name is the API
// name, including the group alias. The string comes from columnExpr.
// ErrUnknownField means the name is unknown or has no column on table.
// ErrFieldNotReady means the column's wave is not on the table yet.
func dimColumn(c *CH, table, name string) (string, error) {
	f, ok := fieldByName(name)
	if !ok {
		return "", ErrUnknownField
	}
	col, w := f.tableCol(table)
	if col == "" {
		return "", ErrUnknownField
	}
	if !c.ready(table, w) {
		return "", ErrFieldNotReady
	}
	return columnExpr(f, col), nil
}
