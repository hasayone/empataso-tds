package main

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

func pathEnv(t *testing.T, groups *config.Groups, domains *config.Book[config.Domain]) (*cidSigner, http.Handler, *captureIns, func()) {
	t.Helper()
	key := strings.Repeat("k", cidKeyBytes)
	signer := newCIDSigner(key, "", discardLog())
	h, ins, stop := curlEnv(t, groups, "0", func(d *engineDeps) {
		d.cids = signer
		d.domains = domains
	})
	return signer, h, ins, stop
}

func TestPathTokenSetsRoot(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Name: "A", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "A", config.Rules{})}},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "B", config.Rules{})}},
	)
	signer, h, ins, stop := pathEnv(t, gg, nil)
	root := "rootclick01"
	tok := url.QueryEscape(signer.signPath("a", root, time.Now()))
	do(t, h, "/b?origin="+tok, "3.3.3.3", map[string]string{"User-Agent": uaWinChrome})
	stop()
	e := ins.events[0]
	if e.GroupID != "b" || e.OriginID != "a" || e.RootCID != root {
		t.Fatalf("group=%s origin=%s root=%s", e.GroupID, e.OriginID, e.RootCID)
	}
}

func TestPathDisabledKeepsRoot(t *testing.T) {
	gg := linked(
		&config.Group{ID: "off", Name: "Off", Status: false, Redirect: "stop", Streams: []config.Stream{text("s", "X", config.Rules{})}},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "B", config.Rules{})}},
	)
	signer, h, ins, stop := pathEnv(t, gg, nil)
	root := "rootclick02"
	tok := url.QueryEscape(signer.signPath("off", root, time.Now()))
	do(t, h, "/b?origin="+tok, "7.7.7.7", map[string]string{"User-Agent": uaWinChrome})
	stop()
	e := ins.events[0]
	if e.GroupID != "b" || e.OriginID != "b" || e.RootCID != root {
		t.Fatalf("disabled path: group=%s origin=%s root=%s", e.GroupID, e.OriginID, e.RootCID)
	}
}

func TestPathMissingKeepsRoot(t *testing.T) {
	gg := linked(&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "B", config.Rules{})}})
	signer, h, ins, stop := pathEnv(t, gg, nil)
	root := "rootclick03"
	tok := url.QueryEscape(signer.signPath("gone", root, time.Now()))
	do(t, h, "/b?origin="+tok, "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	stop()
	e := ins.events[0]
	if e.OriginID != "b" || e.RootCID != root {
		t.Fatalf("missing path: origin=%s root=%s", e.OriginID, e.RootCID)
	}
}

func TestPathExpiredIgnored(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Name: "A", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "A", config.Rules{})}},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "B", config.Rules{})}},
	)
	signer, h, ins, stop := pathEnv(t, gg, nil)
	tok := url.QueryEscape(signer.signPath("a", "rootclick04", time.Now().Add(-3*time.Hour)))
	do(t, h, "/b?origin="+tok, "4.4.4.4", map[string]string{"User-Agent": uaWinChrome})
	stop()
	e := ins.events[0]
	if e.OriginID != "b" || e.RootCID != "" {
		t.Fatalf("expired: origin=%s root=%q", e.OriginID, e.RootCID)
	}
}

func TestPathOfferStepKeepsRoot(t *testing.T) {
	gg := linked(&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{
		text("land", `<a href="{offer}">go</a>`, config.Rules{}),
	}})
	_, h, ins, stop := pathEnv(t, gg, nil)
	ua := map[string]string{"User-Agent": uaWinChrome}
	rec := do(t, h, "/b", "5.5.5.5", ua)
	body := rec.Body.String()
	m := regexp.MustCompile(`href="([^"]+/offer\?[^"]+)"`).FindStringSubmatch(body)
	if len(m) < 2 {
		stop()
		t.Fatalf("offer link missing in %q", body)
	}
	do(t, h, m[1], "5.5.5.5", ua)
	stop()
	evs := ins.snapshotEvents()
	if len(evs) != 2 {
		t.Fatalf("events %d", len(evs))
	}
	if evs[0].RootCID != "" || evs[0].CID == "" {
		t.Fatalf("first click root=%q cid=%q", evs[0].RootCID, evs[0].CID)
	}
	if evs[1].GroupID != "b" || evs[1].RootCID != evs[0].CID || evs[1].OriginID != "b" {
		t.Fatalf("offer step group=%s origin=%s root=%s want root %s", evs[1].GroupID, evs[1].OriginID, evs[1].RootCID, evs[0].CID)
	}
}

func TestHTTPRedirectAddsOrigin(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Name: "A", Status: true, Redirect: "http_redirect", Out: "https://emaso.online/b?k=1"},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "B", config.Rules{})}},
	)
	domains := testDomains(t, config.Domain{ID: "1", Host: "emaso.online"})
	signer, h, ins, stop := pathEnv(t, gg, domains)
	ua := map[string]string{"User-Agent": uaWinChrome}
	rec := do(t, h, "/a", "9.9.9.9", ua)
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil || u.Host != "emaso.online" || u.Query().Get("k") != "1" {
		stop()
		t.Fatalf("location %q", loc)
	}
	gid, root, _, ok := signer.verifyPath(u.Query().Get("origin"), time.Now())
	if !ok || gid != "a" {
		stop()
		t.Fatalf("origin token gid=%s ok=%v loc=%s", gid, ok, loc)
	}
	do(t, h, u.RequestURI(), "9.9.9.9", ua)
	stop()
	evs := ins.snapshotEvents()
	if len(evs) != 2 {
		t.Fatalf("events %d", len(evs))
	}
	if evs[0].RootCID != "" || evs[0].CID != root {
		t.Fatalf("first cid=%s root=%s token root=%s", evs[0].CID, evs[0].RootCID, root)
	}
	if evs[1].GroupID != "b" || evs[1].OriginID != "a" || evs[1].RootCID != evs[0].CID {
		t.Fatalf("second group=%s origin=%s root=%s", evs[1].GroupID, evs[1].OriginID, evs[1].RootCID)
	}
}
