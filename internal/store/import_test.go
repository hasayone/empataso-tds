package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

func TestConversionColumns(t *testing.T) {
	for _, click := range []bool{false, true} {
		cols, n := conversionColumns(click)
		names := splitColumns(cols)
		if n != len(names) {
			t.Fatalf("clickCols=%v: n=%d, columns=%d (%q)", click, n, len(names), cols)
		}
		for _, name := range names {
			if name == "" {
				t.Fatalf("clickCols=%v: empty column in %q", click, cols)
			}
		}
		if !hasColumn(names, "ts") || !hasColumn(names, "cid") || !hasColumn(names, "status") {
			t.Fatalf("clickCols=%v: missing ts, cid or status in %q", click, cols)
		}
		hasSub := hasColumn(names, "sub_id_12")
		hasNet := hasColumn(names, "network_id")
		if click && (!hasSub || !hasNet) {
			t.Fatalf("click columns missing sub_id_12 or network_id in %q", cols)
		}
		if !click && (hasSub || hasNet) {
			t.Fatalf("base columns include click columns: %q", cols)
		}
	}
}

func TestConversionValuesFollowColumns(t *testing.T) {
	row := ConversionInsert{
		Status:    "  Sale ",
		NetworkID: "net",
		Subs:      [config.MaxSubID]string{11: "s12"},
	}
	for _, click := range []bool{false, true} {
		_, n := conversionColumns(click)
		vals := conversionValues(row, click)
		if len(vals) != n {
			t.Fatalf("clickCols=%v: %d values, want %d", click, len(vals), n)
		}
		if vals[11] != "sale" {
			t.Fatalf("status %#v", vals[11])
		}
		ts, ok := vals[0].(time.Time)
		if !ok || ts.IsZero() || ts.Location() != time.UTC {
			t.Fatalf("zero TS must become UTC now, got %#v", vals[0])
		}
	}
	cols, n := conversionColumns(true)
	names := splitColumns(cols)
	vals := conversionValues(row, true)
	if names[n-1] != "sub_id_12" || vals[n-1] != "s12" {
		t.Fatalf("sub_id_12: column %q value %#v", names[n-1], vals[n-1])
	}
	netAt := -1
	for i, name := range names {
		if name == "network_id" {
			netAt = i
		}
	}
	if netAt < 0 || vals[netAt] != "net" {
		t.Fatalf("network_id at %d values %#v", netAt, vals)
	}
	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	row.TS = fixed
	vals = conversionValues(row, false)
	if got, ok := vals[0].(time.Time); !ok || !got.Equal(fixed) {
		t.Fatalf("explicit TS %#v", vals[0])
	}
}

func TestConversionColumnsExt(t *testing.T) {
	cols, n := conversionColumnsExt(true, true)
	names := splitColumns(cols)
	if n != len(names) {
		t.Fatalf("n=%d columns=%d (%q)", n, len(names), cols)
	}
	subAt, extAt := -1, -1
	extCols := columnsOf("conversions", waveExt)
	for i, name := range names {
		if name == "sub_id_12" {
			subAt = i
		}
		if name == extCols[0] {
			extAt = i
		}
	}
	if subAt < 0 || extAt <= subAt {
		t.Fatalf("ext columns must follow sub_id_12 in %q", cols)
	}
	if names[len(names)-1] != extCols[len(extCols)-1] {
		t.Fatalf("last column %q, want %q", names[len(names)-1], extCols[len(extCols)-1])
	}
	base, bn := conversionColumnsExt(false, true)
	baseNames := splitColumns(base)
	if bn != len(baseNames) || hasColumn(baseNames, "sub_id_12") || !hasColumn(baseNames, "click_ts") || !hasColumn(baseNames, "xrw") {
		t.Fatalf("ext without click: %q", base)
	}
	row := ConversionInsert{
		Status: "  Sale ", XRW: "XMLHttpRequest", Site: "land", TID: "T1",
		ClickTS: time.Unix(10, 0).UTC(),
	}
	vals := conversionValuesExt(row, true, true)
	if len(vals) != n {
		t.Fatalf("%d values, want %d", len(vals), n)
	}
	if vals[extAt] == nil {
		t.Fatal("first ext value missing")
	}
	xrwAt := -1
	for i, name := range names {
		if name == "xrw" {
			xrwAt = i
		}
	}
	if xrwAt < 0 || vals[xrwAt] != "XMLHttpRequest" {
		t.Fatalf("xrw at %d = %#v", xrwAt, vals)
	}
}

