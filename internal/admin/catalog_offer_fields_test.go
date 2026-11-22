package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// TestOfferNotesValuesAndCapPersist checks over the API that the notes, the
// named values and the daily limit are saved with the offer and read back, that
// a value with an empty name is dropped and that the limit number stays
// while the limit is off.
func TestOfferNotesValuesAndCapPersist(t *testing.T) {
	srv, _, _ := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	offer := config.Offer{
		ID: "o-rich", Name: "Rich", URL: "https://offer.example/r?p={offer_value:promo}", Status: true,
		Notes:  "line one\nline two",
		Values: []config.OfferValue{{Name: " promo ", Value: "spring"}, {Name: "  ", Value: "dropped"}},
		ConvCap: true, ConvCapLimit: 25,
	}
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/offers", csrf, offer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d", resp.StatusCode)
	}
	resp.Body.Close()
	read := func() config.Offer {
		t.Helper()
		resp := csrfDo(t, c, http.MethodGet, srv.URL+"/api/offers/o-rich", csrf, nil)
		defer resp.Body.Close()
		var got config.Offer
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := read()
	if got.Notes != "line one\nline two" || len(got.Values) != 1 || got.Values[0].Name != "promo" ||
		got.Values[0].Value != "spring" || !got.ConvCap || got.ConvCapLimit != 25 {
		t.Fatalf("stored offer = %+v", got)
	}

	offer.ConvCap = false
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/offers/o-rich", csrf, offer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := read(); got.ConvCap || got.ConvCapLimit != 25 {
		t.Fatalf("turning the limit off dropped its number: %+v", got)
	}
}

// TestOfferFallbackPersists checks over the API that the spare offer is saved with the offer and read back,
// and that clearing it stores an empty value.
func TestOfferFallbackPersists(t *testing.T) {
	srv, _, _ := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")

	offer := config.Offer{ID: "o-main", Name: "Main", URL: "https://offer.example/m", Status: true, FallbackID: "o-spare"}
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/offers", csrf, offer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d", resp.StatusCode)
	}
	resp.Body.Close()
	read := func() config.Offer {
		t.Helper()
		resp := csrfDo(t, c, http.MethodGet, srv.URL+"/api/offers/o-main", csrf, nil)
		defer resp.Body.Close()
		var got config.Offer
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); got.FallbackID != "o-spare" {
		t.Fatalf("stored spare offer = %q", got.FallbackID)
	}
	offer.FallbackID = ""
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/offers/o-main", csrf, offer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := read(); got.FallbackID != "" {
		t.Fatalf("a cleared spare offer stayed: %q", got.FallbackID)
	}
}

// TestRelinkOfferFallbacks checks that a copied offer points at the copy of its spare offer when that one was
// copied in the same request, loses the link when it was not, and that offers that are not copies are untouched.
func TestRelinkOfferFallbacks(t *testing.T) {
	list := []config.Offer{
		{ID: "a", FallbackID: "b"}, {ID: "b"}, {ID: "z", FallbackID: "q"},
		{ID: "a2", FallbackID: "b"}, {ID: "b2"}, {ID: "c2", FallbackID: "outside"},
	}
	relinkOfferFallbacks(list, map[string]string{"a": "a2", "b": "b2", "c": "c2"})
	got := map[string]string{}
	for _, o := range list {
		got[o.ID] = o.FallbackID
	}
	if got["a2"] != "b2" || got["c2"] != "" || got["b2"] != "" {
		t.Fatalf("copies = %v", got)
	}
	if got["a"] != "b" || got["z"] != "q" {
		t.Fatalf("an original changed: %v", got)
	}
}

