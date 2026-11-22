package main

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestParsePostbackDep2Sum checks that dep2sum is not a second deposit: depsum is the only deposit,
// dep2sum and Dep2Sum do not change it and are not stored, a missing depsum stays 0, and NaN in depsum
// is still refused. The payout is left alone.
func TestParsePostbackDep2Sum(t *testing.T) {
	cases := []struct {
		name  string
		query url.Values
		dep   float64
		ok    bool
	}{
		{"depsum is the deposit", url.Values{"subid": {"A"}, "payout": {"4"}, "depsum": {"2.5"}, "dep2sum": {"7"}}, 2.5, true},
		{"dep2sum alone is ignored", url.Values{"subid": {"A"}, "dep2sum": {"6.25"}}, 0, true},
		{"Dep2Sum alone is ignored", url.Values{"subid": {"A"}, "Dep2Sum": {"9"}}, 0, true},
		{"absent deposit stays zero", url.Values{"subid": {"A"}, "payout": {"3"}}, 0, true},
		{"empty depsum stays zero", url.Values{"subid": {"A"}, "depsum": {"  "}}, 0, true},
		{"depsum that is not a number stays zero", url.Values{"subid": {"A"}, "depsum": {"abc"}}, 0, true},
		{"NaN depsum is refused", url.Values{"subid": {"A"}, "depsum": {"NaN"}}, 0, false},
		{"NaN dep2sum is not read", url.Values{"subid": {"A"}, "dep2sum": {"NaN"}, "depsum": {"1"}}, 1, true},
	}
	for _, c := range cases {
		in := parsePostback(c.query)
		if in.ok != c.ok {
			t.Errorf("%s: ok %v, want %v", c.name, in.ok, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		if in.depSum != c.dep || in.dep2Sum != 0 {
			t.Errorf("%s: deposit %v dep2 %v, want %v and 0", c.name, in.depSum, in.dep2Sum, c.dep)
		}
	}
	if in := parsePostback(url.Values{"subid": {"A"}, "payout": {"4"}, "depsum": {"7"}}); in.profit != 4 || in.depSum != 7 {
		t.Errorf("the deposit changed the payout or was dropped: %+v", in)
	}
}

// TestPostbackWritesDep2Sum checks the real /postback path: depsum is stored as the deposit, dep2sum is
// not stored, and the same holds when the postback waits in the disk queue.
func TestPostbackWritesDep2Sum(t *testing.T) {
	cs := &fakeConv{}
	d := queueEnv(t, cs)
	if rec := sendPostback(d, "/postback?subid=abc&status=sale&payout=5&depsum=2&dep2sum=11.5"); rec.Code != 200 {
		t.Fatalf("answer %d %s", rec.Code, rec.Body.String())
	}
	if rec := sendPostback(d, "/postback?subid=abc&status=sale&payout=5&dep2sum=11.5"); rec.Code != 200 {
		t.Fatalf("answer %d %s", rec.Code, rec.Body.String())
	}
	if len(cs.calls) != 2 {
		t.Fatalf("store calls %d, want 2", len(cs.calls))
	}
	if got := cs.calls[0]; got.DepSum != 2 || got.Dep2Sum != 0 || got.Profit != 5 {
		t.Errorf("depsum must be the only deposit: %+v", got)
	}
	if got := cs.calls[1]; got.DepSum != 0 || got.Dep2Sum != 0 || got.Profit != 5 {
		t.Errorf("dep2sum must not invent a deposit: %+v", got)
	}

	failing := &fakeConv{err: errors.New("boom")}
	q := queueEnv(t, failing)
	if rec := sendPostback(q, "/postback?subid=abc&status=lead&depsum=4.5&dep2sum=9"); rec.Code != 200 {
		t.Fatalf("queued answer %d %s", rec.Code, rec.Body.String())
	}
	in := newSpooledPostback(parsePostback(url.Values{"subid": {"abc"}, "depsum": {"4.5"}, "dep2sum": {"9"}}), url.Values{}, time.Now()).storeIn(nil)
	if in.DepSum != 4.5 || in.Dep2Sum != 0 {
		t.Errorf("a queued postback stored the wrong deposit: %+v", in)
	}
}

// TestLegacyPixelDoesNotReadDep2Sum keeps the old /?pb= pixel on cid, profit and status: it reads neither deposit.
func TestLegacyPixelDoesNotReadDep2Sum(t *testing.T) {
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
	block := strings.ToLower(body[i:j])
	if strings.Contains(block, "dep2sum") || strings.Contains(block, "depsum") {
		t.Fatalf("legacy pixel reads a deposit:\n%s", body[i:j])
	}
}
