package config

import (
	"net/url"
	"strings"
	"testing"
)

func TestApplySourceMapsPartnerName(t *testing.T) {
	src := &TrafficSource{Parameters: map[string]string{"sub_id_1": "click_id"}}
	_, subs := ApplySource(url.Values{"click_id": {"ZZ"}}, src)
	if subs[0] != "ZZ" {
		t.Fatalf("sub_id_1 = %q", subs[0])
	}
}

func TestSourceExtrasMappedAndPlain(t *testing.T) {
	q := url.Values{
		"utm_source":     {" shop "},
		"site":           {"plain"},
		"ad_campaign_id": {" camp "},
		"creative_id":    {"cr"},
		"external_id":    {"ex"},
	}
	src := &TrafficSource{Parameters: map[string]string{"site": "utm_source"}}
	got := SourceExtras(q, src)
	if got.Site != "shop" || got.AdCampaignID != "camp" || got.CreativeID != "cr" || got.ExternalID != "ex" {
		t.Fatalf("mapped %+v", got)
	}
	plain := SourceExtras(q, nil)
	if plain.Site != "plain" || plain.AdCampaignID != "camp" {
		t.Fatalf("nil source %+v", plain)
	}
	blank := SourceExtras(q, &TrafficSource{Parameters: map[string]string{"site": "  "}})
	if blank.Site != "plain" {
		t.Fatalf("blank mapping %+v", blank)
	}
	long := "  " + strings.Repeat("x", 300)
	clipped := SourceExtras(url.Values{"site": {long}}, nil)
	if len(clipped.Site) != 255 || clipped.Site != strings.Repeat("x", 255) {
		t.Fatalf("len %d", len(clipped.Site))
	}
}

func TestAllowedSourceParamExtras(t *testing.T) {
	if !AllowedSourceParam("ad_campaign_id") || !AllowedSourceParam("keyword") || !AllowedSourceParam("sub_id_1") {
		t.Fatal("known names rejected")
	}
	for _, name := range ExtraSourceParams {
		if !AllowedSourceParam(name) {
			t.Fatalf("%s rejected", name)
		}
	}
	if AllowedSourceParam("cost") || AllowedSourceParam("sub_id_16") {
		t.Fatal("cost or sub_id_16 accepted")
	}
}

func TestIncomingCostSkipsTheManualWindow(t *testing.T) {
	cents, cur, ok := IncomingCost(url.Values{"price": {"1.25"}, "cur": {"usd"}}, "price", "cur")
	if !ok || cents != 125 || cur != "USD" {
		t.Fatalf("priced click: %d %s %v", cents, cur, ok)
	}
	// A click that already has a price is not a weight of the manual window.
	weights := []SpendWeight{{Stream: "plain", Hits: 4}}
	got := Allocate(400, weights, false)
	if len(got) != 1 || got[0].Stream != "plain" || got[0].Cents != 400 {
		t.Fatalf("window included a priced click: %+v", got)
	}
	if _, _, ok := IncomingCost(url.Values{"price": {"1.25"}, "cur": {"XXX"}}, "price", "cur"); ok {
		t.Fatal("unknown currency counted as a price")
	}
	if _, _, ok := IncomingCost(url.Values{"price": {"1.25"}}, "", "cur"); ok {
		t.Fatal("empty cost key counted as a price")
	}
}
