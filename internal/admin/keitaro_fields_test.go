package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/emtds/bridge/ktimport"
)

// TestKeitaroVersionConversionFields checks that a Keitaro 10 row and a Keitaro 11 row land in the same
// conversion fields: the dotted SubID stays whole, the status comes from status or from event_type,
// a custom type is kept, and a missing Rebill is not required.
func TestKeitaroVersionConversionFields(t *testing.T) {
	cases := []struct {
		name, raw, cid, status, original string
	}{
		{"keitaro 10", `{"sub_id":"1vepsc2qv8h","status":"Rebill","conversion_id":"v-1"}`, "1vepsc2qv8h", "rebill", "Rebill"},
		{"keitaro 11 lead", `{"sub_id":"2k5a13p.ac.2m74","event_id":"e-2","event_type":"lead"}`, "2k5a13p.ac.2m74", "lead", "lead"},
		{"keitaro 11 custom", `{"sub_id":"2k5a13p.ac.2m74","event_id":"e-3","event_type":" VIP_Sale "}`, "2k5a13p.ac.2m74", "vip_sale", " VIP_Sale "},
		{"status wins over event_type", `{"sub_id":"s","status":"sale","event_type":"custom"}`, "s", "sale", "sale"},
	}
	for _, tc := range cases {
		var row ktimport.ConversionRow
		if err := json.Unmarshal([]byte(tc.raw), &row); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := conversionInsert(row, "tracker.example", logPlace{}, nil)
		if got.CID != tc.cid || got.Status != tc.status || got.OriginalStatus != tc.original {
			t.Errorf("%s: cid %q status %q original %q", tc.name, got.CID, got.Status, got.OriginalStatus)
		}
	}
	if !oddConversionStatus(row11Status(t)) {
		t.Error("a custom Keitaro 11 type is counted as outside the built-in set, not dropped")
	}
}

// row11Status returns the effective status of a Keitaro 11 row with a custom type.
func row11Status(t *testing.T) string {
	t.Helper()
	var row ktimport.ConversionRow
	if err := json.Unmarshal([]byte(`{"sub_id":"a.b.c","event_id":"e","event_type":"vip_sale"}`), &row); err != nil {
		t.Fatal(err)
	}
	return row.EffectiveStatus()
}

// TestKeitaroVersionImportColumns imports Keitaro 11 conversions after a check found version 11 and checks the
// conversion log is asked for event_id and event_type, the rows reach the warehouse with their custom type and
// whole SubID, and an origin with no remembered version asks for no id column.
func TestKeitaroVersionImportColumns(t *testing.T) {
	for _, known := range []bool{true, false} {
		var mu sync.Mutex
		var asked []string
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path != "/admin_api/v1/conversions/log" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Offset  int      `json:"offset"`
				Columns []string `json:"columns"`
			}
			_ = json.Unmarshal(raw, &req)
			mu.Lock()
			asked = append(asked, strings.Join(req.Columns, ","))
			mu.Unlock()
			if req.Offset > 0 {
				_, _ = w.Write([]byte(`{"rows":[],"total":2}`))
				return
			}
			_, _ = w.Write([]byte(`{"rows":[` +
				`{"sub_id":"2k5a13p.ac.2m74","event_id":"e-1","event_type":"vip_sale","postback_datetime":"2026-10-02 03:04:05"},` +
				`{"sub_id":"2k5a13p.ad.9x01","event_id":"e-2","event_type":"lead","postback_datetime":"2026-10-02 03:04:06"}` +
				`],"total":2}`))
		}))
		t.Cleanup(fake.Close)
		sink := &fakeSink{}
		s, admin := newKeitaroServers(t, fake.URL, "fixture-key", nil, sink, nil)
		if known {
			s.ktVersion.set(strings.TrimRight(fake.URL, "/"), ktimport.Keitaro11)
		}
		postKeitaroImport(t, admin, map[string]any{"conversions": true, "from": "2026-10-01", "to": "2026-10-15"})
		job := waitKeitaroJob(t, admin)
		if job.Stage != "done" || job.Error != "" || job.Counts["conversions"] != 2 {
			t.Fatalf("known=%v job %+v", known, job)
		}
		mu.Lock()
		first := asked[0]
		mu.Unlock()
		hasID, hasType := strings.Contains(first, "event_id"), strings.Contains(first, "event_type")
		if hasID != known || hasType != known {
			t.Errorf("known=%v columns %s", known, first)
		}
		if len(sink.convs) != 2 || sink.convs[0].CID != "2k5a13p.ac.2m74" || sink.convs[0].Status != "vip_sale" || sink.convs[1].Status != "lead" {
			t.Errorf("known=%v rows %+v", known, sink.convs)
		}
	}
}

// TestKeitaroVersionHint checks that the remembered version belongs to one origin and that an undecided check forgets it.
func TestKeitaroVersionHint(t *testing.T) {
	var h keitaroVersionHint
	if h.get("https://a.example") != "" {
		t.Fatal("empty hint")
	}
	h.set("https://a.example", ktimport.Keitaro10)
	if h.get("https://a.example") != "10" || h.get("https://b.example") != "" {
		t.Fatal("hint belongs to one origin")
	}
	h.set("https://a.example", "")
	if h.get("https://a.example") != "" {
		t.Fatal("an undecided check must forget the old version")
	}
}
