package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
	"github.com/kuztds/bridge/ktimport"
)

func TestSanitizeKeitaroKey(t *testing.T) {
	key := "fixture-key"
	got := sanitize(fmt.Errorf("failed %s then %s", key, key), key)
	if got != "failed *** then ***" || strings.Contains(got, key) {
		t.Fatal("sanitize left the key in the error")
	}
	if sanitize(nil, key) != "" {
		t.Fatal("nil error")
	}
}

func TestKeitaroStatus(t *testing.T) {
	key := "fixture-key"
	bare := newKeitaroAdmin(t, "", "", nil, nil, nil)
	raw := keitaroGET(t, bare, "/api/keitaro/status")
	var off struct {
		Configured bool   `json:"configured"`
		Host       string `json:"host"`
	}
	if err := json.Unmarshal(raw, &off); err != nil {
		t.Fatal(err)
	}
	if off.Configured || off.Host != "" {
		t.Fatalf("status %+v", off)
	}

	on := newKeitaroAdmin(t, "http://tracker.example/admin", key, nil, nil, nil)
	raw = keitaroGET(t, on, "/api/keitaro/status")
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	var body struct {
		Configured bool   `json:"configured"`
		Host       string `json:"host"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !body.Configured || body.Host != "tracker.example" {
		t.Fatalf("status %+v", body)
	}
	if strings.Contains(body.Host, key) || strings.Contains(string(raw), "/admin") {
		t.Fatal("host leaked the origin path")
	}
}

func TestKeitaroCheck(t *testing.T) {
	key := "fixture-key"
	okSrv := keitaroFake(t, "count")
	admin := newKeitaroAdmin(t, okSrv.URL, key, nil, nil, nil)
	raw, code := keitaroPOST(t, admin, "/api/keitaro/check", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("check %d", code)
	}
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	var body struct {
		OK             bool   `json:"ok"`
		Campaigns      int    `json:"campaigns"`
		API            string `json:"api"`
		ProductVersion string `json:"product_version"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Campaigns != 3 || body.API != "v1" || body.ProductVersion != "" {
		t.Fatalf("check %+v", body)
	}
	if !strings.Contains(string(raw), `"api":"v1"`) || !strings.Contains(string(raw), `"product_version":""`) {
		t.Fatalf("check fields %s", raw)
	}

	bad := keitaroFake(t, "unauth")
	admin = newKeitaroAdmin(t, bad.URL, key, nil, nil, nil)
	raw, code = keitaroPOST(t, admin, "/api/keitaro/check", map[string]any{})
	if code != http.StatusBadGateway {
		t.Fatalf("check 401 upstream: %d", code)
	}
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	if strings.Contains(string(raw), "product_version") || strings.Contains(string(raw), `"api"`) {
		t.Fatalf("502 body %s", raw)
	}

	bare := newKeitaroAdmin(t, "", "", nil, nil, nil)
	raw, code = keitaroPOST(t, bare, "/api/keitaro/check", map[string]any{})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured %d %s", code, raw)
	}
	if strings.Contains(string(raw), "product_version") || strings.Contains(string(raw), `"api"`) {
		t.Fatalf("503 body %s", raw)
	}
}

