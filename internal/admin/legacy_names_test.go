package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// legacyCookieGet sends GET path with only the pre-rename session cookie set to token.
func legacyCookieGet(t *testing.T, base, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: legacyCookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestSPACopiesLegacyStorageKeys(t *testing.T) {
	html := spaSource()
	head := strings.Index(html, "try{var t=localStorage.getItem('emtds-theme')")
	copyAt := strings.Index(html, "var o='kuztds'")
	if copyAt < 0 || head < 0 || copyAt > head {
		t.Fatal("SPA must copy kuztds* storage keys to emtds* before reading the theme")
	}
	if !strings.Contains(html, "if(s.getItem(nk)===null)s.setItem(nk,s.getItem(k))") {
		t.Fatal("storage copy must not overwrite an existing emtds* key")
	}
}

func TestRequestBoardReadsLegacyHeader(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(legacyBoardHeader, " seo ")
	if got := requestBoard(req); got != "seo" {
		t.Fatalf("legacy header: %q, want seo", got)
	}
	req.Header.Set(boardHeader, "aso")
	if got := requestBoard(req); got != "aso" {
		t.Fatalf("new header wins: %q, want aso", got)
	}
}

func TestHealthzHeaderReadsLegacyEngine(t *testing.T) {
	h := http.Header{}
	h.Set("X-Kuztds-Probe", "ab12")
	h.Set("X-Kuztds-Host", "park.example")
	h.Set("X-Kuztds-Service", "admin")
	if healthzHeader(h, "Probe") != "ab12" || healthzHeader(h, "Host") != "park.example" {
		t.Fatal("legacy probe headers must be read when the new ones are absent")
	}
	if !healthzServiceOK(h, "admin") {
		t.Fatal("legacy X-Kuztds-Service must satisfy the admin check")
	}
	h.Set("X-Emtds-Probe", "cd34")
	if got := healthzHeader(h, "Probe"); got != "cd34" {
		t.Fatalf("new header wins: %q, want cd34", got)
	}
}

func TestLegacySessionCookieStillAuthenticates(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t)
	code, csrf := login(t, c, srv.URL, "admin", "p@ss")
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	u, _ := url.Parse(srv.URL)
	token := ""
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == cookieName {
			token = ck.Value
		}
	}
	if token == "" {
		t.Fatalf("no %s cookie after login", cookieName)
	}
	if resp := legacyCookieGet(t, srv.URL, "/api/me", token); resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy cookie /api/me: %d, want 200", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/logout", nil)
	req.AddCookie(&http.Cookie{Name: legacyCookieName, Value: token})
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout with legacy cookie: %d, want 200", resp.StatusCode)
	}
	expired := false
	for _, ck := range resp.Cookies() {
		if ck.Name == legacyCookieName && ck.MaxAge < 0 {
			expired = true
		}
	}
	if !expired {
		t.Fatalf("logout did not expire %s", legacyCookieName)
	}
	if resp := legacyCookieGet(t, srv.URL, "/api/me", token); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("legacy cookie after logout: %d, want 401", resp.StatusCode)
	}
}
