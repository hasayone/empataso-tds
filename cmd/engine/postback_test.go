package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/router"
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

func TestParsePostbackDepSum(t *testing.T) {
	in := parsePostback(url.Values{"subid": {"A"}, "payout": {"4"}, "DepSum": {"2.5"}, "depsum": {"9"}})
	if !in.ok || in.profit != 4 || in.depSum != 2.5 {
		t.Fatalf("deposit is separate from payout: %+v", in)
	}
	in = parsePostback(url.Values{"subid": {"A"}, "payout": {"1"}, "depsum": {"8"}})
	if !in.ok || in.profit != 1 || in.depSum != 8 {
		t.Fatalf("depsum: %+v", in)
	}
	in = parsePostback(url.Values{"subid": {"A"}, "payout": {"3"}})
	if !in.ok || in.profit != 3 || in.depSum != 0 {
		t.Fatalf("empty deposit: %+v", in)
	}
	if parsePostback(url.Values{"subid": {"A"}, "payout": {"3"}, "DepSum": {"NaN"}}).ok {
		t.Fatal("deposit NaN accepted")
	}
	if parsePostback(url.Values{"depsum": {"Inf"}}).ok {
		t.Fatal("deposit Inf accepted")
	}
	if parsePostback(url.Values{"DepSum": {"+Inf"}, "payout": {"1"}}).ok {
		t.Fatal("deposit +Inf accepted")
	}
}

func TestLegacyPixelDoesNotReadDepSum(t *testing.T) {
	raw, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	i := strings.Index(body, `if pb := query.Get("pb")`)
	j := strings.Index(body, "API-client mode")
	if i < 0 || j < i {
		t.Fatal("pixel block not found")
	}
	block := body[i:j]
	if strings.Contains(block, "DepSum") || strings.Contains(block, "depsum") || strings.Contains(block, "parsePostback") {
		t.Fatalf("legacy pixel reads the deposit:\n%s", block)
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
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"status":"error"`) {
		t.Fatalf("body %s", rr.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/?pb=secret&subid=abc&status=lead&payout=9&cid=real&profit=1", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusTeapot {
		t.Fatalf("old pixel must stay on /, got %d", rr.Code)
	}
}

func TestPostbackJSON(t *testing.T) {
	d := &engineDeps{}
	cases := []struct {
		method, raw string
		code        int
		errMsg      string
	}{
		{http.MethodGet, "/postback?status=lead&payout=1", http.StatusBadRequest, "subid is required"},
		{http.MethodGet, "/postback?subid=abc&payout=NaN", http.StatusBadRequest, "invalid payout"},
		{http.MethodGet, "/postback?subid=abc&status=rejected&payout=0", http.StatusServiceUnavailable, "store unavailable"},
		{http.MethodPut, "/postback?subid=abc", http.StatusMethodNotAllowed, "method not allowed"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.raw, nil)
		rr := httptest.NewRecorder()
		d.postback(rr, req)
		if rr.Code != tc.code {
			t.Fatalf("%s %s status %d", tc.method, tc.raw, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("content-type %q", ct)
		}
		var out postbackOut
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Status != "error" || out.Error != tc.errMsg {
			t.Fatalf("%s got %+v", tc.raw, out)
		}
	}
}

func TestPostbackResult(t *testing.T) {
	code, outcome, msg := postbackResult("abc", true, nil)
	if code != http.StatusOK || outcome != "ok" || msg != "" {
		t.Fatalf("stored %d %s %q", code, outcome, msg)
	}
	code, outcome, msg = postbackResult("abc", true, sql.ErrNoRows)
	if code != http.StatusNotFound || outcome != "click_not_found" || msg != "click not found" {
		t.Fatalf("missing click %d %s %q", code, outcome, msg)
	}
	code, outcome, msg = postbackResult("abc", true, errors.New("timeout"))
	if code != http.StatusServiceUnavailable || outcome != "store_error" || msg != "store unavailable" {
		t.Fatalf("store %d %s %q", code, outcome, msg)
	}
	rr := httptest.NewRecorder()
	writePostback(rr, http.StatusOK, "")
	var out postbackOut
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "success" || out.Error != "" {
		t.Fatalf("success body %+v", out)
	}
}

func TestTakeOutboundOnce(t *testing.T) {
	d := &engineDeps{}
	if !d.takeOutbound(context.Background(), "c", "lead") || d.takeOutbound(context.Background(), "c", "lead") {
		t.Fatal("same click and status must queue once")
	}
	if !d.takeOutbound(context.Background(), "c", "sale") {
		t.Fatal("a different status is a separate row")
	}
}
