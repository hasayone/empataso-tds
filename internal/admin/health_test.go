package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/geo"
)

// healthOf calls handleHealth on a server whose geo databases are dbs, or on
// one without a geo resolver when dbs is nil and noGeo is true.
func healthOf(t *testing.T, dbs []geo.Info, noGeo bool) (int, healthState) {
	t.Helper()
	s := &Server{}
	if !noGeo {
		s.cfg.Geo = geoInfoStub{dbs: dbs}
	}
	rec := httptest.NewRecorder()
	s.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	var st healthState
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, st
}

func TestHealthReportsTheCityDatabase(t *testing.T) {
	built := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	code, st := healthOf(t, []geo.Info{
		{Kind: geo.KindCity, Loaded: true, Built: built},
		{Kind: geo.KindASN, Loaded: false},
	}, false)
	if code != http.StatusOK || st.Status != "ok" {
		t.Fatalf("code %d, status %q", code, st.Status)
	}
	if !st.Geo.City.Loaded || st.Geo.City.Built != "2026-11-01T00:00:00Z" {
		t.Fatalf("city %+v", st.Geo.City)
	}
}

// TestHealthStaysOKWithoutTheCityDatabase checks that a missing database is
// reported, not turned into a failure of the container probe.
func TestHealthStaysOKWithoutTheCityDatabase(t *testing.T) {
	for name, tc := range map[string]struct {
		dbs   []geo.Info
		noGeo bool
	}{
		"not downloaded yet": {dbs: []geo.Info{{Kind: geo.KindCity}}},
		"only an ASN loaded": {dbs: []geo.Info{{Kind: geo.KindASN, Loaded: true}}},
		"no geo resolver":    {noGeo: true},
	} {
		code, st := healthOf(t, tc.dbs, tc.noGeo)
		if code != http.StatusOK || st.Status != "ok" || st.Geo.City.Loaded || st.Geo.City.Built != "" {
			t.Errorf("%s: code %d, state %+v", name, code, st)
		}
	}
}
