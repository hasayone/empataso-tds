package admin

import (
	"net/url"
	"reflect"
	"testing"
)

// TestParseOfferGeo checks the facts query of one offer: no pair keeps the catalog
// facts, an empty country list is accepted and stays empty, one country is kept,
// and two countries are not merged or repeated.
func TestParseOfferGeo(t *testing.T) {
	geo, id, countries, msg := parseOfferGeo(url.Values{})
	if geo || msg != "" || id != "" || countries != nil {
		t.Fatalf("absent: geo=%v id=%q countries=%v msg=%q", geo, id, countries, msg)
	}
	geo, id, countries, msg = parseOfferGeo(url.Values{"offer": {"o1"}, "countries": {""}})
	if !geo || msg != "" || id != "o1" || len(countries) != 0 {
		t.Fatalf("empty list: geo=%v id=%q countries=%v msg=%q", geo, id, countries, msg)
	}
	geo, id, countries, msg = parseOfferGeo(url.Values{"offer": {"o1"}, "countries": {" RU "}})
	if !geo || msg != "" || !reflect.DeepEqual(countries, []string{"ru"}) {
		t.Fatalf("one country: %v %q", countries, msg)
	}
	geo, id, countries, msg = parseOfferGeo(url.Values{"offer": {"o1"}, "countries": {"ru, DE, ru"}})
	if !geo || msg != "" || !reflect.DeepEqual(countries, []string{"ru", "de"}) {
		t.Fatalf("two countries mixed or repeated: %v %q", countries, msg)
	}
	if _, _, _, msg = parseOfferGeo(url.Values{"offer": {"o1"}, "countries": {"russia"}}); msg == "" {
		t.Fatal("a word is not a country code")
	}
	if _, _, _, msg = parseOfferGeo(url.Values{"countries": {"ru,de"}}); msg == "" {
		t.Fatal("countries without an offer")
	}
}
