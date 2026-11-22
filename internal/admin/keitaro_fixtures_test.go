package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/logbuf"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

type captureLog struct {
	msgs []string
	args [][]any
}

func (c *captureLog) Warn(string, ...any) {}

func (c *captureLog) Info(msg string, args ...any) {
	c.msgs = append(c.msgs, msg)
	c.args = append(c.args, args)
}

type fakeSink struct {
	convs   []store.ConversionInsert
	events  []logbuf.Event
	purged  []string
	eventsN int64
	convN   int64
	wmClick time.Time
	wmConv  time.Time
}

func (f *fakeSink) InsertEvents(_ context.Context, rows []logbuf.Event) error {
	f.events = append(f.events, append([]logbuf.Event(nil), rows...)...)
	return nil
}

func (f *fakeSink) InsertConversions(_ context.Context, rows []store.ConversionInsert) error {
	f.convs = append(f.convs, append([]store.ConversionInsert(nil), rows...)...)
	return nil
}

func (f *fakeSink) PurgeImported(_ context.Context, domain string) error {
	f.purged = append(f.purged, domain)
	return nil
}

func (f *fakeSink) CountByDomain(context.Context, string) (int64, int64, error) {
	return f.eventsN, f.convN, nil
}

func (f *fakeSink) InitialWatermark(context.Context, string) (time.Time, time.Time, error) {
	return f.wmClick, f.wmConv, nil
}

func (f *fakeSink) ExistingCIDs(_ context.Context, domain string, from, to time.Time, cids []string) (map[string]struct{}, error) {
	want := map[string]struct{}{}
	for _, cid := range cids {
		want[cid] = struct{}{}
	}
	out := map[string]struct{}{}
	for _, ev := range f.events {
		if ev.Domain != domain || ev.CID == "" {
			continue
		}
		if _, ok := want[ev.CID]; !ok {
			continue
		}
		if ev.Ts.Before(from) || ev.Ts.After(to) {
			continue
		}
		out[ev.CID] = struct{}{}
	}
	return out, nil
}

type keitaroFiles struct {
	dir     string
	offers  *JSONStore[config.Offer]
	log     *captureLog
	jobFile string
}

type keitaroJobBody struct {
	Running     bool           `json:"running"`
	SyncRunning bool           `json:"sync_running"`
	Stage       string         `json:"stage"`
	Done        int            `json:"done"`
	Total       int            `json:"total"`
	Counts      map[string]int `json:"counts"`
	Losses      []struct {
		Kind   string `json:"kind"`
		Ref    string `json:"ref"`
		Detail string `json:"detail"`
	} `json:"losses"`
	Error      string `json:"error"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

func newKeitaroAdmin(t *testing.T, base, key string, groups *memGroups, wh ImportSink, files *keitaroFiles) *httptest.Server {
	t.Helper()
	_, srv := newKeitaroServers(t, base, key, groups, wh, files)
	return srv
}

func newKeitaroServers(t *testing.T, base, key string, groups *memGroups, wh ImportSink, files *keitaroFiles) (*Server, *httptest.Server) {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	if groups == nil {
		groups = &memGroups{}
	}
	dir := t.TempDir()
	var offers *JSONStore[config.Offer]
	var log slogLogger = noopLog{}
	jobFile := ""
	if files != nil {
		jobFile = files.jobFile
		if files.dir != "" {
			dir = files.dir
		}
		offers = files.offers
		if files.log != nil {
			log = files.log
		}
	}
	if offers == nil {
		offers = NewJSONStore[config.Offer](filepath.Join(dir, "offers.json"))
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: fakeStats{},
		Groups: groups, Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
		Offers:      offers,
		Landings:    NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:     NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks:    NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		Domains:     NewJSONStore[config.Domain](filepath.Join(dir, "domains.json")),
		Manifest:    NewFileManifest(filepath.Join(dir, "keitaro-import.json")),
		KeitaroBase: base,
		KeitaroKey:  key,
		UsersFile:   filepath.Join(dir, "users.json"),
		Warehouse:   wh,
		Log:         log,

		KeitaroJobFile: jobFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

type noopLog struct{}

func (noopLog) Warn(string, ...any) {}
func (noopLog) Info(string, ...any) {}

func keitaroGET(t *testing.T, srv *httptest.Server, path string) []byte {
	t.Helper()
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	resp, err := c.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %d", path, resp.StatusCode)
	}
	return raw
}

func keitaroPOST(t *testing.T, srv *httptest.Server, path string, body any) ([]byte, int) {
	t.Helper()
	c := newClient(t)
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	resp := csrfDo(t, c, http.MethodPost, srv.URL+path, csrf, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw, resp.StatusCode
}

func postKeitaroImport(t *testing.T, srv *httptest.Server, body any) []byte {
	t.Helper()
	raw, code := keitaroPOST(t, srv, "/api/keitaro/import", body)
	if code != http.StatusAccepted {
		t.Fatalf("import %d %s", code, raw)
	}
	var started struct {
		Started bool `json:"started"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || !started.Started {
		t.Fatalf("started %s", raw)
	}
	return raw
}