func TestKeitaroImportAndPurge(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFake(t, "catalog")
	groups := &memGroups{list: []config.Group{{ID: "local", Name: "Local", Status: true}}}
	log := &captureLog{}
	sink := &fakeSink{eventsN: 2, convN: 3}
	dir := t.TempDir()
	offers := NewJSONStore[config.Offer](filepath.Join(dir, "offers.json"))
	if err := offers.Save(context.Background(), []config.Offer{{
		ID: "keep", Name: "Keep", URL: "https://keep.example/a", Status: true,
	}}); err != nil {
		t.Fatal(err)
	}
	admin := newKeitaroAdmin(t, fake.URL, key, groups, sink, &keitaroFiles{
		dir: dir, offers: offers, log: log,
	})
	body := map[string]any{
		"domains": true, "networks": true, "offers": true,
		"landings": true, "sources": true, "campaigns": true,
	}
	raw := postKeitaroImport(t, admin, body)
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	first := waitKeitaroJob(t, admin)
	if first.Stage != "done" || first.Error != "" {
		t.Fatalf("job %+v", first)
	}
	if first.Counts["offers"] != 1 || first.Counts["networks"] != 1 || first.Counts["groups"] != 1 {
		t.Fatalf("counts %+v", first.Counts)
	}
	if _, ok := first.Counts["conversions"]; ok {
		t.Fatal("conversions counted without a request")
	}
	assertImportedFiles(t, dir, groups)

	raw = postKeitaroImport(t, admin, body)
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	second := waitKeitaroJob(t, admin)
	if second.Stage != "done" {
		t.Fatalf("second job %+v", second)
	}
	assertImportedFiles(t, dir, groups)

	raw, code := keitaroPOST(t, admin, "/api/keitaro/purge", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("purge %d", code)
	}
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	var purged struct {
		Removed struct {
			Groups, Offers, Networks int
			Events, Conversions      int64
		} `json:"removed"`
	}
	if err := json.Unmarshal(raw, &purged); err != nil {
		t.Fatal(err)
	}
	if purged.Removed.Groups != 1 || purged.Removed.Offers != 1 || purged.Removed.Networks != 1 {
		t.Fatalf("removed %+v", purged.Removed)
	}
	if purged.Removed.Events != 2 || purged.Removed.Conversions != 3 {
		t.Fatalf("warehouse counts %+v", purged.Removed)
	}
	host := keitaroHost(fake.URL)
	if len(sink.purged) != 1 || sink.purged[0] != host || host == "" {
		t.Fatal("warehouse purge host")
	}
	leftOffers := readOffers(t, filepath.Join(dir, "offers.json"))
	if len(leftOffers) != 1 || leftOffers[0].ID != "keep" {
		t.Fatalf("offers after purge %+v", leftOffers)
	}
	if len(groups.list) != 1 || groups.list[0].ID != "local" || strings.HasPrefix(groups.list[0].Name, "kt: ") {
		t.Fatalf("groups after purge %+v", groups.list)
	}
	if len(log.msgs) == 0 {
		t.Fatal("missing log line")
	}
	var sawDuration bool
	for _, args := range log.args {
		for _, a := range args {
			if a == "duration" {
				sawDuration = true
			}
		}
	}
	if !sawDuration {
		t.Fatal("missing duration")
	}
	for _, args := range log.args {
		for _, a := range args {
			s, ok := a.(string)
			if !ok {
				continue
			}
			if s == key || strings.Contains(s, "://") || strings.Contains(s, "?") {
				t.Fatal("log contains a secret or a url")
			}
		}
	}
}

