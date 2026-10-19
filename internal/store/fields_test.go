package store

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestFieldsContract(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range fields {
		if f.Name == "" || seen[f.Name] {
			t.Fatalf("duplicate or empty name %q", f.Name)
		}
		seen[f.Name] = true
		if f.Type == "" {
			t.Fatalf("%s: empty type", f.Name)
		}
		if f.Events == "" && f.Conv == "" {
			t.Fatalf("%s: no column", f.Name)
		}
	}
	if expectedCount("events", waveExt) != 5 {
		t.Fatalf("events ext = %d, want 5", expectedCount("events", waveExt))
	}
	// The contract summary says 25, but the named list has 24 columns.
	if expectedCount("conversions", waveExt) != 24 {
		t.Fatalf("conversions ext = %d, want 24", expectedCount("conversions", waveExt))
	}
	if expectedCount("events", waveClick) != 16 {
		t.Fatalf("events click = %d, want 16", expectedCount("events", waveClick))
	}
	raw, err := os.ReadFile("../../migrations/clickhouse/006_log_columns.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := "ALTER TABLE events " + alterSQL("events", waveExt) + " ALTER TABLE conversions " + alterSQL("conversions", waveExt)
	if normSQL(string(raw)) != normSQL(want) {
		t.Fatalf("migration\n%s\nwant\n%s", normSQL(string(raw)), normSQL(want))
	}
}

func TestExtJSONTags(t *testing.T) {
	pb := jsonTagSet(PostbackRow{})
	lg := jsonTagSet(LogRow{})
	for _, f := range fields {
		if f.ConvWave != waveExt || f.Conv == "" {
			continue
		}
		tag := f.Name
		if f.Conv == "group_id" {
			tag = "campaign_id"
		}
		if !pb[tag] {
			t.Errorf("PostbackRow missing json %q for %s", tag, f.Name)
		}
	}
	for _, f := range fields {
		if f.EventsWave != waveExt || f.Events == "" {
			continue
		}
		if !lg[f.Name] {
			t.Errorf("LogRow missing json %q", f.Name)
		}
	}
}

// TestFieldDimFlags checks which journal fields are breakdown dimensions.
// t is the test handle. It reads the field list only.
// It does not touch ClickHouse and does not fail the process.
func TestFieldDimFlags(t *testing.T) {
	for _, name := range []string{"sub_id_1", "sub_id_12", "ad_campaign_id", "site"} {
		f, ok := fieldByName(name)
		if !ok || !f.Dim {
			t.Fatalf("%s: ok=%v dim=%v", name, ok, f.Dim)
		}
	}
	for _, name := range []string{"ip", "cid", "tid", "useragent"} {
		f, ok := fieldByName(name)
		if !ok || f.Dim {
			t.Fatalf("%s: ok=%v dim=%v", name, ok, f.Dim)
		}
	}
}

func TestDimColumn(t *testing.T) {
	c := &CH{readyFlags: map[string]map[wave]bool{
		"events": {waveBase: true, waveNet: true},
	}}
	col, err := dimColumn(c, "events", "group")
	if err != nil || col != "group_name" {
		t.Fatalf("group: %q %v", col, err)
	}
	col, err = dimColumn(c, "events", "asn")
	if err != nil || col != "if(asn = 0, '', concat('AS', toString(asn)))" {
		t.Fatalf("asn: %q %v", col, err)
	}
	if _, err = dimColumn(c, "events", "nope"); !errors.Is(err, ErrUnknownField) {
		t.Fatalf("unknown: %v", err)
	}
	c.readyFlags["events"][waveNet] = false
	if _, err = dimColumn(c, "events", "timezone"); !errors.Is(err, ErrFieldNotReady) {
		t.Fatalf("not ready: %v", err)
	}
	col, err = dimColumn(&CH{}, "events", "device")
	if err != nil || col != "device" {
		t.Fatalf("base device: %q %v", col, err)
	}
	if !(&CH{}).Fields().Ready["events"]["base"] || !(&CH{}).Fields().Ready["conversions"]["base"] {
		t.Fatal("base wave must be ready without a probe")
	}
}

// TestDimColumnNonString checks toString for a non-string dim and the asn expression.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestDimColumnNonString(t *testing.T) {
	c := &CH{readyFlags: map[string]map[wave]bool{
		"events": {waveNet: true},
	}}
	col, err := dimColumn(c, "events", "uniq")
	if err != nil || col != "toString(uniq)" {
		t.Fatalf("uniq: %q %v", col, err)
	}
	col, err = dimColumn(c, "events", "country")
	if err != nil || col != "country" {
		t.Fatalf("country: %q %v", col, err)
	}
	col, err = dimColumn(c, "events", "asn")
	want := "if(asn = 0, '', concat('AS', toString(asn)))"
	if err != nil || col != want {
		t.Fatalf("asn: %q %v", col, err)
	}
}

func normSQL(s string) string {
	s = strings.ReplaceAll(s, "kuztds.", "")
	s = strings.ReplaceAll(s, ";", "")
	return strings.Join(strings.Fields(s), " ")
}

// migrationSQLBody returns executable SQL from a migration file text: trimmed
// non-empty lines that are not line comments (--). raw is the full file
// contents. The result is one space-separated string for normSQL comparison.
func migrationSQLBody(raw string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, " ")
}

func jsonTagSet(v any) map[string]bool {
	t := reflect.TypeOf(v)
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}
