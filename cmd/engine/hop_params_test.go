package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/render"
)

// Two campaigns with different parameter names. The second campaign's
// conditions, macros and click log keep the keyword and sub id collected on
// the way in, and its own mapping fills only a field that was still empty.
func TestGroupHopKeepsEntryClickParams(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Status: true, Redirect: "stop",
			Parameters:  map[string]string{"keyword": "kw_in", "sub_id_1": "bundle_id", "site": "src_site"},
			ParamTokens: map[string]string{"keyword": "test", "sub_id_1": "com.com.com"},
			Streams:     []config.Stream{link("to_b", "b", config.Rules{})}},
		&config.Group{ID: "b", Status: true, Redirect: "show_text", Out: "MISS",
			Parameters: map[string]string{"keyword": "kw_out", "sub_id_1": "other", "creative_id": "cr"},
			Streams: []config.Stream{text("hit", "{keyword}|{sub_id_1}", config.Rules{SubIDs: map[string]config.SubIDFilter{
				"sub_id_1": {Flag: config.FlagB, Values: []string{"com.test.app"}},
			}})}},
	)
	h, ins, stop := withClickLog(t, gg, nil)
	rec := do(t, h, "/a?kw_in=shoes&bundle_id=com.test.app&kw_out=nope&other=nope&src_site=site1&site=nope&cr=crea", "8.8.8.8", chrome)
	stop()
	if body := rec.Body.String(); body != "shoes|com.test.app" {
		t.Fatalf("body = %q, want shoes|com.test.app", body)
	}
	if rec.Header().Get("X-Emtds-Stream") != "hit" {
		t.Fatalf("stream = %q, want hit", rec.Header().Get("X-Emtds-Stream"))
	}
	if len(ins.events) != 1 {
		t.Fatalf("%d events, want 1", len(ins.events))
	}
	ev := ins.events[0]
	if ev.GroupID != "b" || ev.Keyword != "shoes" || ev.Subs[0] != "com.test.app" || ev.Site != "site1" || ev.CreativeID != "crea" {
		t.Fatalf("event group=%s keyword=%q sub=%q site=%q creative=%q", ev.GroupID, ev.Keyword, ev.Subs[0], ev.Site, ev.CreativeID)
	}
}

// An external redirect onto another campaign of this tracker carries the
// collected values. The location keeps origin, does not write an empty
// sub_id_1 when the click had one, and does not write link placeholders.
// The campaign that receives the redirect sees the same keyword and sub id.
func TestOwnHostRedirectCarriesClickParams(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Status: true, Redirect: "group", Out: "b",
			Parameters:  map[string]string{"keyword": "keyword", "sub_id_1": "bundle_id"},
			ParamTokens: map[string]string{"keyword": "test", "sub_id_1": "com.com.com"}},
		&config.Group{ID: "b", Status: true, Redirect: "http_redirect",
			Parameters: map[string]string{"sub_id_1": "other"},
			Out:        "https://emaso.online/alloffers?sub_id_1={sub_id_1}#z"},
		&config.Group{ID: "alloffers", Status: true, Redirect: "show_text", Out: "MISS",
			Parameters: map[string]string{"sub_id_1": "click_id"},
			Streams: []config.Stream{text("hit", "{keyword}|{sub_id_1}", config.Rules{SubIDs: map[string]config.SubIDFilter{
				"sub_id_1": {Flag: config.FlagB, Values: []string{"com.test.app"}},
			}})}},
	)
	signer, h, ins, stop := pathEnv(t, gg, testDomains(t, config.Domain{ID: "1", Host: "emaso.online"}))
	stopped := false
	stopOnce := func() {
		if !stopped {
			stopped = true
			stop()
		}
	}
	defer stopOnce()
	rec := do(t, h, "/a?keyword=test1&bundle_id=com.test.app", "9.9.9.9", chrome)
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("location %q: %v", loc, err)
	}
	q := u.Query()
	if q.Get("sub_id_1") != "com.test.app" || strings.Contains(loc, "sub_id_1=&") || strings.HasSuffix(strings.Split(loc, "#")[0], "sub_id_1=") {
		t.Fatalf("empty or missing sub_id_1 in %q", loc)
	}
	if q.Get("keyword") != "test1" || q.Get("bundle_id") != "com.test.app" {
		t.Fatalf("carried params in %q", loc)
	}
	if _, ok := q["sub_id_2"]; ok {
		t.Fatalf("empty sub id written: %q", loc)
	}
	if u.Fragment != "z" {
		t.Fatalf("fragment %q", u.Fragment)
	}
	for _, values := range q {
		for _, v := range values {
			if v == "test" || v == "com.com.com" {
				t.Fatalf("link placeholder %q in %q", v, loc)
			}
		}
	}
	gid, _, _, ok := signer.verifyPath(q.Get("origin"), time.Now())
	if !ok || gid != "a" {
		t.Fatalf("origin gid=%s ok=%v loc=%s", gid, ok, loc)
	}
	followed := do(t, h, u.RequestURI(), "9.9.9.9", chrome)
	if body := followed.Body.String(); body != "test1|com.test.app" {
		t.Fatalf("next campaign body = %q, location %q", body, loc)
	}
	stopOnce()
	evs := ins.snapshotEvents()
	if len(evs) != 2 {
		t.Fatalf("%d events, want 2", len(evs))
	}
	if evs[0].GroupID != "b" || evs[0].Keyword != "test1" || evs[0].Subs[0] != "com.test.app" {
		t.Fatalf("redirect click group=%s keyword=%q sub=%q", evs[0].GroupID, evs[0].Keyword, evs[0].Subs[0])
	}
	if evs[1].GroupID != "alloffers" || evs[1].Keyword != "test1" || evs[1].Subs[0] != "com.test.app" || evs[1].OriginID != "a" {
		t.Fatalf("next click group=%s keyword=%q sub=%q origin=%s", evs[1].GroupID, evs[1].Keyword, evs[1].Subs[0], evs[1].OriginID)
	}
}

