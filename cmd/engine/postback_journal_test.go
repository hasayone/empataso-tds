package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/store"
)

// TestJournalSecretName checks which parameter names count as secrets.
func TestJournalSecretName(t *testing.T) {
	for name, want := range map[string]bool{
		"key": true, "KEY": true, "api_key": true, "apikey": true, "token": true, "access_token": true,
		"accessToken": true, "secret": true, "client-secret": true, "sign": true, "signature": true, "sig": true,
		"hash": true, "password": true, "auth": true, "x.hmac": true,
		"subid": false, "keyword": false, "status": false, "payout": false, "currency": false, "tid": false,
		"monkey": false, "goal": false,
	} {
		if got := journalSecretName(name); got != want {
			t.Errorf("journalSecretName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestMaskPostbackParams checks that secret names and secret values are masked,
// other values kept and long values cut.
func TestMaskPostbackParams(t *testing.T) {
	q := url.Values{
		"subid": {"c1"}, "status": {"sale"}, "payout": {"5.5"}, "currency": {"USD"},
		"key": {"pb-secret"}, "token": {"t0k"}, "sig": {"abc"}, "note": {"pb-secret"},
		"long": {strings.Repeat("x", 1000)},
	}
	got, err := url.ParseQuery(maskPostbackParams(q, "pb-secret", ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"key", "token", "sig", "note"} {
		if got.Get(name) != journalMask {
			t.Errorf("%s = %q, want masked", name, got.Get(name))
		}
	}
	for name, want := range map[string]string{"subid": "c1", "status": "sale", "payout": "5.5", "currency": "USD"} {
		if got.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, got.Get(name), want)
		}
	}
	if len(got.Get("long")) != journalMaxValue {
		t.Errorf("long value is %d bytes, want %d", len(got.Get("long")), journalMaxValue)
	}
	many := url.Values{}
	for i := range 80 {
		many.Set("p"+strings.Repeat("a", i), "v")
	}
	if n := len(strings.Split(maskPostbackParams(many), "&")); n != journalMaxParams {
		t.Errorf("%d params kept, want %d", n, journalMaxParams)
	}
}

// TestJournalPathMasksKeySegment checks that the key of /{key}/postback never reaches the journal.
func TestJournalPathMasksKeySegment(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/s3cret/postback?subid=c", nil)
	if got := journalPath(r, "s3cret"); got != "/***/postback" || strings.Contains(got, "s3cret") {
		t.Fatalf("path %q", got)
	}
	if got := journalPath(httptest.NewRequest(http.MethodGet, "/postback", nil), ""); got != "/postback" {
		t.Fatalf("path %q", got)
	}
}

// journalEnv returns engine deps with a store, a disk queue and a journal writer.
func journalEnv(t *testing.T, cs *fakeConv) *engineDeps {
	t.Helper()
	d := queueEnv(t, cs)
	d.plog = newPostbackLog(d, d.pbq.q, discardLog())
	return d
}

// closeJournal flushes the journal writer of d and returns the lines the store took.
func closeJournal(t *testing.T, d *engineDeps, cs *fakeConv) []store.PostbackLogRow {
	t.Helper()
	if err := d.plog.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	var out []store.PostbackLogRow
	for _, b := range cs.logged {
		out = append(out, b...)
	}
	return out
}

// TestJournalAcceptedPostback checks the detail of an accepted line: sender,
// method, path, masked parameters, User-Agent, campaign, network and the
// duplicate flag of a repeat.
func TestJournalAcceptedPostback(t *testing.T) {
	cs := &fakeConv{res: store.ConversionResult{GroupID: "g1", NetworkID: "net1", Fresh: true}}
	d := journalEnv(t, cs)
	d.pbAuth = postbackAuth{secret: "pbk", mode: "require"}
	req := httptest.NewRequest(http.MethodGet, "/postback?subid=c1&status=sale&payout=5&currency=EUR&key=pbk", nil)
	req.Header.Set("User-Agent", "network-bot/1.0")
	rec := httptest.NewRecorder()
	d.postback(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("answer %d", rec.Code)
	}
	cs.set(store.ConversionResult{GroupID: "g1", NetworkID: "net1", Fresh: false}, nil)
	sendPostback(d, "/postback?subid=c1&status=sale&payout=5&key=pbk")
	rows := closeJournal(t, d, cs)
	if len(rows) != 2 {
		t.Fatalf("lines %+v, want 2", rows)
	}
	first := rows[0]
	if first.Outcome != "ok" || first.Duplicate || first.CampaignID != "g1" || first.Network != "net1" ||
		first.Method != http.MethodGet || first.Path != "/postback" || first.UserAgent != "network-bot/1.0" || first.Direction != "in" {
		t.Fatalf("first line %+v", first)
	}
	if strings.Contains(first.Params, "pbk") || !strings.Contains(first.Params, "currency=EUR") {
		t.Fatalf("params %q", first.Params)
	}
	if !rows[1].Duplicate || rows[1].Outcome != "ok" {
		t.Fatalf("the repeat must be marked duplicate: %+v", rows[1])
	}
}

// TestJournalRejectedReasons checks that a wrong method and a bad payout are journaled with their reason.
func TestJournalRejectedReasons(t *testing.T) {
	cs := &fakeConv{}
	d := journalEnv(t, cs)
	rec := httptest.NewRecorder()
	d.postback(rec, httptest.NewRequest(http.MethodPut, "/postback?subid=c1", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT answer %d", rec.Code)
	}
	if rec := sendPostback(d, "/postback?subid=c2&payout=NaN"); rec.Code != http.StatusBadRequest {
		t.Fatalf("NaN answer %d", rec.Code)
	}
	rows := closeJournal(t, d, cs)
	if len(rows) != 2 || rows[0].Outcome != store.PostbackOutcomeMethod || rows[0].Method != http.MethodPut ||
		rows[1].Outcome != store.PostbackOutcomeInvalid || rows[1].Subid != "c2" {
		t.Fatalf("lines %+v", rows)
	}
}

// TestPostbackAnswerSurvivesJournalFailure checks that a journal that cannot
// write, and one whose channel is full, change neither the answer nor the stored
// conversion.
func TestPostbackAnswerSurvivesJournalFailure(t *testing.T) {
	cs := &fakeConv{res: store.ConversionResult{GroupID: "g1", Fresh: true},
		logFn: func([]store.PostbackLogRow) error { return errors.New("clickhouse down") }}
	d := queueEnv(t, cs)
	d.plog = newPostbackLog(d, nil, discardLog())
	for range postlogInput + 10 {
		d.plog.note(store.PostbackLogRow{Subid: "filler"})
	}
	rec := sendPostback(d, "/postback?subid=c1&status=lead&payout=1")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "success") {
		t.Fatalf("answer %d %s", rec.Code, rec.Body.String())
	}
	if cs.callCount() != 1 {
		t.Fatalf("conversion calls %d, want 1", cs.callCount())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.plog.Close(ctx)
	if d.plog.lost.Load() == 0 {
		t.Fatal("lines the journal could not keep must be counted as lost")
	}
}

// TestRejectGate checks the per-second cap on rejected lines.
func TestRejectGate(t *testing.T) {
	var g rejectGate
	now := time.Unix(1000, 0)
	for i := range journalRejectPerSecond {
		if !g.allow(now) {
			t.Fatalf("line %d refused", i)
		}
	}
	if g.allow(now) {
		t.Fatal("the cap must hold within one second")
	}
	if !g.allow(now.Add(time.Second)) {
		t.Fatal("the next second starts a new budget")
	}
}
