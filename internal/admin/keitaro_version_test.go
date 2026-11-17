package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// keitaroVersionTracker is a tracker fixture for the version check. subID and statuses are the newest log
// records, accept lists the id columns the log takes, logCode is the status of every log answer when not 200.
// It records every request as "METHOD path" to prove that nothing but reads of the two logs and the campaign page goes out.
type keitaroVersionTracker struct {
	subID    string
	statuses []string
	accept   map[string]bool
	logCode  int

	mu    sync.Mutex
	calls []string
}

// serve starts the tracker and returns its origin; the server stops with the test.
func (f *keitaroVersionTracker) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/admin_api/v1/campaigns" {
			if r.URL.Query().Get("offset") == "0" {
				_, _ = w.Write([]byte(`[{"id":1}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if f.logCode != 0 {
			http.Error(w, "log down", f.logCode)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Columns []string `json:"columns"`
		}
		_ = json.Unmarshal(raw, &req)
		rows := []map[string]string{}
		switch {
		case len(req.Columns) == 2 && req.Columns[0] == "sub_id":
			if f.subID != "" {
				rows = append(rows, map[string]string{"sub_id": f.subID})
			}
		case len(req.Columns) == 2 && req.Columns[0] == "status":
			for _, s := range f.statuses {
				rows = append(rows, map[string]string{"status": s})
			}
		case len(req.Columns) == 1 && f.accept[req.Columns[0]]:
			rows = append(rows, map[string]string{req.Columns[0]: ""})
		default:
			http.Error(w, "unknown column", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": rows, "total": len(rows)})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// checkKeitaroVersion runs POST /api/keitaro/check against the tracker and returns the version fields of the answer.
func checkKeitaroVersion(t *testing.T, f *keitaroVersionTracker) (version string, signals []string, conflict bool, raw []byte) {
	t.Helper()
	admin := newKeitaroAdmin(t, f.serve(t), "fixture-key", nil, nil, nil)
	raw, code := keitaroPOST(t, admin, "/api/keitaro/check", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("check %d %s", code, raw)
	}
	var body struct {
		OK             bool   `json:"ok"`
		API            string `json:"api"`
		ProductVersion string `json:"product_version"`
		Signals        []struct {
			Signal  string `json:"signal"`
			Version string `json:"version"`
		} `json:"version_signals"`
		Conflict bool `json:"version_conflict"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || !body.OK || body.API != "v1" {
		t.Fatalf("check body %s: %v", raw, err)
	}
	for _, s := range body.Signals {
		signals = append(signals, s.Signal+"="+s.Version)
	}
	return body.ProductVersion, signals, body.Conflict, raw
}

// TestKeitaroVersionCheck checks the check answer for Keitaro 10, Keitaro 11, mixed signs and no signs.
func TestKeitaroVersionCheck(t *testing.T) {
	cases := []struct {
		name     string
		tracker  keitaroVersionTracker
		version  string
		signals  string
		conflict bool
	}{
		{"keitaro 10", keitaroVersionTracker{
			subID: "1vepsc2qv8h", statuses: []string{"lead", "rebill"},
			accept: map[string]bool{"click_id": true, "conversion_id": true},
		}, "10", "subid=10 rebill=10 click_id=10 conversion_id=10", false},
		{"keitaro 11", keitaroVersionTracker{
			subID: "2k5a13p.ac.2m74", statuses: []string{"vip"},
			accept: map[string]bool{"event_id": true, "event_type": true},
		}, "11", "subid=11 custom_type=11 event_id=11 event_type=11", false},
		{"mixed", keitaroVersionTracker{
			subID: "2k5a13p.ac.2m74", statuses: []string{"rebill"},
		}, "", "subid=11 rebill=10", true},
		{"empty", keitaroVersionTracker{statuses: []string{"lead"}}, "", "", false},
		{"logs down", keitaroVersionTracker{logCode: http.StatusInternalServerError}, "", "", false},
	}
	for i := range cases {
		tc := &cases[i]
		version, signals, conflict, raw := checkKeitaroVersion(t, &tc.tracker)
		if version != tc.version || strings.Join(signals, " ") != tc.signals || conflict != tc.conflict {
			t.Errorf("%s: version %q signals %v conflict %v", tc.name, version, signals, conflict)
		}
		if strings.Contains(string(raw), "fixture-key") || !strings.Contains(string(raw), `"version_signals":[`) {
			t.Errorf("%s: answer %s", tc.name, raw)
		}
	}
}

// TestKeitaroVersionCheckReadsOnly checks that the check sends only campaign GETs and POST reads of the two logs.
func TestKeitaroVersionCheckReadsOnly(t *testing.T) {
	f := keitaroVersionTracker{subID: "1vepsc2qv8h"}
	checkKeitaroVersion(t, &f)
	logs := 0
	for _, c := range f.calls {
		switch c {
		case "GET /admin_api/v1/campaigns":
		case "POST /admin_api/v1/clicks/log", "POST /admin_api/v1/conversions/log":
			logs++
		default:
			t.Errorf("unexpected request %s", c)
		}
	}
	if logs != 6 {
		t.Errorf("%d log reads, want 6: %v", logs, f.calls)
	}
}
