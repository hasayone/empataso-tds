package ktimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIPListSharedAndReject(t *testing.T) {
	payload := json.RawMessage(`["10.0.0.1","10.0.0.1","not-an-ip","10.1.0.0/16","2001:db8::1-2001:db8::9","zzz","bad2","bad3"]`)
	snap := Snapshot{
		Campaigns: []Campaign{
			{ID: 1, Alias: "a", Name: "A", State: "active"},
			{ID: 2, Alias: "b", Name: "B", State: "active"},
		},
		Streams: map[string][]KStream{
			"1": {{ID: 11, Type: "regular", State: "active", ActionType: "http", ActionPayload: "https://a.example/", Filters: []KFilter{{Name: "ip", Mode: "reject", Payload: payload}}}},
			"2": {{ID: 12, Type: "regular", State: "active", ActionType: "http", ActionPayload: "https://b.example/", Filters: []KFilter{{Name: "ip", Mode: "reject", Payload: payload}}}},
		},
	}
	res := MapAll(snap)
	if len(res.Lists) != 1 {
		t.Fatalf("lists = %+v", res.Lists)
	}
	a, _ := findGroup(res.Groups, "a")
	b, _ := findGroup(res.Groups, "b")
	if a.Streams[0].Rules.IPList.File == "" || a.Streams[0].Rules.IPList.File != b.Streams[0].Rules.IPList.File || a.Streams[0].Rules.IPList.Flag != 1 {
		t.Fatalf("files %+v %+v", a.Streams[0].Rules.IPList, b.Streams[0].Rules.IPList)
	}
	body := res.Lists[a.Streams[0].Rules.IPList.File]
	want := "10.0.0.1\n10.1.0.0/16\n2001:db8::1-2001:db8::9\n"
	if body != want || IPListName(body) != a.Streams[0].Rules.IPList.File || !strings.HasPrefix(a.Streams[0].Rules.IPList.File, "ip-") || strings.HasPrefix(a.Streams[0].Rules.IPList.File, "kt-") {
		t.Fatalf("body %q name %s", body, a.Streams[0].Rules.IPList.File)
	}
	text := lossesText(res.Losses)
	if !strings.Contains(text, "фильтр ip отбросил негодные записи: not-an-ip, zzz, bad2") || strings.Contains(text, "bad3") {
		t.Fatalf("losses %s", text)
	}
}

func TestProxyOnlyStaysEnabled(t *testing.T) {
	var losses []Loss
	st := KStream{ID: 5, State: "active", ActionType: "http", ActionPayload: "https://a.example/", Filters: []KFilter{{Name: "proxy", Mode: "reject", Payload: json.RawMessage(`["1"]`)}}}
	s := (&mapCtx{losses: &losses}).buildStream(st, false)
	if !s.Status || s.Rules.Proxy != 2 {
		t.Fatalf("proxy %+v %+v", s.Rules, losses)
	}
	if len(losses) != 0 {
		t.Fatalf("%+v", losses)
	}
}

func TestOfferParamQuery(t *testing.T) {
	nets, losses := mapNetworks([]KNetwork{
		{ID: 1, Name: "token", OfferParam: "offer_id", State: "active", PostbackURL: "https://n.example/pb"},
		{ID: 2, Name: "query", OfferParam: "?&sub1={subid}", State: "active"},
		{ID: 3, Name: "amp", OfferParam: "&x=1", State: "active"},
	})
	if len(nets) != 3 || nets[0].OfferParam != "offer_id" || nets[0].OfferQuery != "" || nets[1].OfferQuery != "sub1={subid}" || nets[1].OfferParam != "" || nets[2].OfferQuery != "x=1" {
		t.Fatalf("%+v", nets)
	}
	if strings.Contains(lossesText(losses), "offer_param") {
		t.Fatalf("%+v", losses)
	}
}

func TestLandingURLCollapsed(t *testing.T) {
	snap := Snapshot{
		Campaigns: []Campaign{{ID: 1, Alias: "L", Name: "L", State: "active"}},
		Landings: []Landing{
			{ID: 1, Name: "A", ActionType: "http", ActionPayload: "https://land.example/a", State: "active"},
			{ID: 2, Name: "B", ActionType: "http", ActionPayload: "https://land.example/a", State: "active"},
		},
		Streams: map[string][]KStream{
			"1": {{
				ID: 8, Type: "regular", State: "active", Schema: "landings", ActionType: "http",
				Landings: []KStreamRef{{LandingID: 1, Share: 30, State: "active"}, {LandingID: 2, Share: 70, State: "active"}},
			}},
		},
	}
	res := MapAll(snap)
	g, _ := findGroup(res.Groups, "L")
	if g.Streams[0].Out.Redirect != "http_redirect" {
		t.Fatalf("remote landing %+v", g.Streams[0].Out)
	}
	refs := g.Streams[0].Out.Landings
	if len(refs) != 1 || refs[0].ID != "1" || refs[0].Share != 100 {
		t.Fatalf("%+v", refs)
	}
	if !strings.Contains(lossesText(res.Losses), "лендинги с одинаковым URL объединены: 1, 2") {
		t.Fatalf("%s", lossesText(res.Losses))
	}
}

func TestLocalOfferStaysDisabled(t *testing.T) {
	var losses []Loss
	offers := map[int]Offer{1: {ID: 1, Name: "L", OfferType: "local", ActionType: "local"}}
	st := KStream{ID: 9, Name: "loc", State: "active", ActionType: "http", Offers: []KStreamOffer{{OfferID: 1, Share: 100, State: "active"}}}
	s := (&mapCtx{losses: &losses, offers: offers}).buildStream(st, false)
	if s.Status || s.Out.Redirect == "landing" {
		t.Fatalf("%+v %+v", s, losses)
	}
	if len(losses) == 0 || losses[0].Ref != "offer 1" {
		t.Fatalf("%+v", losses)
	}
}

func TestMapLandingGroupName(t *testing.T) {
	groups := map[int][]KGroup{
		3: {{ID: 3, Name: "LP folder", Type: "landings"}},
	}
	items, losses := mapLandings([]Landing{{
		ID: 7, Name: "L", GroupID: 3, ActionType: "http", ActionPayload: "https://land.example/a", State: "active",
	}}, groups)
	if len(items) != 1 || items[0].Group != "LP folder" {
		t.Fatalf("items = %+v", items)
	}
	if len(losses) != 0 {
		t.Fatalf("losses = %+v", losses)
	}
}

func TestMapOfferGroupTrimmed(t *testing.T) {
	long := strings.Repeat("в", 70)
	groups := map[int][]KGroup{
		2: {{ID: 2, Name: long, Type: "offers"}},
	}
	items, losses := mapOffers([]Offer{{
		ID: 5, Name: "O", GroupID: 2, ActionType: "http", ActionPayload: "https://offer.example/a", State: "active",
	}}, groups)
	want := string([]rune(long)[:64])
	if len(items) != 1 || items[0].Group != want {
		t.Fatalf("group = %q len %d", items[0].Group, len([]rune(items[0].Group)))
	}
	if len(losses) != 1 || losses[0].Kind != "approximated" || losses[0].Ref != "offer 5" ||
		losses[0].Detail != "имя группы обрезано до 64 символов" {
		t.Fatalf("losses = %+v", losses)
	}
}

func TestRewriteKeepsUnknown(t *testing.T) {
	got := rewriteMacros("https://m.example/go?x={bundle_id}&s={subid}&k={sub_id_1}")
	if got != "https://m.example/go?x={bundle_id}&s=[CID]&k=[PAR-1]" {
		t.Fatal(got)
	}
}
