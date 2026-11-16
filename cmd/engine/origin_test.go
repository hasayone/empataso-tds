package main

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/logbuf"
)

func waitCaptureEvents(ins *captureIns, want int, timeout time.Duration) []logbuf.Event {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if evs := ins.snapshotEvents(); len(evs) >= want {
			return evs
		}
		time.Sleep(2 * time.Millisecond)
	}
	return ins.snapshotEvents()
}

func originEnv(t *testing.T, groups *config.Groups) (*cidSigner, http.Handler, *captureIns, func()) {
	t.Helper()
	key := strings.Repeat("k", cidKeyBytes)
	signer := newCIDSigner(key, "", discardLog())
	h, ins, stop := curlEnv(t, groups, "0", func(d *engineDeps) { d.cids = signer })
	return signer, h, ins, stop
}

func TestOriginDirectVisit(t *testing.T) {
	gg := linked(&config.Group{ID: "a", Name: "A", Status: true, Redirect: "stop", Streams: []config.Stream{
		text("s", "OK", config.Rules{}),
	}})
	_, h, ins, stop := originEnv(t, gg)
	do(t, h, "/a", "1.1.1.1", map[string]string{"User-Agent": uaWinChrome})
	stop()
	if len(ins.events) != 1 {
		t.Fatalf("events %d", len(ins.events))
	}
	e := ins.events[0]
	if e.GroupID != "a" || e.OriginID != "a" || e.Via != "" {
		t.Fatalf("direct: group=%s origin=%s via=%q", e.GroupID, e.OriginID, e.Via)
	}
}

func TestOriginGroupChain(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Name: "A", Status: true, Redirect: "stop", Streams: []config.Stream{link("to_b", "b", config.Rules{})}},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "group", Out: "c"},
		&config.Group{ID: "c", Name: "C", Status: true, Redirect: "stop", Streams: []config.Stream{text("final", "C", config.Rules{})}},
	)
	_, h, ins, stop := originEnv(t, gg)
	do(t, h, "/a", "2.2.2.2", map[string]string{"User-Agent": uaWinChrome})
	stop()
	if len(ins.events) != 1 {
		t.Fatalf("events %d", len(ins.events))
	}
	e := ins.events[0]
	if e.GroupID != "c" || e.OriginID != "a" || e.Via != "a/to_b" {
		t.Fatalf("chain: group=%s origin=%s via=%q", e.GroupID, e.OriginID, e.Via)
	}
}

func TestOriginSignedToken(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Name: "A", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "A", config.Rules{})}},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "B", config.Rules{})}},
	)
	signer, h, ins, stop := originEnv(t, gg)
	tok := url.QueryEscape(signer.signOrigin("a"))
	do(t, h, "/b?origin="+tok, "3.3.3.3", map[string]string{"User-Agent": uaWinChrome})
	stop()
	e := ins.events[0]
	if e.GroupID != "b" || e.OriginID != "a" {
		t.Fatalf("token: group=%s origin=%s", e.GroupID, e.OriginID)
	}
}

func TestOriginForgedToken(t *testing.T) {
	gg := linked(&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{
		text("s", "B", config.Rules{}),
	}})
	_, h, ins, stop := originEnv(t, gg)
	do(t, h, "/b?origin=a.forged", "4.4.4.4", map[string]string{"User-Agent": uaWinChrome})
	stop()
	e := ins.events[0]
	if e.OriginID != "b" {
		t.Fatalf("forged origin=%s want b", e.OriginID)
	}
}

func TestOriginOfferStepKeepsOrigin(t *testing.T) {
	gg := linked(
		&config.Group{ID: "a", Name: "A", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "A", config.Rules{})}},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{
			text("land", `<a href="{offer}">go</a>`, config.Rules{}),
		}},
	)
	signer, h, ins, stop := originEnv(t, gg)
	ua := map[string]string{"User-Agent": uaWinChrome}
	tok := url.QueryEscape(signer.signOrigin("a"))
	rec := do(t, h, "/b?origin="+tok, "5.5.5.5", ua)
	body := rec.Body.String()
	re := regexp.MustCompile(`href="([^"]+/offer\?[^"]+)"`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		stop()
		t.Fatalf("offer link missing in %q", body)
	}
	offerPath := m[1]
	if strings.Contains(offerPath, "{offer}") {
		stop()
		t.Fatal("offer macro not expanded")
	}
	do(t, h, offerPath, "5.5.5.5", ua)
	stop()
	evs := ins.snapshotEvents()
	if len(evs) != 2 {
		t.Fatalf("events %d", len(evs))
	}
	if evs[0].GroupID != "b" || evs[0].OriginID != "a" {
		t.Fatalf("landing: group=%s origin=%s", evs[0].GroupID, evs[0].OriginID)
	}
	if evs[1].GroupID != "b" || evs[1].OriginID != "a" {
		t.Fatalf("offer step: group=%s origin=%s", evs[1].GroupID, evs[1].OriginID)
	}
}

func TestOriginAPIMode(t *testing.T) {
	gg := linked(&config.Group{ID: "api-g", Name: "API", Status: true, Redirect: "stop", Streams: []config.Stream{
		text("s", "OK", config.Rules{}),
	}})
	h, ins, stop := curlEnv(t, gg, "0", func(d *engineDeps) { d.keys = apiKeys{env: "k"} })
	req := apiRequest{KeyAPI: "k", ID: "api-g", IP: "6.6.6.6", UserAgent: uaWinChrome}
	do(t, h, "/?api="+encodeAPI(t, req), "6.6.6.6", nil)
	stop()
	if len(ins.events) != 1 || ins.events[0].OriginID != "api-g" {
		t.Fatalf("api origin=%+v", ins.events[0])
	}
}

func TestOriginDisabledGroupTokenIgnored(t *testing.T) {
	gg := linked(
		&config.Group{ID: "off", Name: "Off", Status: false, Redirect: "stop", Streams: []config.Stream{text("s", "X", config.Rules{})}},
		&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{text("s", "B", config.Rules{})}},
	)
	signer, h, ins, stop := originEnv(t, gg)
	tok := url.QueryEscape(signer.signOrigin("off"))
	do(t, h, "/b?origin="+tok, "7.7.7.7", map[string]string{"User-Agent": uaWinChrome})
	stop()
	e := ins.events[0]
	if e.GroupID != "b" || e.OriginID != "b" {
		t.Fatalf("disabled token: group=%s origin=%s", e.GroupID, e.OriginID)
	}
}

func TestOriginUnknownGroupTokenIgnored(t *testing.T) {
	gg := linked(&config.Group{ID: "b", Name: "B", Status: true, Redirect: "stop", Streams: []config.Stream{
		text("s", "B", config.Rules{}),
	}})
	signer, h, ins, stop := originEnv(t, gg)
	tok := url.QueryEscape(signer.signOrigin("missing"))
	do(t, h, "/b?origin="+tok, "8.8.8.8", map[string]string{"User-Agent": uaWinChrome})
	stop()
	e := ins.events[0]
	if e.OriginID != "b" {
		t.Fatalf("unknown token origin=%s", e.OriginID)
	}
}
