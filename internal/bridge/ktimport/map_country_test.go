package ktimport

import (
	"strings"
	"testing"
)

// TestCountryCodesUnknownBucket проверяет, что Unknown и пустой код дают отдельную страну без кода, а настоящий мусор остаётся непереведённым.
func TestCountryCodesUnknownBucket(t *testing.T) {
	got, bad := countryCodes([]string{"Unknown", "unknown", " UNKNOWN ", "", "ru", "Russia", "xyzzy"})
	if strings.Join(got, "|") != "unknown|ru" {
		t.Fatalf("countries = %v", got)
	}
	if len(bad) != 1 || bad[0] != "xyzzy" {
		t.Fatalf("bad = %v", bad)
	}
}

// TestMapOffersUnknownIsNotApproximation проверяет, что оффер со страной Unknown хранит её и не пишет приближение в отчёт.
func TestMapOffersUnknownIsNotApproximation(t *testing.T) {
	rows := []Offer{
		{ID: 1, Name: "a", ActionType: "http", ActionPayload: "https://x.test/", Country: FlexStrings{"Unknown", "us"}},
		{ID: 2, Name: "b", ActionType: "http", ActionPayload: "https://x.test/", Country: FlexStrings{"nowhere-land"}},
	}
	offers, losses := mapOffers(rows, nil)
	if len(offers) != 2 || strings.Join(offers[0].Countries, "|") != "unknown|us" {
		t.Fatalf("offers = %+v", offers)
	}
	var unknownLoss, garbageLoss bool
	for _, l := range losses {
		if l.Kind != "approximated" || !strings.Contains(l.Detail, "стран") {
			continue
		}
		if l.Ref == entityRef("offer", 1) {
			unknownLoss = true
		}
		if l.Ref == entityRef("offer", 2) && strings.Contains(l.Detail, "nowhere-land") {
			garbageLoss = true
		}
	}
	if unknownLoss {
		t.Fatalf("Unknown не должен быть приближением: %+v", losses)
	}
	if !garbageLoss {
		t.Fatalf("мусор должен остаться приближением: %+v", losses)
	}
}
