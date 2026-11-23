package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/router"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
	"github.com/redis/go-redis/v9"
)

// trafficRig is a roles rig whose admin reads a traffic log on a memory Redis.
func trafficRig(t *testing.T) (*rolesRig, *store.TrafficLog) {
	t.Helper()
	rig := newRolesRig(t, "")
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	log := store.NewTrafficLog(rdb, store.DefaultTrafficLogLimits())
	rig.s.cfg.Traffic = log
	return rig, log
}

// pushTrace stores one trace under campaign id, as the engine would.
func pushTrace(t *testing.T, log *store.TrafficLog, id, cid string, tr router.Trace) {
	t.Helper()
	tr.Campaign, tr.CID = id, cid
	tr.Seal()
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Push(context.Background(), []string{id}, cid, b); err != nil {
		t.Fatal(err)
	}
}

func decodeTrafficList(t *testing.T, raw []byte) trafficLogList {
	t.Helper()
	var out trafficLogList
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bad answer %s: %v", raw, err)
	}
	return out
}

func TestTrafficLogListSearchAndLimit(t *testing.T) {
	rig, log := trafficRig(t)
	pushTrace(t, log, "c1", "k1", router.Trace{IP: "203.0.113.1", Visitor: router.VisitorInfo{Country: "de"}})
	pushTrace(t, log, "c1", "k2", router.Trace{IP: "203.0.113.2", Visitor: router.VisitorInfo{Country: "fr", Subs: map[string]string{"sub_id_1": "Fonbet"}}})
	pushTrace(t, log, "c1", "k3", router.Trace{IP: "203.0.113.3", Visitor: router.VisitorInfo{Country: "de"}})
	pushTrace(t, log, "c2", "k9", router.Trace{IP: "198.51.100.1"})

	code, raw := rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log", nil)
	if code != http.StatusOK {
		t.Fatalf("list %d %s", code, raw)
	}
	all := decodeTrafficList(t, raw)
	if !all.Available || !all.Enabled || all.Max != store.DefaultTrafficLogMax || all.Total != 3 || len(all.Entries) != 3 {
		t.Fatalf("list = %+v", all)
	}
	if all.Entries[0].CID != "k3" || all.Entries[2].CID != "k1" {
		t.Errorf("entries are not newest first: %s %s", all.Entries[0].CID, all.Entries[2].CID)
	}

	_, raw = rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log?q=fonbet", nil)
	if got := decodeTrafficList(t, raw); got.Total != 1 || got.Entries[0].CID != "k2" {
		t.Errorf("a sub id search = %+v", got)
	}
	_, raw = rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log?q=DE+203.0.113.3", nil)
	if got := decodeTrafficList(t, raw); got.Total != 1 || got.Entries[0].CID != "k3" {
		t.Errorf("all words must match: %+v", got)
	}
	_, raw = rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log?limit=2", nil)
	if got := decodeTrafficList(t, raw); got.Total != 2 || len(got.Entries) != 2 {
		t.Errorf("limit 2: total %d entries %d", got.Total, len(got.Entries))
	}
	_, raw = rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log?q=198.51.100.1", nil)
	if got := decodeTrafficList(t, raw); got.Total != 0 {
		t.Errorf("another campaign's click leaked into the search: %+v", got)
	}
	if code, _ := rig.adminAPI(t, http.MethodGet, "/api/campaigns/nope/traffic-log", nil); code != http.StatusNotFound {
		t.Errorf("unknown campaign: %d", code)
	}
}

func TestTrafficLogClickPath(t *testing.T) {
	rig, log := trafficRig(t)
	pushTrace(t, log, "c1", "k1", router.Trace{IP: "203.0.113.1", Hops: []router.Hop{{Group: "c1", Selected: "-"}}})
	pushTrace(t, log, "c2", "k2", router.Trace{IP: "203.0.113.2"})

	code, raw := rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log/k1", nil)
	var one trafficLogClick
	if code != http.StatusOK || json.Unmarshal(raw, &one) != nil || len(one.Entries) != 1 || one.Entries[0].IP != "203.0.113.1" {
		t.Fatalf("click path %d %s", code, raw)
	}
	_, raw = rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log/k2", nil)
	if err := json.Unmarshal(raw, &one); err != nil || len(one.Entries) != 0 {
		t.Errorf("a click of another campaign must read empty: %s", raw)
	}
	_, raw = rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log/expired", nil)
	if err := json.Unmarshal(raw, &one); err != nil || len(one.Entries) != 0 || !one.Available {
		t.Errorf("an expired click reads empty and the log stays available: %s", raw)
	}
}

func TestTrafficLogSwitchIsStoredOnTheCampaign(t *testing.T) {
	rig, _ := trafficRig(t)
	code, raw := rig.adminAPI(t, http.MethodPut, "/api/campaigns/c1/traffic-log", map[string]any{"enabled": false})
	if code != http.StatusOK || strings.Contains(string(raw), `"enabled":true`) {
		t.Fatalf("switch off %d %s", code, raw)
	}
	if !rig.storedGroups().list[0].TrafficLogOff {
		t.Error("the campaign did not keep the switch")
	}
	_, raw = rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log", nil)
	if decodeTrafficList(t, raw).Enabled {
		t.Error("the list still reports the log on")
	}
	if rig.storedGroups().list[1].TrafficLogOff {
		t.Error("another campaign was switched")
	}
	if code, _ := rig.adminAPI(t, http.MethodPut, "/api/campaigns/c1/traffic-log", map[string]any{"enabled": true}); code != http.StatusOK || rig.storedGroups().list[0].TrafficLogOff {
		t.Errorf("switch on: %d", code)
	}
	if code, _ := rig.adminAPI(t, http.MethodPut, "/api/campaigns/c1/traffic-log", map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("a body without enabled: %d", code)
	}
	if code, _ := rig.adminAPI(t, http.MethodPut, "/api/campaigns/nope/traffic-log", map[string]any{"enabled": false}); code != http.StatusNotFound {
		t.Errorf("unknown campaign: %d", code)
	}
}