func TestKeitaroImportLogs(t *testing.T) {
	key := "fixture-key"
	var ranges []string
	var fetchOrder []string
	var pages atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/conversions/log":
			fetchOrder = append(fetchOrder, "conversions")
			var payload struct {
				Range struct {
					From, To, Timezone string
				} `json:"range"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			ranges = append(ranges, payload.Range.From+"|"+payload.Range.To+"|"+payload.Range.Timezone)
			if pages.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"rows":[{"sub_id":"abc","status":"Lead","revenue":1.5,"campaign":"Camp","stream":"s","offer_id":4,"landing_id":2,"affiliate_network_id":9,"ts_id":3,"country_code":"US","city":"Austin","region":"TX","device_type":"Mobile","operator":"Vodacom","postback_datetime":"2026-10-02 03:04:05","sub_id_1":"x"}],"total":1}`))
				return
			}
			_, _ = w.Write([]byte(`{"rows":[],"total":1}`))
		case "/admin_api/v1/clicks/log":
			fetchOrder = append(fetchOrder, "clicks")
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"abc","datetime":"2026-10-03 01:02:03","campaign_id":7,"campaign":"Camp","stream":"s","country_code":"US","city":"Austin","region":"TX","language":"en","os":"iOS","os_version":"17","browser":"Safari","browser_version":"17.0","device_type":"Mobile","device_model":"iPhone","operator":"Vodacom","is_bot":true,"is_unique_campaign":true,"ip":"1.2.3.4","referrer":"https://ref.example/path","search_engine":"Google","user_agent":"ua","keyword":"shoes","offer_id":4,"landing_id":2,"affiliate_network_id":9,"ts_id":3,"cost":1.5,"sub_id_1":"z"}],"total":1}`))
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":7,"alias":"camp-seven","name":"Camp","state":"active"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	groups := &memGroups{list: []config.Group{{ID: "camp-seven", Name: "Camp", Num: 7}}}
	s, admin := newKeitaroServers(t, fake.URL, key, groups, sink, nil)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Groups: []string{"camp-seven"}}); err != nil {
		t.Fatal(err)
	}
	raw := postKeitaroImport(t, admin, map[string]any{
		"conversions": true, "clicks": true,
		"from": "2026-10-01", "to": "2026-10-15",
	})
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	body := waitKeitaroJob(t, admin)
	if body.Stage != "done" || body.Error != "" {
		t.Fatalf("job %+v", body)
	}
	if strings.Contains(body.Error, key) {
		t.Fatal("job contains key")
	}
	if body.Counts["conversions"] != 1 || body.Counts["clicks"] != 1 {
		t.Fatalf("counts %+v", body.Counts)
	}
	if len(ranges) == 0 || ranges[0] != "2026-10-01 00:00:00|2026-10-15 23:59:59|UTC" {
		t.Fatalf("range %#v", ranges)
	}
	if len(fetchOrder) != 2 || fetchOrder[0] != "clicks" || fetchOrder[1] != "conversions" {
		t.Fatalf("fetch order %#v", fetchOrder)
	}
	if len(sink.convs) != 1 {
		t.Fatalf("conversions %d", len(sink.convs))
	}
	row := sink.convs[0]
	wantTS := time.Date(2026, 10, 2, 3, 4, 5, 0, time.UTC)
	if !row.TS.Equal(wantTS) || row.Domain != keitaroHost(fake.URL) || row.CID != "abc" || row.Status != "lead" ||
		row.Profit != 1.5 || row.GroupName != "Camp" || row.StreamName != "s" ||
		row.Country != "US" || row.City != "Austin" || row.Device != "phone" || row.Operator != "Vodacom" ||
		row.OfferID != "4" || row.LandingID != "2" || row.NetworkID != "9" ||
		row.SourceID != "3" || row.Subs[0] != "x" ||
		row.IP != "1.2.3.4" || row.UserAgent != "ua" || row.OS != "iOS" || row.Browser != "Safari" ||
		row.GroupID != "camp-seven" {
		t.Fatalf("conversion %+v", row)
	}
	wantClick := time.Date(2026, 10, 3, 1, 2, 3, 0, time.UTC)
	if !row.ClickTS.Equal(wantClick) {
		t.Fatalf("click ts from index %s", row.ClickTS)
	}
	if len(sink.events) != 1 {
		t.Fatalf("clicks %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.GroupID != "camp-seven" || ev.GroupName != "Camp" || ev.Device != "phone" || ev.Uniq != 1 || ev.Bot != "bot" ||
		ev.OfferID != "4" || ev.LandingID != "2" || ev.CostCents != 150 ||
		ev.Country != "US" || ev.Operator != "Vodacom" || ev.SE != "Google" ||
		ev.NetworkID != "9" || ev.SourceID != "3" ||
		ev.Domain != keitaroHost(fake.URL) || ev.CID != "abc" || ev.Subs[0] != "z" || ev.Keyword != "shoes" {
		t.Fatalf("click %+v", ev)
	}

	raw, code := keitaroPOST(t, admin, "/api/keitaro/import", map[string]any{
		"conversions": true, "from": "yesterday", "to": "2026-10-15",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("bad range %d", code)
	}
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
}

func TestKeitaroOddConversionStatus(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/conversions/log":
			_, _ = w.Write([]byte(`{"rows":[` +
				`{"sub_id":"t1","status":"trash","postback_datetime":"2026-10-02 03:04:05"},` +
				`{"sub_id":"t2","status":"custom","postback_datetime":"2026-10-02 03:04:06"},` +
				`{"sub_id":"t3","status":"lead","postback_datetime":"2026-10-02 03:04:07"},` +
				`{"sub_id":"t4","status":"sale","postback_datetime":"2026-10-02 03:04:08"},` +
				`{"sub_id":"t5","status":"","postback_datetime":"2026-10-02 03:04:09"},` +
				`{"sub_id":"t6","status":"  Rejected  ","postback_datetime":"2026-10-02 03:04:10"},` +
				`{"sub_id":"t7","status":"registration","postback_datetime":"2026-10-02 03:04:11"},` +
				`{"sub_id":"t8","status":"deposit","postback_datetime":"2026-10-02 03:04:12"},` +
				`{"sub_id":"t9","status":"rebill","postback_datetime":"2026-10-02 03:04:13"}` +
				`],"total":9}`))
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	admin := newKeitaroAdmin(t, fake.URL, key, nil, sink, nil)
	raw := postKeitaroImport(t, admin, map[string]any{
		"conversions": true, "from": "2026-10-01", "to": "2026-10-15",
	})
	if strings.Contains(string(raw), key) {
		t.Fatal("response contains key")
	}
	body := waitKeitaroJob(t, admin)
	if body.Stage != "done" || body.Error != "" || body.Counts["conversions"] != 9 {
		t.Fatalf("job %+v", body)
	}
	want := "статус конверсии не из списка lead, sale, rejected, registration, deposit, rebill: 2"
	var n int
	for _, l := range body.Losses {
		if l.Ref == "conversions" && l.Detail == want {
			if l.Kind != "approximated" {
				t.Fatalf("loss %+v", l)
			}
			n++
		}
	}
	if n != 1 {
		t.Fatalf("losses %+v", body.Losses)
	}
	if len(sink.convs) != 9 {
		t.Fatalf("conversions %d", len(sink.convs))
	}
	got := map[string]string{}
	for _, row := range sink.convs {
		got[row.CID] = row.Status
	}
	if got["t1"] != "trash" || got["t2"] != "custom" || got["t3"] != "lead" || got["t4"] != "sale" || got["t6"] != "rejected" {
		t.Fatalf("statuses %+v", got)
	}
}

func TestKeitaroClickUsesCampaignAlias(t *testing.T) {
	key := "fixture-key"
	fake := keitaroFake(t, "catalog")
	sink := &fakeSink{}
	groups := &memGroups{}
	admin := newKeitaroAdmin(t, fake.URL, key, groups, sink, nil)
	postKeitaroImport(t, admin, map[string]any{
		"campaigns": true, "clicks": true,
		"from": "2026-10-01", "to": "2026-10-01",
	})
	if job := waitKeitaroJob(t, admin); job.Stage != "done" {
		t.Fatalf("job %+v", job)
	}
	if len(sink.events) != 1 || sink.events[0].GroupID != "promo" {
		t.Fatalf("events %+v", sink.events)
	}
}

func TestKeitaroImportLogsErrorKeepsUnknownLoss(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":7,"alias":"known","name":"Known","state":"active"}]`))
		case "/admin_api/v1/clicks/log":
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"x","datetime":"2026-10-01 00:00:00","campaign_id":999,"campaign":"Ghost"}],"total":1}`))
		case "/admin_api/v1/conversions/log":
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"x","status":"Lead","postback_datetime":"2026-10-01 01:00:00","campaign_id":999}],"total":1}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &convFailSink{fakeSink: fakeSink{}}
	groups := &memGroups{list: []config.Group{{ID: "known", Name: "Known"}}}
	s, admin := newKeitaroServers(t, fake.URL, key, groups, sink, nil)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Groups: []string{"known"}, Campaigns: map[string]string{"7": "known"}}); err != nil {
		t.Fatal(err)
	}
	postKeitaroImport(t, admin, map[string]any{
		"clicks": true, "conversions": true, "from": "2026-10-01", "to": "2026-10-01",
	})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "failed" || job.Error == "" {
		t.Fatalf("job %+v", job)
	}
	var lossFound bool
	for _, l := range job.Losses {
		if l.Ref == "log_campaign" && strings.Contains(l.Detail, "999") {
			lossFound = true
		}
	}
	if !lossFound {
		t.Fatalf("losses %+v", job.Losses)
	}
}

type convFailSink struct {
	fakeSink
}

func (c *convFailSink) InsertConversions(context.Context, []store.ConversionInsert) error {
	return fmt.Errorf("warehouse down")
}

func TestKeitaroWarehouseMissing(t *testing.T) {
	admin := newKeitaroAdmin(t, "http://tracker.example", "fixture-key", nil, nil, nil)
	raw := postKeitaroImport(t, admin, map[string]any{
		"conversions": true, "from": "2026-10-01", "to": "2026-10-02",
	})
	if strings.Contains(string(raw), "fixture-key") {
		t.Fatal("response contains key")
	}
	body := waitKeitaroJob(t, admin)
	if body.Stage != "done" || body.Counts["conversions"] != 0 || len(body.Losses) != 1 || body.Losses[0].Detail != "clickhouse unavailable" {
		t.Fatalf("job %+v", body)
	}
}

func TestKeitaroLogMapping(t *testing.T) {
	var row ktimport.ClickRow
	raw := []byte(`{"is_bot":false,"is_unique_campaign":false,"country_code":"ZA","operator":"Vodacom","search_engine":"Google","affiliate_network_id":4,"ts_id":8,"device_type":"Desktop"}`)
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	ev := clickEvent(row, "tracker.example", logPlace{})
	if ev.Bot != "" || ev.Uniq != 0 || ev.Country != "ZA" || ev.Operator != "Vodacom" || ev.SE != "Google" ||
		ev.NetworkID != "4" || ev.SourceID != "8" || ev.Device != "computer" {
		t.Fatalf("click %+v", ev)
	}
	raw = []byte(`{"affiliate_network_id":0,"ts_id":0}`)
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	ev = clickEvent(row, "tracker.example", logPlace{})
	if ev.NetworkID != "" || ev.SourceID != "" {
		t.Fatalf("zero ids %+v", ev)
	}
	var conv ktimport.ConversionRow
	raw = []byte(`{"revenue":2.5,"country_code":"ZA","region":"GP","device_type":"Tablet","operator":"MTN","landing_id":2,"affiliate_network_id":4,"ts_id":8,"offer_id":1,"status":"Sale"}`)
	if err := json.Unmarshal(raw, &conv); err != nil {
		t.Fatal(err)
	}
	got := conversionInsert(conv, "tracker.example", logPlace{}, nil)
	if got.Profit != 2.5 || got.Country != "ZA" || got.Device != "tablet" || got.Operator != "MTN" ||
		got.LandingID != "2" || got.NetworkID != "4" || got.SourceID != "8" ||
		got.OfferID != "1" || got.Status != "sale" {
		t.Fatalf("conversion %+v", got)
	}
}

func TestKeitaroSubIDKeepsDot(t *testing.T) {
	const sub = "2k5a13p.ac.2m74"
	ev := clickEvent(ktimport.ClickRow{SubID: sub}, "tracker.example", logPlace{})
	if ev.CID != sub {
		t.Fatalf("click cid %q", ev.CID)
	}
	got := conversionInsert(ktimport.ConversionRow{SubID: sub, Status: "sale"}, "tracker.example", logPlace{}, nil)
	if got.CID != sub || got.Status != "sale" {
		t.Fatalf("conversion cid %q status %q", got.CID, got.Status)
	}
}

func TestKeitaroImportLogsReusedGroupAlias(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":8,"alias":"promo","name":"Promo","state":"active"}]`))
		case "/admin_api/v1/campaigns/8/streams":
			_, _ = w.Write([]byte(`[{"id":1,"type":"regular","name":"main","state":"active","action_type":"blank"}]`))
		case "/admin_api/v1/clicks/log":
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"old","datetime":"2026-10-01 00:00:00","campaign_id":7,"campaign":"Old"}],"total":1}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	groups := &memGroups{list: []config.Group{{ID: "promo", Name: "Promo", Num: 8}}}
	s, admin := newKeitaroServers(t, fake.URL, key, groups, sink, nil)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{
		Groups:    []string{"promo"},
		Campaigns: map[string]string{"7": "promo"},
	}); err != nil {
		t.Fatal(err)
	}
	postKeitaroImport(t, admin, map[string]any{
		"campaigns": true, "clicks": true, "from": "2026-10-01", "to": "2026-10-01",
	})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" || len(sink.events) != 1 || sink.events[0].GroupID != "7" {
		t.Fatalf("job %+v event %+v", job, sink.events)
	}
	var lossFound bool
	for _, l := range job.Losses {
		if l.Ref == "log_campaign" && strings.Contains(l.Detail, "7") {
			lossFound = true
		}
	}
	if !lossFound {
		t.Fatalf("losses %+v", job.Losses)
	}
	loaded, err := s.cfg.Manifest.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Campaigns["7"] != "" || loaded.Campaigns["8"] != "promo" {
		t.Fatalf("manifest campaigns %+v", loaded.Campaigns)
	}
}

