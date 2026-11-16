package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/logbuf"
	"github.com/emtds/bridge/ktimport"
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

	tally := &unknownCampaignTally{}
	placeUnknown := logPlace{names: names, aliases: place.aliases, unknown: tally}
	missClick := ktimport.ClickRow{Campaign: "Unknown", CampaignID: 9}
	ev = clickEvent(missClick, "tracker.example", placeUnknown)
	if ev.GroupID != "9" || ev.GroupName != "Home" || tally.rows != 1 {
		t.Fatalf("id click %+v tally %+v", ev, tally)
	}
	var losses []ktimport.Loss
	noteUnknownCampaignLoss(&losses, tally)
	if len(losses) != 1 || losses[0].Ref != "log_campaign" || !strings.Contains(losses[0].Detail, "9") {
		t.Fatalf("losses %+v", losses)
	}
	missConv := ktimport.ConversionRow{Campaign: "Unknown", CampaignID: 9}
	convID, convName = keitaroGroup(missConv.Campaign, int(missConv.CampaignID), place)
	got = conversionInsert(missConv, "tracker.example", place, nil)
	if convID != "9" || convName != "Home" || got.GroupName != convName {
		t.Fatalf("id conversion id=%s name=%s row=%+v", convID, convName, got)
	}

	plain := logPlace{names: names}
	ev = clickEvent(ktimport.ClickRow{Campaign: "Elsewhere", CampaignID: 3}, "tracker.example", plain)
	if ev.GroupID != "" || ev.GroupName != "Elsewhere" {
		t.Fatalf("missing alias map click %+v", ev)
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

func TestNoteUnknownCampaignLossTruncates(t *testing.T) {
	tally := &unknownCampaignTally{}
	for i := 1; i <= 25; i++ {
		tally.note(i)
	}
	var losses []ktimport.Loss
	noteUnknownCampaignLoss(&losses, tally)
	if len(losses) != 1 || losses[0].Ref != "log_campaign" || !strings.Contains(losses[0].Detail, "25 rows") || !strings.Contains(losses[0].Detail, "и ещё 5") {
		t.Fatalf("loss %+v", losses)
	}
}

func TestConversionInsertExtended(t *testing.T) {
	raw := []byte(`{"campaign_id":4,"campaign":"Home","status":"Sale","original_status":"Sale","previous_status":" Lead ","tid":99,"language":"pl","os":"Android","os_version":"14","browser":"Chrome","browser_version":"120","device_model":"Pixel","referrer":"https://ex","search_engine":"Google","keyword":"shoes","isp":"Orange","ad_campaign_id":"77","external_id":55,"creative_id":"cr","ip":"8.8.8.8","region":"MZ","user_agent":"UA","click_datetime":"2026-01-02 03:04:05","postback_datetime":"2026-01-02 04:00:00"}`)
	var row ktimport.ConversionRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	place := logPlace{aliases: map[int]string{4: "home"}}
	id, name := keitaroGroup(row.Campaign, int(row.CampaignID), place)
	got := conversionInsert(row, "tracker.example", place, nil)
	if got.GroupID != id || got.GroupName != name || id != "home" {
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

// TestClickEventDestination — the tracker destination lands in out; a landing marker becomes redirect landing.
func TestClickEventDestination(t *testing.T) {
	long := "https://offer.example/?q=" + strings.Repeat("x", 3000)
	tests := []struct {
		name, raw, out, redirect string
	}{
		{"offer url", `{"sub_id":"a","destination":"https://go.example/click?pid=1&sub1=k"}`, "https://go.example/click?pid=1&sub1=k", ""},
		{"landing marker", `{"sub_id":"b","landing_id":"7","destination":"LP"}`, "7", "landing"},
		{"landing without destination", `{"sub_id":"c","landing_id":7}`, "7", "landing"},
		{"landing with offer url", `{"sub_id":"d","landing_id":7,"destination":" https://go.example/o "}`, "https://go.example/o", ""},
		{"marker without landing", `{"sub_id":"e","destination":"LP"}`, "LP", ""},
		{"empty", `{"sub_id":"f"}`, "", ""},
		{"long url", `{"sub_id":"g","destination":"` + long + `"}`, long[:logbuf.MaxOut], ""},
	}
	for _, tc := range tests {
		var row ktimport.ClickRow
		if err := json.Unmarshal([]byte(tc.raw), &row); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		ev := clickEvent(row, "tracker.example", logPlace{})
		if ev.Out != tc.out || ev.Redirect != tc.redirect {
			t.Fatalf("%s: out %q redirect %q, want %q %q", tc.name, ev.Out, ev.Redirect, tc.out, tc.redirect)
		}
	}
}