// carryClickParams leaves a key that is already present, skips empty values
// and a foreign host, and does not replace origin.
func TestCarryClickParamsLeavesSetAndEmpty(t *testing.T) {
	d, _ := originLinkDeps(t, "emaso.online", &config.Group{ID: "alloffers", Status: true})
	grp := &config.Group{
		ID:          "a",
		Parameters:  map[string]string{"keyword": "keyword", "sub_id_1": "bundle_id"},
		ParamTokens: map[string]string{"keyword": "test", "sub_id_1": "com.com.com"},
	}
	var subs [config.MaxSubID]string
	subs[0] = "com.test.app"
	got := d.carryClickParams("https://emaso.online/alloffers?keyword=keep&origin=tok#z", grp, "test1", subs, config.Extras{})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("keyword") != "keep" || q.Get("bundle_id") != "com.test.app" || q.Get("origin") != "tok" || u.Fragment != "z" {
		t.Fatalf("location %q", got)
	}
	if _, ok := q["sub_id_2"]; ok || strings.Contains(got, "sub_id_2=") {
		t.Fatalf("empty sub id written: %q", got)
	}
	foreign := "https://offer.example/go?s=1"
	if d.carryClickParams(foreign, grp, "test1", subs, config.Extras{}) != foreign {
		t.Fatalf("foreign host changed")
	}
}

// A landing that redirects onto another campaign of this tracker keeps origin
// and adds the collected values under the entry campaign's link names.
func TestLandingRedirectCarriesClickParams(t *testing.T) {
	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://emaso.online/alloffers?sub_id_1=com.test.app#z")
		w.WriteHeader(http.StatusFound)
	}))
	defer php.Close()
	srv := newLandingServer(t.TempDir(), php.URL, func(id string) (config.Landing, bool) {
		if id == "7" {
			return config.Landing{ID: "7", Status: true, Local: true, Entry: "index.php"}, true
		}
		return config.Landing{}, false
	}, discardLog())
	d, signer := originLinkDeps(t, "emaso.online",
		&config.Group{ID: "alloffers", Status: true},
		&config.Group{ID: "a", Status: true, Parameters: map[string]string{"keyword": "keyword", "sub_id_1": "bundle_id"},
			ParamTokens: map[string]string{"keyword": "test", "sub_id_1": "com.com.com"}},
	)
	srv.originOn = d.withOrigin
	grp, _ := d.groups.Get("a")
	var subs [config.MaxSubID]string
	subs[0] = "com.test.app"
	req := httptest.NewRequest(http.MethodGet, "/g1", nil)
	rec := httptest.NewRecorder()
	origin := signer.signPath("a", "rootclick01", time.Now())
	srv.serveEntry(rec, req, "7", render.MacroDeps{CID: "0123456789", Origin: origin}, func(loc string) string {
		return d.carryClickParams(loc, grp, "test1", subs, config.Extras{})
	})
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("location %q: %v", loc, err)
	}
	q := u.Query()
	if q.Get("sub_id_1") != "com.test.app" || q.Get("keyword") != "test1" || q.Get("bundle_id") != "com.test.app" {
		t.Fatalf("location %q", loc)
	}
	if q.Get("origin") != origin || u.Fragment != "z" {
		t.Fatalf("origin or fragment lost: %q", loc)
	}
	if strings.Contains(loc, "sub_id_1=&") || strings.Contains(loc, "sub_id_2=") {
		t.Fatalf("empty parameter: %q", loc)
	}
}