func TestKeitaroImportLogsStaleManifestPair(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":7,"alias":"camp","name":"Camp","state":"active"}]`))
		case "/admin_api/v1/clicks/log":
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"x","datetime":"2026-10-01 00:00:00","campaign_id":7,"campaign":"Camp"}],"total":1}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	s, admin := newKeitaroServers(t, fake.URL, key, &memGroups{}, sink, nil)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Campaigns: map[string]string{"7": "gone"}}); err != nil {
		t.Fatal(err)
	}
	postKeitaroImport(t, admin, map[string]any{"clicks": true, "from": "2026-10-01", "to": "2026-10-01"})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" || len(sink.events) != 1 || sink.events[0].GroupID != "7" {
		t.Fatalf("job %+v event %+v", job, sink.events)
	}
	var lossFound bool
	for _, l := range job.Losses {
		if l.Ref == "log_campaign" && strings.Contains(l.Detail, "7") {
			lossFound = true
		}
	}
	if !lossFound {
		t.Fatalf("losses %+v", job.Losses)
	}
}

func TestKeitaroImportLogsCampaignsUnavailable(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin_api/v1/campaigns" {
			http.Error(w, "no", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/admin_api/v1/clicks/log" {
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"x","datetime":"2026-10-01 00:00:00","campaign_id":1}],"total":1}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	admin := newKeitaroAdmin(t, fake.URL, key, nil, sink, nil)
	postKeitaroImport(t, admin, map[string]any{"clicks": true, "from": "2026-10-01", "to": "2026-10-01"})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "failed" || job.Error == "" || len(sink.events) != 0 {
		t.Fatalf("job %+v events %d", job, len(sink.events))
	}
}

