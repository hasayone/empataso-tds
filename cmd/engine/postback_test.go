package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/router"
)

func TestParsePostbackRejectsNaN(t *testing.T) {
	if parsePostback(url.Values{"subid": {"A"}, "payout": {"NaN"}}).ok {
		t.Fatal("nan accepted")
	}
	if parsePostback(url.Values{"subid": {"A"}, "payout": {"Inf"}}).ok {
		t.Fatal("inf accepted")
	}
}

func TestParsePostbackAliases(t *testing.T) {
	in := parsePostback(url.Values{
		"subid": {"A"}, "clickid": {"B"}, "cid": {"C"},
		"status": {"lead"}, "type": {"sale"}, "goal": {"x"},
		"payout": {"1.5"}, "profit": {"9"}, "sum": {"3"},
	})
	if !in.ok || in.cid != "A" || in.status != "lead" || in.profit != 1.5 {
		t.Fatalf("first names win, got %q %q %v", in.cid, in.status, in.profit)
	}
	in = parsePostback(url.Values{"clickid": {"B"}, "type": {"sale"}, "profit": {"9"}})
	if in.cid != "B" || in.status != "sale" || in.profit != 9 {
		t.Fatalf("clickid/type/profit, got %q %q %v", in.cid, in.status, in.profit)
	}
	in = parsePostback(url.Values{"cid": {"C"}, "goal": {"rejected"}, "sum": {"2"}})
	if in.cid != "C" || in.status != "rejected" || in.profit != 2 {
		t.Fatalf("cid/goal/sum, got %q %q %v", in.cid, in.status, in.profit)
	}
}

func TestParsePostbackKeepsOriginalAndTID(t *testing.T) {
	in := parsePostback(url.Values{"subid": {"A"}, "status": {"Approved"}, "tid": {"  tx-1  "}})
	if !in.ok || in.status != "approved" || in.original != "Approved" || in.tid != "tx-1" {
		t.Fatalf("%+v", in)
	}
	longStatus := strings.Repeat("Z", 80)
	longTID := strings.Repeat("t", 200)
	in = parsePostback(url.Values{"status": {longStatus}, "tid": {longTID}})
	if in.status != strings.ToLower(longStatus) || in.original != longStatus[:64] || len(in.tid) != 128 {
		t.Fatalf("clip status %d original %d tid %d", len(in.status), len(in.original), len(in.tid))
	}
}

func TestLegacyPixelDoesNotReadTID(t *testing.T) {
	h := testEnv(t, group(), func(d *engineDeps) { d.postbackKey = "secret" })
	rec := do(t, h, "/?pb=secret&cid=abc&profit=1&status=Approved&tid=tx-9", "8.8.8.8", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pixel status %d", rec.Code)
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
