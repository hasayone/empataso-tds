package cron

import (
	"errors"
	"testing"
)

// TestGeoSlotsDefault checks that a fresh config asks for a MaxMind key for asn
// only, takes the city database from DB-IP City Lite without one and is ready
// for the DB-IP ASN database.
func TestGeoSlotsDefault(t *testing.T) {
	slots := Default().GeoSlots()
	if len(slots) != 3 || slots[0].Kind != KindCity || slots[1].Kind != KindASN || slots[2].Kind != KindDBIPASN {
		t.Fatalf("slots %+v", slots)
	}
	if c := slots[0]; !c.Ready || c.NeedsKey || c.Source != GeoSourceDBIP {
		t.Fatalf("city: %+v, want DB-IP City Lite in place of the keyless MaxMind source", c)
	}
	if s := slots[1]; s.Ready || !s.NeedsKey || s.Source != GeoSourceMaxMind {
		t.Fatalf("asn: %+v, want a MaxMind slot that needs a key", s)
	}
	if d := slots[2]; !d.Ready || d.NeedsKey || d.Source != GeoSourceDBIP {
		t.Fatalf("dbip_asn: %+v", d)
	}
}

// TestGeoSlotsKeyedMaxMind checks that a MaxMind source with a key makes the slot ready.
func TestGeoSlotsKeyedMaxMind(t *testing.T) {
	c := Default()
	c.GeoDB.Sources[0].User, c.GeoDB.Sources[0].Password = "123", "key"
	if s := c.GeoSlots()[0]; !s.Ready || s.NeedsKey || s.Source != GeoSourceMaxMind {
		t.Fatalf("city %+v", s)
	}
}

// TestGeoSlotsEmpty checks a slot without any source.
func TestGeoSlotsEmpty(t *testing.T) {
	var c Config
	for _, s := range c.GeoSlots() {
		if s.Ready || s.NeedsKey || s.Source != "" {
			t.Fatalf("%+v", s)
		}
	}
}

// TestUseDBIPCityReplacesKeylessMaxMind checks the switch to DB-IP City Lite:
// the keyless MaxMind city source goes, the DB-IP one comes once, other kinds stay,
// the receiver is not changed and the result validates.
func TestUseDBIPCityReplacesKeylessMaxMind(t *testing.T) {
	c := Default()
	before := len(c.GeoDB.Sources)
	got, err := c.UseDBIPCity()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.GeoDB.Sources) != before || c.GeoDB.Sources[0].Kind != KindCity || geoSourceClass(c.GeoDB.Sources[0]) != GeoSourceMaxMind {
		t.Fatal("the receiver must not change")
	}
	city := 0
	for _, s := range got.GeoDB.Sources {
		if s.Kind == KindCity {
			city++
			if s.URL != DBIPCityURL || s.User != "" || s.Password != "" {
				t.Fatalf("city source %+v", s)
			}
		}
	}
	if city != 1 || len(got.GeoDB.Sources) != before {
		t.Fatalf("sources %+v", got.GeoDB.Sources)
	}
	if s := got.GeoSlots()[0]; !s.Ready || s.Source != GeoSourceDBIP {
		t.Fatalf("city slot %+v", s)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	again, err := got.UseDBIPCity()
	if err != nil || len(again.GeoDB.Sources) != len(got.GeoDB.Sources) {
		t.Fatalf("a second switch must change nothing: %v %+v", err, again.GeoDB.Sources)
	}
}

// TestUseDBIPCityKeepsWorkingSource checks that a keyed MaxMind city source is not dropped.
func TestUseDBIPCityKeepsWorkingSource(t *testing.T) {
	c := Default()
	c.GeoDB.Sources[0].Password = "key"
	if _, err := c.UseDBIPCity(); !errors.Is(err, ErrGeoCitySourceSet) {
		t.Fatalf("err %v, want ErrGeoCitySourceSet", err)
	}
}

// TestDBIPCityDatabaseFillsCitySlot checks that the DB-IP City Lite type passes the city check.
func TestDBIPCityDatabaseFillsCitySlot(t *testing.T) {
	if !kindMatches(KindCity, "DBIP-City-Lite (compat=GeoLite2-City)") {
		t.Fatal("DB-IP City Lite must fill the city slot")
	}
}