func TestKeitaroImportLogsUnknownCampaign(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[{"id":7,"alias":"known","name":"Known","state":"active"}]`))
		case "/admin_api/v1/clicks/log":
			_, _ = w.Write([]byte(`{"rows":[{"sub_id":"x","datetime":"2026-10-01 00:00:00","campaign_id":999,"campaign":"Ghost"}],"total":1}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	groups := &memGroups{list: []config.Group{{ID: "known", Name: "Known"}}}
	s, admin := newKeitaroServers(t, fake.URL, key, groups, sink, nil)
	if err := s.cfg.Manifest.Save(ktimport.Manifest{Groups: []string{"known"}, Campaigns: map[string]string{"7": "known"}}); err != nil {
		t.Fatal(err)
	}
	postKeitaroImport(t, admin, map[string]any{"clicks": true, "from": "2026-10-01", "to": "2026-10-01"})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" || len(sink.events) != 1 || sink.events[0].GroupID != "999" {
		t.Fatalf("job %+v event %+v", job, sink.events)
	}
	var lossFound bool
	for _, l := range job.Losses {
		if l.Ref == "log_campaign" && strings.Contains(l.Detail, "999") {
			lossFound = true
		}
	}
	if !lossFound {
		t.Fatalf("losses %+v", job.Losses)
	}
}

