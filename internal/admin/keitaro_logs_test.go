package admin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/kuztds/bridge/ktimport"
)

func TestKeitaroDevice(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "", want: ""},
		{raw: "   ", want: ""},
		{raw: "mobile phone", want: "phone"},
		{raw: "Mobile", want: "phone"},
		{raw: "PHONE", want: "phone"},
		{raw: "desktop", want: "computer"},
		{raw: "Computer", want: "computer"},
		{raw: "PC", want: "computer"},
		{raw: "tablet", want: "tablet"},
		{raw: "iPad", want: "tablet"},
		{raw: "SmartTV", want: "smarttv"},
	}
	for _, tc := range tests {
		if got := keitaroDevice(tc.raw); got != tc.want {
			t.Fatalf("keitaroDevice(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestKeitaroGroupName(t *testing.T) {
	store := &memGroups{list: []config.Group{
		{ID: "middleware", Name: "Middleware"},
		{ID: "9", Name: "Home"},
	}}
	groups, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := groupNames(groups)
	if names["middleware"] != "Middleware" || names["9"] != "Home" || len(names) != 2 {
		t.Fatalf("map %#v", names)
	}
	place := logPlace{names: names, aliases: map[int]string{4: "middleware"}}

	click := ktimport.ClickRow{Campaign: "Tracker name", CampaignID: 4, OfferID: 12}
	ev := clickEvent(click, "tracker.example", place)
	if ev.GroupID != "middleware" || ev.GroupName != "Middleware" || ev.OfferID != "12" {
		t.Fatalf("click %+v", ev)
	}
	conv := ktimport.ConversionRow{Campaign: "Tracker name", CampaignID: 4, OfferID: 12}
	convID, convName := keitaroGroup(conv.Campaign, int(conv.CampaignID), place)
	got := conversionInsert(conv, "tracker.example", place, nil)
	if convID != "middleware" || convName != "Middleware" || got.GroupName != convName || got.OfferID != "12" {
		t.Fatalf("conversion id=%s name=%s row=%+v", convID, convName, got)
	}

	missClick := ktimport.ClickRow{Campaign: "Unknown", CampaignID: 9}
	ev = clickEvent(missClick, "tracker.example", place)
	if ev.GroupID != "9" || ev.GroupName != "Home" {
		t.Fatalf("id click %+v", ev)
	}
	missConv := ktimport.ConversionRow{Campaign: "Unknown", CampaignID: 9}
	convID, convName = keitaroGroup(missConv.Campaign, int(missConv.CampaignID), place)
	got = conversionInsert(missConv, "tracker.example", place, nil)
	if convID != "9" || convName != "Home" || got.GroupName != convName {
		t.Fatalf("id conversion id=%s name=%s row=%+v", convID, convName, got)
	}

	plain := logPlace{names: names}
	ev = clickEvent(ktimport.ClickRow{Campaign: "Elsewhere", CampaignID: 3}, "tracker.example", plain)
	if ev.GroupID != "3" || ev.GroupName != "Elsewhere" {
		t.Fatalf("fallback click %+v", ev)
	}

	aliased := logPlace{names: names, aliases: map[int]string{4: "from-snapshot"}}
	ev = clickEvent(click, "tracker.example", aliased)
	convID, convName = keitaroGroup(conv.Campaign, int(conv.CampaignID), aliased)
	got = conversionInsert(conv, "tracker.example", aliased, nil)
	if ev.GroupID != "from-snapshot" || ev.GroupName != "Tracker name" ||
		convID != "from-snapshot" || convName != "Tracker name" || got.GroupName != convName {
		t.Fatalf("alias click %+v conversion id=%s name=%s row=%+v", ev, convID, convName, got)
	}

	zeroID, zeroName := keitaroGroup("No campaign", 0, logPlace{})
	if zeroID != "" || zeroName != "No campaign" {
		t.Fatalf("zero campaign id=%q name=%q", zeroID, zeroName)
	}
}

func TestConversionInsertExtended(t *testing.T) {
	raw := []byte(`{"campaign_id":4,"campaign":"Home","status":"Sale","original_status":"Sale","previous_status":" Lead ","tid":99,"language":"pl","os":"Android","os_version":"14","browser":"Chrome","browser_version":"120","device_model":"Pixel","referrer":"https://ex","search_engine":"Google","keyword":"shoes","isp":"Orange","ad_campaign_id":"77","external_id":55,"creative_id":"cr","ip":"8.8.8.8","region":"MZ","user_agent":"UA","click_datetime":"2026-01-02 03:04:05","postback_datetime":"2026-01-02 04:00:00"}`)
	var row ktimport.ConversionRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	id, name := keitaroGroup(row.Campaign, int(row.CampaignID), logPlace{})
	got := conversionInsert(row, "tracker.example", logPlace{}, nil)
	if got.GroupID != id || got.GroupName != name || id != "4" {
		t.Fatalf("group %s %s row %+v", id, name, got)
	}
	if got.OriginalStatus != "Sale" || got.PrevStatus != "lead" || got.Status != "sale" {
		t.Fatalf("status %+v", got)
	}
	if got.TID != "99" || got.Lang != "pl" || got.OS != "Android" || got.OSVersion != "14" || got.Browser != "Chrome" || got.BrowserV != "120" || got.Brand != "" || got.DeviceModel != "Pixel" {
		t.Fatalf("client %+v", got)
	}
	if got.IP != "8.8.8.8" || got.Region != "MZ" || got.UserAgent != "UA" || got.Referer != "https://ex" || got.SE != "Google" || got.Keyword != "shoes" || got.Org != "Orange" {
		t.Fatalf("net %+v", got)
	}
	if got.AdCampaignID != "77" || got.ExternalID != "55" || got.CreativeID != "cr" || got.Site != "" || got.XRW != "" {
		t.Fatalf("ids %+v", got)
	}
	if got.ClickTS.UTC().Format("2006-01-02 15:04:05") != "2026-01-02 03:04:05" {
		t.Fatalf("click ts %s", got.ClickTS)
	}
	plain := conversionInsert(ktimport.ConversionRow{Status: "Hold"}, "tracker.example", logPlace{}, nil)
	if plain.OriginalStatus != "Hold" || plain.PrevStatus != "" {
		t.Fatalf("fallback status %+v", plain)
	}
	var click ktimport.ClickRow
	if err := json.Unmarshal([]byte(`{"ad_campaign_id":12,"creative_id":"c","external_id":"e","device_model":"SM-S918B"}`), &click); err != nil {
		t.Fatal(err)
	}
	ev := clickEvent(click, "tracker.example", logPlace{})
	if ev.DeviceModel != "SM-S918B" || ev.Brand != "" {
		t.Fatalf("click model %q brand %q: the Keitaro export has no brand", ev.DeviceModel, ev.Brand)
	}
	if ev.AdCampaignID != "12" || ev.CreativeID != "c" || ev.ExternalID != "e" || ev.Site != "" || ev.XRW != "" {
		t.Fatalf("click %+v", ev)
	}
}
