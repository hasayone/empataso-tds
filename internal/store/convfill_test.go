package store

import (
	"testing"
	"time"
)

func TestFillFromClicksPostback(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := []PostbackRow{{
		CID:     "a",
		ClickTS: time.Unix(0, 0).UTC(),
	}}
	fillFromClicks(rows, map[string]ClickFill{
		"a": {
			TS: ts, GroupID: "g1", GroupName: "Promo", IP: "1.2.3.4", UserAgent: "ua",
			OS: "Linux", Browser: "Firefox", LandingID: "9",
		},
	})
	p := rows[0]
	if p.Group != "Promo" {
		t.Fatalf("campaign name not filled: %+v", p)
	}
	if p.CampaignID != "g1" || !p.ClickTS.Equal(ts) || p.IP != "1.2.3.4" ||
		p.UserAgent != "ua" || p.OS != "Linux" || p.Browser != "Firefox" || p.Landing != "9" {
		t.Fatalf("filled %+v", p)
	}

	rows = []PostbackRow{{
		CID: "b", CampaignID: "keep", Group: "Mine", IP: "9.9.9.9", ClickTS: ts,
		OS: "iOS", Browser: "Safari", Landing: "1", UserAgent: "mine",
	}}
	fillFromClicks(rows, map[string]ClickFill{
		"b": {GroupID: "other", GroupName: "Other", IP: "0.0.0.0", OS: "Android", Browser: "Chrome", LandingID: "2", UserAgent: "other"},
	})
	p = rows[0]
	if p.CampaignID != "keep" || p.Group != "Mine" || p.IP != "9.9.9.9" || p.OS != "iOS" ||
		p.Browser != "Safari" || p.Landing != "1" || p.UserAgent != "mine" {
		t.Fatalf("must not overwrite %+v", p)
	}
}

func TestFillConversionInsert(t *testing.T) {
	row := ConversionInsert{CID: "x", Status: "lead"}
	snap := ClickFill{IP: "8.8.8.8", GroupID: "g", GroupName: "Promo"}
	FillConversionInsert(&row, &snap)
	if row.IP != "8.8.8.8" || row.GroupID != "g" || row.GroupName != "Promo" {
		t.Fatalf("insert %+v", row)
	}
	FillConversionInsert(&row, &ClickFill{GroupName: "Other"})
	if row.GroupName != "Promo" {
		t.Fatal("campaign name overwritten")
	}
	row.IP = "1.1.1.1"
	FillConversionInsert(&row, &ClickFill{IP: "8.8.8.8"})
	if row.IP != "1.1.1.1" {
		t.Fatal("overwrite")
	}
	FillConversionInsert(&row, nil)
}

func TestFillOriginOnlyWhenEmpty(t *testing.T) {
	rows := []PostbackRow{{CID: "a"}}
	fillFromClicks(rows, map[string]ClickFill{
		"a": {OriginGroupID: "entry", OriginGroupName: "Entry"},
	})
	if rows[0].OriginCampaignID != "entry" || rows[0].OriginCampaign != "Entry" {
		t.Fatalf("filled %+v", rows[0])
	}
	rows = []PostbackRow{{CID: "b", OriginCampaign: "Keep", OriginCampaignID: "keep"}}
	fillFromClicks(rows, map[string]ClickFill{"b": {OriginGroupID: "other", OriginGroupName: "Other"}})
	if rows[0].OriginCampaign != "Keep" || rows[0].OriginCampaignID != "keep" {
		t.Fatalf("overwritten %+v", rows[0])
	}
}

func TestCidsNeedingClickFill(t *testing.T) {
	rows := []PostbackRow{
		{CID: "a", IP: "1.1.1.1", ClickTS: time.Now(), CampaignID: "1", Group: "n", UserAgent: "u", OS: "o", Browser: "b", Landing: "l"},
		{CID: "n", IP: "1.1.1.1", ClickTS: time.Now(), CampaignID: "1", UserAgent: "u", OS: "o", Browser: "b", Landing: "l"},
		{CID: "b", IP: ""},
		{CID: "c", IP: ""},
		{CID: "", IP: ""},
	}
	got := cidsNeedingClickFill(rows)
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("cids %v", got)
	}
}
