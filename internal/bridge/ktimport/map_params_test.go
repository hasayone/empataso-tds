package ktimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCampaignRenamedSubs(t *testing.T) {
	raw := json.RawMessage(`{
		"sub_id_1": {"name": "Bundle ID", "placeholder": "{bundle_id}", "alias": ""},
		"sub_id_2": {"name": "target keys", "placeholder": "{target_keys}", "alias": ""},
		"keyword": {"name": "kw", "placeholder": "{kw}", "alias": ""},
		"creative_id": {"name": "cr", "placeholder": "{crid}", "alias": "cr"}
	}`)
	url := "https://offer.example/go?b={bundle_id}&t={target_keys}&kw={kw}&cr={crid}&s={subid}&k={sub_id_2}&x={other}"
	snap := Snapshot{
		Campaigns: []Campaign{{
			ID: 3, Alias: "bundle", Name: "Bundle", State: "active",
			TrafficSourceID: 1, Parameters: raw,
		}},
		Streams: map[string][]KStream{
			"3": {{
				ID: 30, Name: "go", State: "active", ActionType: "http", Schema: "offers",
				Offers: []KStreamOffer{{OfferID: 9, Share: 100, State: "active"}},
			}},
		},
		Offers:         []Offer{{ID: 9, Name: "Main", ActionType: "http", ActionPayload: url, State: "active"}},
		TrafficSources: []KSource{{ID: 1, Name: "fb", State: "active"}},
	}
	res := MapAll(snap)
	src := res.Sources[0].Parameters
	if src["sub_id_1"] != "bundle_id" || src["sub_id_2"] != "target_keys" || src["keyword"] != "kw" || src["creative_id"] != "cr" {
		t.Fatalf("source = %+v", src)
	}
	g, ok := findGroup(res.Groups, "bundle")
	if !ok {
		t.Fatal("campaign missing")
	}
	got := g.Streams[0].Out.Out
	for _, want := range []string{"{sub_id_1}", "{sub_id_2}", "[KEY]", "[CID]", "[PAR-2]", "{crid}", "{other}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stream %q missing %s", got, want)
		}
	}
	for _, gone := range []string{"{bundle_id}", "{target_keys}", "{kw}"} {
		if strings.Contains(got, gone) {
			t.Fatalf("stream %q still has %s", got, gone)
		}
	}
	if len(res.Offers) != 1 || res.Offers[0].ID != "9" || res.Offers[0].URL != got {
		t.Fatalf("offer = %+v stream %s", res.Offers[0], got)
	}
}

func TestSourceAliasWins(t *testing.T) {
	raw := json.RawMessage(`{"sub_id_1": {"name": "Bundle ID", "placeholder": "{bundle_id}", "alias": ""}}`)
	url := "https://o.example/?b={bundle_id}&k={sub_id_2}"
	snap := Snapshot{
		Campaigns: []Campaign{{
			ID: 1, Alias: "keep", Name: "Keep", State: "active",
			TrafficSourceID: 1, Parameters: raw,
		}},
		Streams: map[string][]KStream{
			"1": {{ID: 11, State: "active", ActionType: "http", ActionPayload: url}},
		},
		TrafficSources: []KSource{{
			ID: 1, Name: "fb",
			Parameters: map[string]KParam{"sub_id_1": {Alias: "sub1"}},
		}},
	}
	res := MapAll(snap)
	if res.Sources[0].Parameters["sub_id_1"] != "sub1" {
		t.Fatalf("source = %+v", res.Sources[0].Parameters)
	}
	got := res.Groups[0].Streams[0].Out.Out
	if !strings.Contains(got, "{sub_id_1}") || !strings.Contains(got, "[PAR-2]") || strings.Contains(got, "{bundle_id}") {
		t.Fatalf("out = %s", got)
	}
}