func TestKeitaroLogGroupIDsMatchImport(t *testing.T) {
	key := "fixture-key"
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/admin_api/v1/campaigns":
			_, _ = w.Write([]byte(`[
				{"id":10,"alias":"","name":"Empty","state":"active"},
				{"id":11,"alias":"dup","name":"One","state":"active"},
				{"id":12,"alias":"dup","name":"Two","state":"active"}
			]`))
		case "/admin_api/v1/campaigns/10/streams", "/admin_api/v1/campaigns/11/streams", "/admin_api/v1/campaigns/12/streams":
			_, _ = w.Write([]byte(`[{"id":1,"type":"regular","name":"main","state":"active","action_type":"blank"}]`))
		case "/admin_api/v1/clicks/log":
			_, _ = w.Write([]byte(`{"rows":[
				{"sub_id":"a","datetime":"2026-10-01 00:00:00","campaign_id":10,"campaign":"Empty"},
				{"sub_id":"b","datetime":"2026-10-01 00:00:01","campaign_id":12,"campaign":"Two"}
			],"total":2}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	groups := &memGroups{}
	admin := newKeitaroAdmin(t, fake.URL, key, groups, sink, nil)
	postKeitaroImport(t, admin, map[string]any{
		"campaigns": true, "clicks": true, "from": "2026-10-01", "to": "2026-10-01",
	})
	job := waitKeitaroJob(t, admin)
	if job.Stage != "done" {
		t.Fatalf("job %+v", job)
	}
	byNum := map[int]string{}
	for _, g := range groups.list {
		byNum[g.Num] = g.ID
	}
	for _, ev := range sink.events {
		var num int
		switch ev.CID {
		case "a":
			num = 10
		case "b":
			num = 12
		default:
			t.Fatalf("unexpected cid %q", ev.CID)
		}
		if byNum[num] != ev.GroupID {
			t.Fatalf("campaign %d store %q log %q", num, byNum[num], ev.GroupID)
		}
	}
	if byNum[10] != "missing-alias-10" || byNum[12] != "dup-dup-12" {
		t.Fatalf("groups %+v", byNum)
	}
}

func TestMergeImportedGroupNums(t *testing.T) {
	existing := []config.Group{
		{ID: "a", Name: "kt: A", Num: 3},
		{ID: "local", Name: "Home", Num: 1},
	}
	incoming := []ktimport.Group{
		{ID: "a", Name: "kt: A"},
		{ID: "b", Name: "kt: B"},
	}
	prev := ktimport.Manifest{}
	prev.Add(ktimport.KindGroups, "a")
	out, _, err := mergeImportedGroups(existing, incoming, prev, "", config.MainBoardID)
	if err != nil {
		t.Fatal(err)
	}
	max := 0
	for _, g := range existing {
		if g.Num > max {
			max = g.Num
		}
	}
	var gotA, gotB config.Group
	var seenA, seenB bool
	for _, g := range out {
		switch g.ID {
		case "a":
			gotA, seenA = g, true
		case "b":
			gotB, seenB = g, true
		}
	}
	if !seenA || gotA.Num != 3 {
		t.Fatalf("reimport %+v", gotA)
	}
	if !seenB || gotB.Num <= max {
		t.Fatalf("new group %+v max %d", gotB, max)
	}
}
