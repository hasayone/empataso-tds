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
		OK        bool `json:"ok"`
		Campaigns int  `json:"campaigns"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Campaigns != 3 {
		t.Fatalf("check %+v", body)
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
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(fake.Close)
	sink := &fakeSink{}
	admin := newKeitaroAdmin(t, fake.URL, key, nil, sink, nil)
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
		row.GroupID != "7" {
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
	if ev.GroupID != "7" || ev.GroupName != "Camp" || ev.Device != "phone" || ev.Uniq != 1 || ev.Bot != "bot" ||
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
	out, _, err := mergeImportedGroups(existing, incoming, prev)
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
