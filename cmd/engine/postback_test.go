package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/router"
)

func TestParsePostbackAliases(t *testing.T) {
	cid, profit, status := parsePostback(url.Values{
		"subid": {"A"}, "clickid": {"B"}, "cid": {"C"},
		"status": {"lead"}, "type": {"sale"}, "goal": {"x"},
		"payout": {"1.5"}, "profit": {"9"}, "sum": {"3"},
	})
	if cid != "A" || status != "lead" || profit != 1.5 {
		t.Fatalf("first names win, got %q %q %v", cid, status, profit)
	}
	cid, profit, status = parsePostback(url.Values{"clickid": {"B"}, "type": {"sale"}, "profit": {"9"}})
	if cid != "B" || status != "sale" || profit != 9 {
		t.Fatalf("clickid/type/profit, got %q %q %v", cid, status, profit)
	}
	cid, profit, status = parsePostback(url.Values{"cid": {"C"}, "goal": {"rejected"}, "sum": {"2"}})
	if cid != "C" || status != "rejected" || profit != 2 {
		t.Fatalf("cid/goal/sum, got %q %q %v", cid, status, profit)
	}
}

func TestAppendOfferQueryKeepsIdentity(t *testing.T) {
	raw := "https://offer.example/go?a=1"
	got := appendOfferQuery(raw, "sub1={subid}")
	if got != raw+"&sub1={subid}" {
		t.Fatalf("append = %s", got)
	}
	if appendOfferQuery("https://offer.example/go", "?sub1={subid}") != "https://offer.example/go?sub1={subid}" {
		t.Fatal("query without a question mark was not joined")
	}
	stream := &config.Stream{Out: config.Output{Offers: []config.ShareRef{{ID: "o"}}}}
	cat := router.Catalogs{Offers: map[string]config.Offer{
		"o": {ID: "o", URL: raw, Status: true, NetworkID: "n"},
	}}
	id, _, net := clickIDs(stream, cat, raw, true)
	if id != "o" || net != "n" {
		t.Fatalf("offer identity %q %q", id, net)
	}
	if id2, _, _ := clickIDs(stream, cat, got, true); id2 != "" {
		t.Fatalf("appended url matched an offer: %s", id2)
	}
}

func TestPostbackPathIsNotACampaign(t *testing.T) {
	mux := http.NewServeMux()
	d := &engineDeps{}
	mux.HandleFunc("/postback", d.postback)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequest(http.MethodGet, "/postback?subid=abc&status=lead&payout=1", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/?pb=secret&subid=abc&status=lead&payout=9&cid=real&profit=1", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusTeapot {
		t.Fatalf("old pixel must stay on /, got %d", rr.Code)
	}
}

func TestMarkOutboundOnce(t *testing.T) {
	d := &engineDeps{}
	if !d.markOutbound("c", "lead") || d.markOutbound("c", "lead") {
		t.Fatal("same click and status must queue once")
	}
	if !d.markOutbound("c", "sale") {
		t.Fatal("a different status is a separate row")
	}
}