func TestOfferCopiedWhenMacrosDiffer(t *testing.T) {
	offerURL := "https://offer.example/go?x={bundle_id}"
	landURL := "https://land.example/?x={bundle_id}"
	a := json.RawMessage(`{"sub_id_1": {"placeholder": "{bundle_id}"}}`)
	b := json.RawMessage(`{"sub_id_1": {"placeholder": "{pack}"}}`)
	snap := Snapshot{
		Campaigns: []Campaign{
			{ID: 1, Alias: "a", Name: "A", State: "active", TrafficSourceID: 1, Parameters: a},
			{ID: 2, Alias: "b", Name: "B", State: "active", TrafficSourceID: 2, Parameters: b},
			{ID: 3, Alias: "c", Name: "C", State: "active", TrafficSourceID: 1, Parameters: a},
		},
		Streams: map[string][]KStream{
			"1": {{ID: 11, State: "active", ActionType: "http", Schema: "offers",
				Offers:   []KStreamOffer{{OfferID: 7, Share: 100, State: "active"}},
				Landings: []KStreamRef{{LandingID: 8, Share: 100, State: "active"}},
			}},
			"2": {{ID: 12, State: "active", ActionType: "http", Schema: "offers",
				Offers:   []KStreamOffer{{OfferID: 7, Share: 100, State: "active"}},
				Landings: []KStreamRef{{LandingID: 8, Share: 100, State: "active"}},
			}},
			"3": {{ID: 13, State: "active", ActionType: "http", Schema: "offers",
				Offers: []KStreamOffer{{OfferID: 7, Share: 100, State: "active"}},
			}},
		},
		Offers:   []Offer{{ID: 7, Name: "Main", ActionType: "http", ActionPayload: offerURL, State: "active"}},
		Landings: []Landing{{ID: 8, Name: "LP", ActionType: "http", ActionPayload: landURL, State: "active"}},
		TrafficSources: []KSource{
			{ID: 1, Name: "one"},
			{ID: 2, Name: "two"},
		},
	}
	res := MapAll(snap)
	if len(res.Offers) != 2 || res.Offers[0].ID != "7" || !strings.Contains(res.Offers[0].URL, "{sub_id_1}") || strings.Contains(res.Offers[0].URL, "{bundle_id}") {
		t.Fatalf("offers = %+v", res.Offers)
	}
	if res.Offers[1].ID != "7-b" || res.Offers[1].Name != "Main · B" || strings.Contains(res.Offers[1].URL, "{pack}") || !strings.Contains(res.Offers[1].URL, "{bundle_id}") {
		t.Fatalf("copy = %+v", res.Offers[1])
	}
	ga, _ := findGroup(res.Groups, "a")
	gb, _ := findGroup(res.Groups, "b")
	gc, _ := findGroup(res.Groups, "c")
	if ga.Streams[0].Out.Offers[0].ID != "7" || gc.Streams[0].Out.Offers[0].ID != "7" || gb.Streams[0].Out.Offers[0].ID != "7-b" {
		t.Fatalf("refs a=%s b=%s c=%s", ga.Streams[0].Out.Offers[0].ID, gb.Streams[0].Out.Offers[0].ID, gc.Streams[0].Out.Offers[0].ID)
	}
	if len(res.Landings) != 2 || res.Landings[1].ID != "8-b" {
		t.Fatalf("landings = %+v", res.Landings)
	}
	text := lossesText(res.Losses)
	if strings.Count(text, "оффер 7 скопирован") != 1 || strings.Count(text, "лендинг 8 скопирован") != 1 {
		t.Fatalf("losses %s", text)
	}
	if res.Sources[0].Parameters["sub_id_1"] != "bundle_id" || res.Sources[1].Parameters["sub_id_1"] != "pack" {
		t.Fatalf("sources = %+v %+v", res.Sources[0].Parameters, res.Sources[1].Parameters)
	}
}

func TestSourceNamesDisagree(t *testing.T) {
	a := json.RawMessage(`{"sub_id_1": {"placeholder": "{bundle_id}"}}`)
	b := json.RawMessage(`{"sub_id_1": {"placeholder": "{pack}"}}`)
	snap := Snapshot{
		Campaigns: []Campaign{
			{ID: 1, Alias: "a", State: "active", TrafficSourceID: 1, Parameters: a},
			{ID: 2, Alias: "b", State: "active", TrafficSourceID: 1, Parameters: b},
		},
		Streams: map[string][]KStream{
			"1": {{ID: 11, State: "active", ActionType: "http", ActionPayload: "https://a.example/"}},
			"2": {{ID: 12, State: "active", ActionType: "http", ActionPayload: "https://b.example/"}},
		},
		TrafficSources: []KSource{{ID: 1, Name: "fb"}},
	}
	res := MapAll(snap)
	if res.Sources[0].Parameters["sub_id_1"] != "bundle_id" {
		t.Fatalf("source = %+v", res.Sources[0].Parameters)
	}
	text := lossesText(res.Losses)
	if !strings.Contains(text, "параметр sub_id_1: входящее имя pack не совпало с bundle_id") {
		t.Fatalf("losses %s", text)
	}
}

func TestSourcePlaceholderStripsBraces(t *testing.T) {
	params, losses := sourceParams(KSource{ID: 4, Parameters: map[string]KParam{
		"sub_id_1": {Placeholder: "{bundle_id}"},
		"sub_id_2": {Name: "Bundle ID"},
		"keyword":  {Name: "utm_term"},
	}})
	if params["sub_id_1"] != "bundle_id" || params["keyword"] != "utm_term" {
		t.Fatalf("%+v", params)
	}
	if _, ok := params["sub_id_2"]; ok {
		t.Fatalf("label stored: %+v", params)
	}
	if len(losses) != 0 {
		t.Fatalf("%+v", losses)
	}
}

func TestCampaignParametersArray(t *testing.T) {
	snap := Snapshot{
		Campaigns: []Campaign{{
			ID: 1, Alias: "a", State: "active", Parameters: json.RawMessage(`[]`),
		}},
		Streams: map[string][]KStream{
			"1": {{ID: 1, State: "active", ActionType: "http", ActionPayload: "https://a.example/?x={bundle_id}"}},
		},
	}
	res := MapAll(snap)
	g, _ := findGroup(res.Groups, "a")
	if !strings.Contains(g.Streams[0].Out.Out, "{bundle_id}") {
		t.Fatalf("%+v", g.Streams[0].Out)
	}
	if strings.Contains(lossesText(res.Losses), "параметры кампании") {
		t.Fatalf("%+v", res.Losses)
	}

	bad := snap
	bad.Campaigns = []Campaign{{
		ID: 1, Alias: "a", State: "active", Parameters: json.RawMessage(`{"sub_id_1":`),
	}}
	res = MapAll(bad)
	n := 0
	for _, loss := range res.Losses {
		if loss.Detail == "параметры кампании не переносятся" && loss.Kind == "skipped" && loss.Ref == "a" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("losses %+v", res.Losses)
	}
}
