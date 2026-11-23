package cron

import (
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/store"
)

// TestConversionTextSubIDAndCID checks that [SUBID] and [CID] expand to one click id.
func TestConversionTextSubIDAndCID(t *testing.T) {
	p := store.PostbackRow{CID: "click-1", Profit: 1.5}
	got := conversionText("a=[SUBID]&b=[CID]&p=[PROFIT]", p)
	if got != "a=click-1&b=click-1&p=1.50" {
		t.Fatalf("got %q", got)
	}
}

// TestConversionTextCampaignAndKey checks the payout, the campaign name and the keyword.
func TestConversionTextCampaignAndKey(t *testing.T) {
	p := store.PostbackRow{Profit: 2, Group: "Promo", Stream: "s", Country: "ru", Device: "phone", Keyword: "shoes"}
	got := conversionText("[PROFIT] [CAMPAIGN] [GROUP] [STREAM] [COUNTRY] [DEVICE] [KEY] [FOLDER]", p)
	if got != "2.00 Promo Promo s RU phone shoes [FOLDER]" {
		t.Fatalf("got %q", got)
	}
}

// TestConvSummaryRussianProfile checks the period report in Russian.
func TestConvSummaryRussianProfile(t *testing.T) {
	rows := []store.PostbackRow{
		{Profit: 10, Group: "Promo", Stream: "ru_mobile", Country: "ru", Device: "phone", Keyword: "shoes"},
		{Profit: 2.5, Group: "Other", Stream: "us_all", Country: "us", Device: "computer", Keyword: "hats"},
	}
	got := profitSummaryText("today", time.Time{}, time.Time{}, 12.5, rows, "ru")
	want := strings.Join([]string{
		"Профит за сегодня",
		"12.50 $",
		"",
		"Кампании",
		"Promo — 10.00 $",
		"Other — 2.50 $",
		"",
		"Потоки",
		"ru_mobile — 10.00 $",
		"us_all — 2.50 $",
		"",
		"Страны",
		"RU — 10.00 $",
		"US — 2.50 $",
		"",
		"Устройства",
		"телефон — 10.00 $",
		"компьютер — 2.50 $",
		"",
		"Ключи",
		"shoes — 10.00 $",
		"hats — 2.50 $",
	}, "\n")
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "Profit for today") {
		t.Fatal(got)
	}
}
