// Package geo determines what an IP address tells about a visitor: country,
// city, region, time zone, and the network it belongs to (ASN + organization).
//
// The resolver is abstracted by the Resolver interface: the production
// implementation is DB, and Nop is used when no database is configured.
// DB reads up to three MaxMind-format .mmdb files: a City or Country
// database (GeoLite2/GeoIP2) for location, MaxMind GeoLite2-ASN and DB-IP
// ASN Lite for the network. Any of them may be missing. When both ASN
// databases know an address, DB-IP wins (filters migrated from Keitaro use
// its names and numbers) and MaxMind's answer is kept in Geo.ASNAlt and
// Geo.OrgAlt. Values are normalized: lower case, missing = "-".
package geo

import (
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // zone names must resolve in an image that ships no zoneinfo
)

// Empty — the "no data" value.
const Empty = "-"

// Geo — the result of a lookup.
type Geo struct {
	Country string // country ISO code, lower-case, or "-"
	City    string // English city name, lower-case, or "-"
	Region  string // region ISO code, lower-case, or "-"

	// Timezone is the IANA name as the database spells it ("Europe/Moscow"),
	// or "-". UTCOffset turns it into the "+3" / "+5:30" form filters use.
	Timezone string

	// ASN is the autonomous system number, 0 when unknown. Org is its
	// organization as the database spells it ("Google LLC"), or "-".
	ASN uint32
	Org string

	// ASNAlt is the number the secondary ASN database gives the same address
	// when it differs from ASN (MaxMind versus DB-IP), or 0.
	ASNAlt uint32

	// OrgAlt is the same organization as the secondary ASN database spells
	// it ("GOOGLE" from MaxMind while Org came from DB-IP as "Google LLC").
	// Empty when there is no second spelling or it equals Org.
	OrgAlt string
}

// None is the answer for "nothing known about this address".
func None() Geo {
	return Geo{Country: Empty, City: Empty, Region: Empty, Timezone: Empty, Org: Empty}
}

// Resolver determines geo data by IP. The implementation must be thread-safe.
type Resolver interface {
	Resolve(ip netip.Addr) Geo
}

// Nop — a stub resolver: always returns empty values.
// Used when no .mmdb is set (geo filtering simply won't trigger).
type Nop struct{}

func (Nop) Resolve(netip.Addr) Geo { return None() }

// norm lower-cases the value; empty → Empty.
func norm(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return Empty
	}
	return strings.ToLower(s)
}

// keep trims the value and keeps its case; empty → Empty.
func keep(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return Empty
	}
	return s
}

// record — the subset of the GeoLite2/GeoIP2 City schema the engine needs.
// A Country database carries the same "country" field and nothing else, so
// the same struct reads it and the rest stays empty.
type record struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	City struct {
		Names names `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		ISOCode string `maxminddb:"iso_code"`
		Names   names  `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	Location struct {
		TimeZone string `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

// names picks the English name out of a localized-names map. Decoding into a
// struct reads that one entry; decoding into a map would allocate the map and
// a string for every language of every lookup.
type names struct {
	En string `maxminddb:"en"`
}

// asnRecord — the GeoLite2-ASN schema (DB-IP's ASN Lite uses the same names).
type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// mergeASN combines the answers of the DB-IP and MaxMind ASN databases.
// A record with Number == 0 is a miss (the reader reports no error then).
// DB-IP wins when it knows the address: filters migrated from Keitaro are
// written with DB-IP organization names ("Google LLC") while MaxMind says
// "GOOGLE". When MaxMind also knows the address and spells the organization
// differently, that spelling is returned as orgAlt; when it numbers the
// network differently, that number is returned as asnAlt (0 when equal or
// unknown). Both alternatives let filters written for MaxMind keep matching.
// With a DB-IP miss MaxMind is the answer and both alternatives are empty.
// A miss in both gives (0, 0, Empty, "").
func mergeASN(dbip, mm asnRecord) (asn, asnAlt uint32, org, orgAlt string) {
	if dbip.Number == 0 {
		return mm.Number, 0, keep(mm.Org), ""
	}
	org = keep(dbip.Org)
	if mm.Number != 0 {
		if mm.Number != dbip.Number {
			asnAlt = mm.Number
		}
		if o := strings.TrimSpace(mm.Org); o != "" && !strings.EqualFold(o, org) {
			orgAlt = o
		}
	}
	return dbip.Number, asnAlt, org, orgAlt
}

// fromRecord converts an mmdb record into Geo (pure function, tested without a DB).
func fromRecord(r record) Geo {
	region := ""
	if n := len(r.Subdivisions); n > 0 {
		// The last subdivision is the most specific. Databases without ISO
		// codes for subdivisions (DB-IP Lite) still name them.
		if region = r.Subdivisions[n-1].ISOCode; region == "" {
			region = r.Subdivisions[n-1].Names.En
		}
	}
	return Geo{
		Country:  norm(r.Country.ISOCode),
		City:     norm(r.City.Names.En),
		Region:   norm(region),
		Timezone: keep(r.Location.TimeZone),
		Org:      Empty,
	}
}

// locations caches parsed time zones: time.LoadLocation re-reads and re-parses
// the zone file on every call, and the set of zones is small and fixed.
var locations sync.Map // string -> *time.Location (nil when the name is unknown)

func location(name string) *time.Location {
	if v, ok := locations.Load(name); ok {
		loc, _ := v.(*time.Location)
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = nil
	}
	locations.Store(name, loc)
	return loc
}

// UTCOffset returns the visitor's offset from UTC at the given moment in the
// form filters and macros use: "+3", "-8", "+5:30", "+0". Empty when the time
// zone is unknown. The moment matters: the same zone is "+1" in winter and
// "+2" in summer.
func (g Geo) UTCOffset(now time.Time) string {
	if g.Timezone == "" || g.Timezone == Empty {
		return Empty
	}
	loc := location(g.Timezone)
	if loc == nil {
		return Empty
	}
	_, sec := now.In(loc).Zone()
	return formatOffset(sec)
}

func formatOffset(sec int) string {
	sign := "+"
	if sec < 0 {
		sign, sec = "-", -sec
	}
	h, m := sec/3600, sec%3600/60
	if m == 0 {
		return sign + strconv.Itoa(h)
	}
	return sign + strconv.Itoa(h) + ":" + twoDigits(m)
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// NormOffset brings an operator-written offset to the canonical form of
// UTCOffset: "3", "+3", "+03", "+03:00", "UTC+3", "GMT+3" all become "+3";
// "5:30" and "+0530" become "+5:30". ok is false when s is not an offset.
func NormOffset(s string) (string, bool) {
	s = strings.ReplaceAll(s, "\u2212", "-") // a typographic minus
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "UTC"), "GMT")
	s = strings.TrimSpace(s)
	if s == "" || s == "Z" {
		return "+0", true
	}
	sign := 1
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		sign, s = -1, s[1:]
	}
	hs, ms, hasColon := strings.Cut(s, ":")
	if !hasColon && len(hs) == 4 { // "0530"
		hs, ms = hs[:2], hs[2:]
	}
	h, err := strconv.Atoi(hs)
	if err != nil || h < 0 || h > 14 {
		return "", false
	}
	m := 0
	if ms != "" {
		if m, err = strconv.Atoi(ms); err != nil || m < 0 || m > 59 {
			return "", false
		}
	}
	return formatOffset(sign * (h*3600 + m*60)), true
}
