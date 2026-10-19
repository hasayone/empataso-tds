package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// cookieVisit requests path through the engine as 8.8.8.8 behind the trusted
// proxy 127.0.0.1. proto is X-Forwarded-Proto; remote overrides the peer.
func cookieVisit(t *testing.T, h http.Handler, path, proto, remote string) []*http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	req.Header.Set("User-Agent", uaWinChrome)
	if proto != "" {
		req.Header.Set("X-Forwarded-Proto", proto)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result().Cookies()
}

// Every cookie the engine sets (uniqueness of both steps, the remembered
// stream, the rotator) is Secure over HTTPS and not otherwise.
func TestEngineCookiesAreSecureOverHTTPS(t *testing.T) {
	offer := "/g1/offer?cid=" + signedCID()
	cases := []struct {
		name, proto, remote string
		secure              bool
	}{
		{"https via trusted proxy", "https", "127.0.0.1:1", true},
		{"http via trusted proxy", "http", "127.0.0.1:1", false},
		{"no header", "", "127.0.0.1:1", false},
		{"untrusted peer claims https", "https", "5.5.5.5:1", false},
	}
	for _, tc := range cases {
		h := testEnv(t, uniqGroup("cookie"))
		var got []*http.Cookie
		got = append(got, cookieVisit(t, h, "/g1", tc.proto, tc.remote)...)
		got = append(got, cookieVisit(t, h, offer, tc.proto, tc.remote)...)
		names := map[string]bool{}
		for _, c := range got {
			names[c.Name] = true
			if c.Secure != tc.secure || !c.HttpOnly {
				t.Errorf("%s: cookie %s secure=%v httponly=%v", tc.name, c.Name, c.Secure, c.HttpOnly)
			}
		}
		if !names["ztu_g1"] || !names["ztuo_g1"] {
			t.Errorf("%s: cookies %v, want both uniqueness cookies", tc.name, names)
		}
	}
}

func TestStickyAndRotatorCookiesAreSecureOverHTTPS(t *testing.T) {
	sticky := config.NewGroups(stickyGroup("cookie"))
	rotator := config.NewGroups(&config.Group{
		ID: "g1", Status: true, Redirect: "stop",
		Streams: []config.Stream{{Name: "s", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "x|||y", Distribution: "rotator"}}},
	})
	for name, groups := range map[string]*config.Groups{"sticky": sticky, "rotator": rotator} {
		prefix := map[string]string{"sticky": stickyCookiePrefix, "rotator": "ztrot_"}[name]
		for _, secure := range []bool{true, false} {
			proto := "http"
			if secure {
				proto = "https"
			}
			var seen bool
			for _, c := range cookieVisit(t, testEnv(t, groups), "/g1", proto, "127.0.0.1:1") {
				if strings.HasPrefix(c.Name, prefix) {
					seen = true
					if c.Secure != secure {
						t.Errorf("%s over %s: secure=%v", name, proto, c.Secure)
					}
				}
			}
			if !seen {
				t.Errorf("%s over %s: no %s cookie set", name, proto, prefix)
			}
		}
	}
}
