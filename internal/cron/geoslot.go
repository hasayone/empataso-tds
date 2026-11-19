package cron

import "errors"

// DBIPCityURL is the URL template of the DB-IP City Lite database: free, no
// account, CC BY 4.0. {YYYY-MM} is the month; see MonthPlaceholder.
const DBIPCityURL = "https://download.db-ip.com/free/dbip-city-lite-" + MonthPlaceholder + ".mmdb.gz"

// Classes of a geo source, as GeoSlot.Source reports them.
const (
	GeoSourceMaxMind = "maxmind" // download.maxmind.com, needs an account id and a license key
	GeoSourceDBIP    = "dbip"    // download.db-ip.com, public
	GeoSourceCustom  = "custom"  // any other host
)

// ErrGeoCitySourceSet is returned by UseDBIPCity when a city source that already
// works (a MaxMind one with a key, or a custom URL) is configured: switching would
// drop it.
var ErrGeoCitySourceSet = errors.New("a working city source is already configured")

// GeoSlot is how the config fills one geo database slot.
// Kind is KindCity, KindASN or KindDBIPASN. Source is the class of the first
// source that can download, or of the first source at all, or "" when no source
// has this kind. Ready is true when a geo_db run downloads something for the slot.
// NeedsKey is true when the slot has only MaxMind sources without a key.
type GeoSlot struct {
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Ready    bool   `json:"ready"`
	NeedsKey bool   `json:"needs_key"`
}

// geoSourceClass returns the class of s by the host of its URL.
func geoSourceClass(s GeoSource) string {
	switch hostOf(cleanURL(s.URL)) {
	case maxMindHost:
		return GeoSourceMaxMind
	case DBIPASNHost:
		return GeoSourceDBIP
	}
	return GeoSourceCustom
}

// geoSourceReady reports whether runGeoDB downloads s instead of skipping it:
// every source except a MaxMind one without an account id and a license key.
func geoSourceReady(s GeoSource) bool {
	return geoSourceClass(s) != GeoSourceMaxMind || s.User != "" || s.Password != ""
}

// GeoSlots returns the slot of each geo database kind, in the order city, asn,
// dbip_asn, as the sources of c fill them. It reads c only.
func (c Config) GeoSlots() []GeoSlot {
	out := make([]GeoSlot, 0, 3)
	for _, kind := range []string{KindCity, KindASN, KindDBIPASN} {
		slot := GeoSlot{Kind: kind}
		for _, s := range c.GeoDB.Sources {
			if s.Kind != kind {
				continue
			}
			if geoSourceReady(s) {
				slot.Source, slot.Ready, slot.NeedsKey = geoSourceClass(s), true, false
				break
			}
			if slot.Source == "" {
				slot.Source, slot.NeedsKey = geoSourceClass(s), true
			}
		}
		out = append(out, slot)
	}
	return out
}

// UseDBIPCity returns c with the city slot filled by DB-IP City Lite: MaxMind
// city sources without a key are dropped and the DB-IP source is added once.
// A config whose city slot already uses DB-IP comes back unchanged. The receiver
// is not modified. It returns ErrGeoCitySourceSet when a working city source of
// another class is configured.
func (c Config) UseDBIPCity() (Config, error) {
	kept := make([]GeoSource, 0, len(c.GeoDB.Sources)+1)
	hasDBIP := false
	for _, s := range c.GeoDB.Sources {
		if s.Kind != KindCity {
			kept = append(kept, s)
			continue
		}
		switch {
		case geoSourceClass(s) == GeoSourceDBIP:
			hasDBIP = true
			kept = append(kept, s)
		case geoSourceReady(s):
			return c, ErrGeoCitySourceSet
		}
	}
	if !hasDBIP {
		kept = append(kept, GeoSource{Kind: KindCity, URL: DBIPCityURL})
	}
	c.GeoDB.Sources = kept
	return c, nil
}
