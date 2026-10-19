package admin

import (
	"testing"

	"github.com/kuztds/bridge/ktimport"
)

// importedClickRow is a tracker click that carries an offer and a landing, as a click
// that went through the /offer step of the source tracker would.
func importedClickRow() ktimport.ClickRow {
	return ktimport.ClickRow{
		SubID: "sub1", Datetime: "2026-10-01 10:00:00", CampaignID: 7, Campaign: "Promo",
		Stream: "s1", OfferID: 3, LandingID: 4, NetworkID: 5, SourceID: 6,
		IP: "1.2.3.4", IsUnique: true,
	}
}

// An imported click has no /offer step: it is written as step 0, even when the
// tracker row names an offer, so it counts in every campaign unique summary.
// The writer side is TestImportedEventsAreStepZero in package store.
func TestImportedClickIsStepZero(t *testing.T) {
	ev := clickEvent(importedClickRow(), "tracker.example", logPlace{})
	if ev.OfferID != "3" || ev.LandingID != "4" {
		t.Fatalf("the row must carry its offer and landing: offer=%q landing=%q", ev.OfferID, ev.LandingID)
	}
	if ev.Step != 0 || ev.Uniq != 1 {
		t.Fatalf("imported click: step=%d uniq=%d, want 0 and 1", ev.Step, ev.Uniq)
	}
}

// The click snapshot that backfills imported conversions carries the campaign id and name
// of the imported event, so a conversion without a campaign name gets both.
func TestClickFillFromEventCarriesCampaign(t *testing.T) {
	place := logPlace{aliases: map[int]string{7: "promo"}, names: map[string]string{"promo": "Promo stored"}}
	ev := clickEvent(importedClickRow(), "tracker.example", place)
	fill := clickFillFromEvent(ev)
	if fill.GroupID != "promo" || fill.GroupName != "Promo stored" {
		t.Fatalf("fill group id=%q name=%q", fill.GroupID, fill.GroupName)
	}
}