// TestOfferFieldsValidation checks what the API refuses: a limit that is on
// without a number, an out-of-range number, repeated or brace-bearing value
// names, too many values and notes beyond the limit.
func TestOfferFieldsValidation(t *testing.T) {
	long := strings.Repeat("x", config.MaxOfferNotes+1)
	many := make([]config.OfferValue, config.MaxOfferValues+1)
	for i := range many {
		many[i] = config.OfferValue{Name: "n" + string(rune('a'+i%26)) + strings.Repeat("z", i/26)}
	}
	cases := []struct {
		name string
		o    config.Offer
		bad  bool
	}{
		{"plain", config.Offer{ID: "a", URL: "https://x.example"}, false},
		{"cap on with number", config.Offer{ID: "a", URL: "https://x.example", ConvCap: true, ConvCapLimit: 1}, false},
		{"cap on without number", config.Offer{ID: "a", URL: "https://x.example", ConvCap: true}, true},
		{"cap number negative", config.Offer{ID: "a", URL: "https://x.example", ConvCap: true, ConvCapLimit: -1}, true},
		{"cap number too big", config.Offer{ID: "a", URL: "https://x.example", ConvCap: true, ConvCapLimit: config.MaxConvCapLimit + 1}, true},
		{"total on with number", config.Offer{ID: "a", URL: "https://x.example", ConvTotal: true, ConvTotalLimit: 2, ConvTotalOfferID: "b"}, false},
		{"total on without number", config.Offer{ID: "a", URL: "https://x.example", ConvTotal: true}, true},
		{"cap offer is itself", config.Offer{ID: "a", URL: "https://x.example", ConvCap: true, ConvCapLimit: 1, ConvCapOfferID: "a"}, true},
		{"total offer is itself", config.Offer{ID: "a", URL: "https://x.example", ConvTotal: true, ConvTotalLimit: 1, ConvTotalOfferID: "a"}, true},
		{"spare offer", config.Offer{ID: "a", URL: "https://x.example", FallbackID: "b"}, false},
		{"spare offer is itself", config.Offer{ID: "a", URL: "https://x.example", FallbackID: "a"}, true},
		{"spare offer id not valid", config.Offer{ID: "a", URL: "https://x.example", FallbackID: "b c"}, true},
		{"repeated name", config.Offer{ID: "a", URL: "https://x.example", Values: []config.OfferValue{{Name: "p"}, {Name: "p"}}}, true},
		{"brace in name", config.Offer{ID: "a", URL: "https://x.example", Values: []config.OfferValue{{Name: "p}"}}}, true},
		{"empty name", config.Offer{ID: "a", URL: "https://x.example", Values: []config.OfferValue{{Name: ""}}}, true},
		{"long value", config.Offer{ID: "a", URL: "https://x.example", Values: []config.OfferValue{{Name: "p", Value: strings.Repeat("v", config.MaxOfferValueText+1)}}}, true},
		{"too many values", config.Offer{ID: "a", URL: "https://x.example", Values: many}, true},
		{"notes too long", config.Offer{ID: "a", URL: "https://x.example", Notes: long}, true},
		{"action 404 without url", config.Offer{ID: "a", Action: "404"}, false},
		{"action text with body", config.Offer{ID: "a", Action: "text", ActionBody: "hi"}, false},
		{"action html with body", config.Offer{ID: "a", Action: "html", ActionBody: "<p>x</p>"}, false},
		{"action none without url", config.Offer{ID: "a", Action: "none"}, false},
		{"action text empty", config.Offer{ID: "a", URL: "https://x.example", Action: "text"}, true},
		{"action html blank", config.Offer{ID: "a", Action: "html", ActionBody: "  "}, true},
		{"action unknown", config.Offer{ID: "a", URL: "https://x.example", Action: "campaign"}, true},
		{"action body too long", config.Offer{ID: "a", Action: "text", ActionBody: strings.Repeat("x", config.MaxOfferActionBody+1)}, true},
	}
	for _, c := range cases {
		msg := validOffer(c.o)
		if (msg != "") != c.bad {
			t.Errorf("%s: validOffer = %q, bad = %v", c.name, msg, c.bad)
		}
	}
}

// TestCapLimitPersists checks that a saved daily limit stays when the total limit is written,
// and that switching a limit off keeps its number and drops its old hand-over offer.
func TestCapLimitPersists(t *testing.T) {
	srv, _, _ := catalogServer(t)
	c := newClient(t)
	_, csrf := login(t, c, srv.URL, "admin", "p@ss")
	offer := config.Offer{
		ID: "o-lim", Name: "Lim", URL: "https://offer.example/lim", Status: true,
		ConvCap: true, ConvCapLimit: 25, ConvCapOfferID: "o-day",
	}
	resp := csrfDo(t, c, http.MethodPost, srv.URL+"/api/offers", csrf, offer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %d", resp.StatusCode)
	}
	resp.Body.Close()
	read := func() config.Offer {
		t.Helper()
		resp := csrfDo(t, c, http.MethodGet, srv.URL+"/api/offers/o-lim", csrf, nil)
		defer resp.Body.Close()
		var got config.Offer
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	offer.ConvTotal, offer.ConvTotalLimit, offer.ConvTotalOfferID = true, 10, "o-all"
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/offers/o-lim", csrf, offer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put total %d", resp.StatusCode)
	}
	resp.Body.Close()
	got := read()
	if !got.ConvCap || got.ConvCapLimit != 25 || got.ConvCapOfferID != "o-day" {
		t.Fatalf("the daily limit was cleared: %+v", got)
	}
	if !got.ConvTotal || got.ConvTotalLimit != 10 || got.ConvTotalOfferID != "o-all" {
		t.Fatalf("the total limit was not stored: %+v", got)
	}
	offer.ConvTotal, offer.ConvTotalLimit, offer.ConvTotalOfferID = false, 10, "o-all"
	resp = csrfDo(t, c, http.MethodPut, srv.URL+"/api/offers/o-lim", csrf, offer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put total off %d", resp.StatusCode)
	}
	resp.Body.Close()
	got = read()
	if !got.ConvCap || got.ConvCapLimit != 25 || got.ConvCapOfferID != "o-day" {
		t.Fatalf("turning the total off cleared the daily limit: %+v", got)
	}
	if got.ConvTotal || got.ConvTotalLimit != 10 || got.ConvTotalOfferID != "" {
		t.Fatalf("a total limit that is off lost its number or kept its offer: %+v", got)
	}
}

// TestPrepOfferDropsEmptyValueNames checks that the trimming step keeps only named values.
func TestPrepOfferDropsEmptyValueNames(t *testing.T) {
	o := config.Offer{Values: []config.OfferValue{{Name: " a ", Value: "1"}, {Name: ""}, {Name: "   ", Value: "2"}}, ConvCapLimit: 9}
	prepOffer(&o)
	if len(o.Values) != 1 || o.Values[0].Name != "a" {
		t.Fatalf("values = %+v", o.Values)
	}
	if o.ConvCapLimit != 9 {
		t.Fatalf("limit number dropped with the limit off: %d", o.ConvCapLimit)
	}
}