func readKeitaroJob(t *testing.T, srv *httptest.Server) keitaroJobBody {
	t.Helper()
	raw := keitaroGET(t, srv, "/api/keitaro/job")
	var body keitaroJobBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func waitKeitaroJob(t *testing.T, srv *httptest.Server) keitaroJobBody {
	t.Helper()
	return waitKeitaroJobFor(t, srv, 10*time.Second)
}

func waitKeitaroJobFor(t *testing.T, srv *httptest.Server, limit time.Duration) keitaroJobBody {
	t.Helper()
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("login %d", code)
	}
	deadline := time.Now().Add(limit)
	var body keitaroJobBody
	for {
		resp, err := c.Get(srv.URL + "/api/keitaro/job")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("job %d %s", resp.StatusCode, raw)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if !body.Running && body.Stage != "idle" {
			if _, err := time.Parse(time.RFC3339, body.StartedAt); err != nil {
				t.Fatalf("started_at %q", body.StartedAt)
			}
			if _, err := time.Parse(time.RFC3339, body.FinishedAt); err != nil {
				t.Fatalf("finished_at %q", body.FinishedAt)
			}
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still running: %+v", body)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func keitaroFake(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	var clickPage atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "unauth" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if mode == "count" && r.URL.Path == "/admin_api/v1/campaigns" {
			_, _ = w.Write([]byte(`[{},{},{}]`))
			return
		}
		if mode == "count" && strings.HasSuffix(r.URL.Path, "/log") {
			_, _ = w.Write([]byte(`{"rows":[],"total":0}`))
			return
		}
		switch r.URL.Path {
		case "/admin_api/v1/users":
			_, _ = w.Write([]byte(`[
				{"id":10,"login":"buyer","type":"USER","access_data":{"resources":["campaigns","offers"],"campaigns_access_type":"to_groups_and_selected","campaigns_selected_entities":[7],"offers_access_type":"read_only"},"preferences":{"language":"en","timezone":"UTC"}},
				{"id":1,"login":"admin","type":"USER","access_data":{"resources":["offers"]}}
			]`))
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":7,"alias":"promo","name":"Spring","state":"active"}]`))
		case "/admin_api/v1/campaigns/7/streams":
			_, _ = w.Write([]byte(`[{"id":3,"type":"regular","name":"main","state":"active","action_type":"http","action_payload":"https://offer.example/a","offers":[{"offer_id":4,"share":100,"state":"active"}]}]`))
		case "/admin_api/v1/offers":
			_, _ = w.Write([]byte(`[{"id":4,"name":"Offer","state":"active","action_type":"http","action_payload":"https://offer.example/a","affiliate_network_id":9}]`))
		case "/admin_api/v1/affiliate_networks":
			_, _ = w.Write([]byte(`[{"id":9,"name":"Net","state":"active"}]`))
		case "/admin_api/v1/clicks/log":
			if clickPage.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"rows":[{"sub_id":"c1","datetime":"2026-10-01 00:00:00","campaign_id":7,"campaign":"Camp","stream":"s","device_type":"desktop","is_bot":false,"is_unique_campaign":true}],"total":1}`))
				return
			}
			_, _ = w.Write([]byte(`{"rows":[],"total":1}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func assertImportedFiles(t *testing.T, dir string, groups *memGroups) {
	t.Helper()
	offers := readOffers(t, filepath.Join(dir, "offers.json"))
	nets := readNets(t, filepath.Join(dir, "networks.json"))
	offN, netN, grpN := 0, 0, 0
	var kept bool
	for _, o := range offers {
		if o.ID == "4" {
			offN++
		}
		if o.ID == "keep" {
			kept = true
		}
	}
	for _, n := range nets {
		if n.ID == "9" {
			netN++
		}
	}
	for _, g := range groups.list {
		if g.ID == "promo" && g.Name == "Spring" {
			grpN++
		}
	}
	if offN != 1 || netN != 1 || grpN != 1 || !kept {
		t.Fatalf("offers=%d nets=%d groups=%d kept=%v offers=%+v groups=%+v", offN, netN, grpN, kept, offers, groups.list)
	}
	b, err := os.ReadFile(filepath.Join(dir, "offers.json"))
	if err != nil || !strings.Contains(string(b), `"id": "4"`) {
		t.Fatal("offers file missing id 4")
	}
	b, err = os.ReadFile(filepath.Join(dir, "networks.json"))
	if err != nil || !strings.Contains(string(b), `"id": "9"`) {
		t.Fatal("networks file missing id 9")
	}
}

func readOffers(t *testing.T, path string) []config.Offer {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list []config.Offer
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func readNets(t *testing.T, path string) []config.AffiliateNetwork {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list []config.AffiliateNetwork
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	return list
}