func TestTrafficLogWithoutRedis(t *testing.T) {
	rig := newRolesRig(t, "")
	code, raw := rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log", nil)
	got := decodeTrafficList(t, raw)
	if code != http.StatusOK || got.Available || len(got.Entries) != 0 || !got.Enabled {
		t.Errorf("without Redis: %d %+v", code, got)
	}
}

func TestTrafficLogRespectsCampaignAccess(t *testing.T) {
	rig, log := trafficRig(t)
	pushTrace(t, log, "c2", "k2", router.Trace{IP: "203.0.113.2"})
	rig.setRole(t, security.Access{
		Sections: userSectionsAll(),
		Grants:   map[string]security.Grant{"campaigns": {Mode: security.ModeFull}},
	})
	if code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodGet, "/api/campaigns/c2/traffic-log", nil); code != http.StatusOK {
		t.Fatalf("a user with the campaign sees its log: %d", code)
	}
	rig.setRole(t, security.Access{Sections: []string{"flows"}})
	if code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodGet, "/api/campaigns/c2/traffic-log", nil); code != http.StatusForbidden {
		t.Errorf("a user without the logs section: %d", code)
	}
}

func TestSimulateReturnsTheClickPath(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.storedGroups().list[0].Streams = []config.Stream{
		{Name: "de_only", Status: true,
			Rules: config.Rules{Country: config.ListFilter{Flag: config.FlagB, Values: []string{"de"}}, Phone: config.FlagA},
			Out:   config.Output{Redirect: "show_text", Out: "DE"}},
		{Name: "rest", Status: true, Out: config.Output{Redirect: "show_text", Out: "REST"}},
	}
	body := map[string]any{
		"groups": rig.storedGroups().list, "group": "c1",
		"visitor": map[string]any{
			"ip": "8.8.8.8", "country": "fr", "lang": "en", "query": "sub_id_1=abc&api=SECRETVALUE",
			"useragent": "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148 Safari/604.1",
		},
	}
	code, raw := rig.adminAPI(t, http.MethodPost, "/api/simulate", body)
	if code != http.StatusOK {
		t.Fatalf("simulate %d %s", code, raw)
	}
	var out struct {
		Trace router.Trace `json:"trace"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	tr := out.Trace
	if tr.Source != router.TraceSimulation || tr.Campaign != "c1" || tr.IP != "8.8.8.8" || tr.Stream != "rest" || tr.Action != "show_text" {
		t.Fatalf("trace = %+v", tr)
	}
	if len(tr.Hops) != 1 || len(tr.Hops[0].Streams) != 2 || tr.Hops[0].Selected != "rest" {
		t.Fatalf("hops = %+v", tr.Hops)
	}
	if first := tr.Hops[0].Streams[0]; first.Match || len(first.Why) == 0 {
		t.Errorf("the first stream rejects the visitor: %+v", first)
	}
	if strings.Contains(string(raw), "SECRETVALUE") || !strings.Contains(tr.URL, "api=[hidden]") {
		t.Errorf("a secret query value reached the path: %s", tr.URL)
	}
	if tr.Visitor.OS != "iOS" || tr.Visitor.Subs["sub_id_1"] != "abc" {
		t.Errorf("visitor = %+v", tr.Visitor)
	}
}

func TestTrafficLogHidesWhatTheRoleMayNotSee(t *testing.T) {
	rig, log := trafficRig(t)
	pushTrace(t, log, "c1", "k1", router.Trace{
		IP: "203.0.113.9", URL: "/c1?kw=shoes&sub_id_1=abc",
		Headers: []router.HeaderLine{{Name: "x-forwarded-for", Value: "203.0.113.9"}},
		Visitor: router.VisitorInfo{
			Country: "de", Keyword: "shoes", Referer: "https://r.example/?q=1", UserAgent: "UA-STRING",
			Subs: map[string]string{"sub_id_1": "abc", "sub_id_2": "keep"},
		},
	})
	rig.setRole(t, editorAccess("ip", "keyword", "sub_id_1"))
	code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodGet, "/api/campaigns/c1/traffic-log", nil)
	if code != http.StatusOK {
		t.Fatalf("list %d %s", code, raw)
	}
	body := string(raw)
	for _, leak := range []string{"203.0.113.9", "shoes", "sub_id_1", "abc", "kw="} {
		if strings.Contains(body, leak) {
			t.Errorf("the hidden value %q reached the role: %s", leak, body)
		}
	}
	if !strings.Contains(body, `"sub_id_2":"keep"`) || !strings.Contains(body, "UA-STRING") {
		t.Errorf("a visible value was removed: %s", body)
	}
	_, raw = rig.api(t, rig.user, rig.ucsrf, http.MethodGet, "/api/campaigns/c1/traffic-log?q=203.0.113.9", nil)
	if got := decodeTrafficList(t, raw); got.Total != 0 {
		t.Errorf("a hidden address can be searched for: %+v", got)
	}
	_, raw = rig.adminAPI(t, http.MethodGet, "/api/campaigns/c1/traffic-log?q=203.0.113.9", nil)
	if got := decodeTrafficList(t, raw); got.Total != 1 {
		t.Errorf("the administrator sees every field: %+v", got)
	}
}
