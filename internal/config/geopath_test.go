package config

import (
	"path/filepath"
	"testing"
)

func TestDBIPASNPath(t *testing.T) {
	unsetAll := func(t *testing.T) {
		for _, k := range []string{"EMTDS_DBIP_ASN_DB", "EMTDS_ASN_DB", "EMTDS_GEO_DB"} {
			t.Setenv(k, "")
		}
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, ""},
		{"explicit path wins", map[string]string{
			"EMTDS_DBIP_ASN_DB": filepath.Join("x", "own.mmdb"),
			"EMTDS_ASN_DB":      filepath.Join("a", "asn.mmdb"),
			"EMTDS_GEO_DB":      filepath.Join("g", "city.mmdb"),
		}, filepath.Join("x", "own.mmdb")},
		{"next to the asn database", map[string]string{
			"EMTDS_ASN_DB": filepath.Join("a", "asn.mmdb"),
			"EMTDS_GEO_DB": filepath.Join("g", "city.mmdb"),
		}, filepath.Join("a", "dbip-asn.mmdb")},
		{"next to the city database", map[string]string{
			"EMTDS_GEO_DB": filepath.Join("g", "city.mmdb"),
		}, filepath.Join("g", "dbip-asn.mmdb")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unsetAll(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if got := DBIPASNPath(); got != c.want {
				t.Fatalf("DBIPASNPath = %q, want %q", got, c.want)
			}
		})
	}
}

// TestDBIPASNPathIgnoresRetiredName checks that a database path under the retired
// variable prefix gives no DB-IP path.
func TestDBIPASNPathIgnoresRetiredName(t *testing.T) {
	t.Setenv("EMTDS_DBIP_ASN_DB", "")
	t.Setenv("EMTDS_ASN_DB", "")
	t.Setenv("EMTDS_GEO_DB", "")
	t.Setenv(retiredEnvName("ASN_DB"), filepath.Join("old", "asn.mmdb"))
	if got := DBIPASNPath(); got != "" {
		t.Fatalf("DBIPASNPath = %q, want empty", got)
	}
}

func TestGeoFilePaths(t *testing.T) {
	for _, k := range []string{"DBIP_ASN_DB", "ASN_DB", "GEO_DB"} {
		t.Setenv("EMTDS_"+k, "")
	}
	if got := GeoFilePaths(); got != (GeoFiles{}) {
		t.Fatalf("nothing set: %+v", got)
	}
	city, asn := filepath.Join("g", "city.mmdb"), filepath.Join("a", "asn.mmdb")
	t.Setenv("EMTDS_GEO_DB", city)
	if got := GeoFilePaths(); got != (GeoFiles{City: city, DBIPASN: filepath.Join("g", "dbip-asn.mmdb")}) {
		t.Fatalf("city only: %+v", got)
	}
	t.Setenv("EMTDS_ASN_DB", asn)
	own := filepath.Join("x", "own.mmdb")
	t.Setenv("EMTDS_DBIP_ASN_DB", own)
	if got := GeoFilePaths(); got != (GeoFiles{City: city, ASN: asn, DBIPASN: own}) {
		t.Fatalf("all set: %+v", got)
	}
	if GeoFilePaths().DBIPASN != DBIPASNPath() {
		t.Fatal("the DB-IP path must be the one DBIPASNPath gives")
	}
}
