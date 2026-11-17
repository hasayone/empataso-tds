package admin

import "testing"

// TestOfferCountriesAcceptUnknown проверяет, что страна без кода unknown проходит проверку оффера, а другие не-коды нет.
func TestOfferCountriesAcceptUnknown(t *testing.T) {
	if msg := validOfferCountries([]string{"ru", "unknown", "Unknown"}); msg != "" {
		t.Fatalf("unknown должен приниматься: %s", msg)
	}
	if msg := validOfferCountries([]string{"nowhere"}); msg == "" {
		t.Fatal("мусор должен отклоняться")
	}
}
