package config

import "path/filepath"

// DBIPASNFileName is the file name of the DB-IP ASN database when its path is
// derived from the location of another geo database.
const DBIPASNFileName = "dbip-asn.mmdb"

// GeoFiles holds the paths of the three geo databases. An empty field means
// the path is not configured.
type GeoFiles struct {
	City    string // EMTDS_GEO_DB
	ASN     string // EMTDS_ASN_DB
	DBIPASN string // see DBIPASNPath
}

// GeoFilePaths returns the geo database paths from the environment. Every
// process that needs them reads them here, so they cannot disagree.
func GeoFilePaths() GeoFiles {
	return GeoFiles{
		City:    Env("EMTDS_GEO_DB"),
		ASN:     Env("EMTDS_ASN_DB"),
		DBIPASN: DBIPASNPath(),
	}
}

// DBIPASNPath returns where the DB-IP ASN database lives.
// EMTDS_DBIP_ASN_DB wins. Without it the file sits next to the MaxMind ASN
// database (EMTDS_ASN_DB) or, failing that, next to the city database
// (EMTDS_GEO_DB). It returns "" when none of the three is set.
func DBIPASNPath() string {
	if p := Env("EMTDS_DBIP_ASN_DB"); p != "" {
		return p
	}
	for _, key := range []string{"EMTDS_ASN_DB", "EMTDS_GEO_DB"} {
		if p := Env(key); p != "" {
			return filepath.Join(filepath.Dir(p), DBIPASNFileName)
		}
	}
	return ""
}
