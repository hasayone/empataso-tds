package geo

import (
	"net/netip"
	"path/filepath"
	"testing"
)

const (
	testCity = "testdata/GeoLite2-City-Test.mmdb"
	testASN  = "testdata/GeoLite2-ASN-Test.mmdb"
)

// mergeASN: DB-IP wins, MaxMind fills a DB-IP miss and supplies the
// alternative number and organization spelling.
func TestMergeASN(t *testing.T) {
	cases := []struct {
		name        string
		dbip, mm    asnRecord
		asn, asnAlt uint32
		org, orgAlt string
	}{
		{"both miss", asnRecord{}, asnRecord{}, 0, 0, Empty, ""},
		{"dbip miss, maxmind hit", asnRecord{}, asnRecord{15169, "GOOGLE"}, 15169, 0, "GOOGLE", ""},
		{"dbip hit only", asnRecord{15169, "Google LLC"}, asnRecord{}, 15169, 0, "Google LLC", ""},
		{"different spelling", asnRecord{15169, "Google LLC"}, asnRecord{15169, "GOOGLE"}, 15169, 0, "Google LLC", "GOOGLE"},
		{"same spelling other case", asnRecord{15169, "Google LLC"}, asnRecord{15169, "GOOGLE LLC"}, 15169, 0, "Google LLC", ""},
		{"maxmind org empty", asnRecord{15169, "Google LLC"}, asnRecord{15169, "  "}, 15169, 0, "Google LLC", ""},
		{"dbip org empty", asnRecord{15169, ""}, asnRecord{15169, "GOOGLE"}, 15169, 0, Empty, "GOOGLE"},
		{"dbip wins on number", asnRecord{16509, "Amazon.com, Inc."}, asnRecord{14618, "AMAZON-AES"}, 16509, 14618, "Amazon.com, Inc.", "AMAZON-AES"},
		{"same number, same name", asnRecord{1221, "Telstra"}, asnRecord{1221, "TELSTRA"}, 1221, 0, "Telstra", ""},
	}
	for _, c := range cases {
		asn, asnAlt, org, orgAlt := mergeASN(c.dbip, c.mm)
		if asn != c.asn || asnAlt != c.asnAlt || org != c.org || orgAlt != c.orgAlt {
			t.Errorf("%s: got (%d, %d, %q, %q), want (%d, %d, %q, %q)",
				c.name, asn, asnAlt, org, orgAlt, c.asn, c.asnAlt, c.org, c.orgAlt)
		}
	}
}

// DB-IP ASN alone is a valid setup.
func TestDBIPOnly(t *testing.T) {
	d, err := OpenWith(Paths{DBIPASN: testASN}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Resolve(netip.MustParseAddr("1.128.0.1"))
	if g.ASN != 1221 || g.Org != "Telstra Pty Ltd" || g.OrgAlt != "" {
		t.Errorf("dbip-only → %+v", g)
	}
	if g := d.Resolve(netip.MustParseAddr("203.0.113.1")); g.ASN != 0 || g.Org != Empty || g.OrgAlt != "" {
		t.Errorf("miss → %+v", g)
	}
}

// The same organization in both ASN databases leaves OrgAlt empty.
func TestDBIPAndMaxMindSameName(t *testing.T) {
	d, err := OpenWith(Paths{City: testCity, ASN: testASN, DBIPASN: testASN}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Resolve(netip.MustParseAddr("1.128.0.1"))
	if g.ASN != 1221 || g.Org != "Telstra Pty Ltd" || g.OrgAlt != "" {
		t.Errorf("asn = %+v", g)
	}
	if g := d.Resolve(netip.MustParseAddr("81.2.69.142")); g.Country != "gb" || g.ASN != 0 {
		t.Errorf("city-only address → %+v", g)
	}
}

// A City database read as an ASN database never hits (no ASN fields), so it
// stands in for a DB-IP database that misses: MaxMind answers alone.
func TestDBIPMissFallsBackToMaxMind(t *testing.T) {
	d, err := OpenWith(Paths{ASN: testASN, DBIPASN: testCity}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := d.Resolve(netip.MustParseAddr("1.128.0.1"))
	if g.ASN != 1221 || g.Org != "Telstra Pty Ltd" || g.OrgAlt != "" {
		t.Errorf("fallback → %+v", g)
	}
}

// Databases lists all three sources; a missing DB-IP file is not an error
// while another database is loaded.
func TestOpenWithDatabases(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "dbip.mmdb")
	d, err := OpenWith(Paths{City: testCity, ASN: testASN, DBIPASN: missing}, nil)
	if err != nil {
		t.Fatalf("DB-IP not downloaded yet: %v", err)
	}
	info := d.Databases()
	if len(info) != 3 || !info[0].Loaded || !info[1].Loaded || info[2].Loaded || info[2].Path != missing {
		t.Errorf("Databases() = %+v", info)
	}
	if _, err := OpenWith(Paths{DBIPASN: missing}, nil); err == nil {
		t.Error("a missing DB-IP database with nothing else must be an error")
	}
	if _, err := OpenWith(Paths{}, nil); err != nil {
		t.Errorf("no databases configured: %v", err)
	}
}

// None carries no alternative organization.
func TestNoneOrgAlt(t *testing.T) {
	if g := None(); g.OrgAlt != "" || g.ASNAlt != 0 {
		t.Errorf("None() alternatives = %d %q", g.ASNAlt, g.OrgAlt)
	}
}