func TestNormalizeCHDateTime(t *testing.T) {
	if !normalizeCHDateTime(time.Unix(0, 0)).IsZero() {
		t.Fatal("epoch must become zero")
	}
	fixed := time.Date(2024, 1, 2, 3, 0, 0, 0, time.UTC)
	if !normalizeCHDateTime(fixed).Equal(fixed) {
		t.Fatal("real ts must stay")
	}
}

func TestInitialWatermarkEmptyDomain(t *testing.T) {
	c := &CH{}
	clicks, conv, err := c.InitialWatermark(context.Background(), "")
	if err != nil || !clicks.IsZero() || !conv.IsZero() {
		t.Fatalf("empty domain: clicks=%v conv=%v err=%v", clicks, conv, err)
	}
}

func TestInitialWatermarkNilClient(t *testing.T) {
	var c *CH
	if _, _, err := c.InitialWatermark(context.Background(), "host"); err == nil {
		t.Fatal("nil client must error")
	}
}

func TestExistingCIDsGuards(t *testing.T) {
	c := &CH{}
	from, to := time.Now().Add(-time.Hour), time.Now()
	out, err := c.ExistingCIDs(context.Background(), "", from, to, []string{"a"})
	if err != nil || len(out) != 0 {
		t.Fatalf("empty domain: %v %v", out, err)
	}
	out, err = c.ExistingCIDs(context.Background(), "host", from, to, nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("empty cids: %v %v", out, err)
	}
	var nilCH *CH
	if _, err := nilCH.ExistingCIDs(context.Background(), "host", from, to, []string{"a"}); err == nil {
		t.Fatal("nil client must error")
	}
}

func TestPostbackWhereExcludeDomain(t *testing.T) {
	where, args := postbackWhere(PostbackQuery{
		From: time.Unix(1, 0), To: time.Unix(2, 0), ExcludeDomain: "keitaro.local",
	}, false)
	if !strings.Contains(where, "domain != ?") {
		t.Fatalf("where = %q", where)
	}
	if len(args) != 3 || args[2] != "keitaro.local" {
		t.Fatalf("args = %v", args)
	}
}

func TestPurgeImportedEmptyDomain(t *testing.T) {
	c := &CH{}
	if err := c.PurgeImported(context.Background(), ""); err == nil {
		t.Fatal("empty domain must return an error")
	}
	if err := c.PurgeImported(context.Background(), "   "); err == nil {
		t.Fatal("blank domain must return an error")
	}
}

func TestInsertConversionsEmpty(t *testing.T) {
	c := &CH{}
	if err := c.InsertConversions(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertConversions(context.Background(), []ConversionInsert{}); err != nil {
		t.Fatal(err)
	}
}

func TestInsertConversionsNilReceiver(t *testing.T) {
	var c *CH
	if err := c.InsertConversions(context.Background(), []ConversionInsert{{CID: "x"}}); err == nil {
		t.Fatal("nil receiver must return an error")
	}
}

// TestConversionExtValueFilled checks every ext column yields a non-zero value.
// t is the test handle. It does not touch ClickHouse and does not fail the process.
func TestConversionExtValueFilled(t *testing.T) {
	row := ConversionInsert{
		GroupID:        "group_id",
		ClickTS:        time.Unix(100, 0).UTC(),
		OriginalStatus: "original_status",
		PrevStatus:     "prev_status",
		TID:            "tid",
		IP:             "ip",
		Region:         "region",
		Lang:           "lang",
		OS:             "os",
		OSVersion:      "os_version",
		Browser:        "browser",
		BrowserV:       "browser_ver",
		Brand:          "brand",
		UserAgent:      "useragent",
		Referer:        "referer",
		SE:             "se",
		Keyword:        "keyword",
		ASN:            42,
		Org:            "org",
		Site:           "site",
		AdCampaignID:   "ad_campaign_id",
		CreativeID:     "creative_id",
		ExternalID:     "external_id",
		XRW:            "xrw",
	}
	for _, col := range columnsOf("conversions", waveExt) {
		v := conversionExtValue(row, col)
		switch x := v.(type) {
		case string:
			if x == "" {
				t.Errorf("%s: empty", col)
			}
		case uint32:
			if x == 0 {
				t.Errorf("%s: zero", col)
			}
		case time.Time:
			if x.IsZero() || x.Unix() <= 0 {
				t.Errorf("%s: zero time %v", col, x)
			}
		default:
			t.Errorf("%s: unexpected %T %#v", col, v, v)
		}
	}
}

func splitColumns(cols string) []string {
	raw := strings.Split(cols, ",")
	out := make([]string, len(raw))
	for i, p := range raw {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

func hasColumn(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
