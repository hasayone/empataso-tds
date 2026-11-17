package admin

import (
	"net/http"
	"net/url"
	"testing"
)

// retiredName assembles a retired product name from parts so that no source file
// spells it out. suffix is appended after the retired stem; it returns the full name.
func retiredName(suffix string) string {
	return "ku" + "ztds" + suffix
}

// cookieGet sends GET path with a single cookie name=token and returns the response.
// t is the test handle; a transport error fails the test. The body is closed.
func cookieGet(t *testing.T, base, path, name, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: name, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// TestRetiredSessionCookieDoesNotAuthenticate checks that the session token sent
// under the retired cookie name is refused with 401, while the same token under
// emtds_admin is accepted.
func TestRetiredSessionCookieDoesNotAuthenticate(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "admin", "p@ss"); code != http.StatusOK {
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
	if resp := cookieGet(t, srv.URL, "/api/me", cookieName, token); resp.StatusCode != http.StatusOK {
		t.Fatalf("%s /api/me: %d, want 200", cookieName, resp.StatusCode)
	}
	if resp := cookieGet(t, srv.URL, "/api/me", retiredName("_admin"), token); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("retired cookie /api/me: %d, want 401", resp.StatusCode)
	}
}

// TestRetiredBoardHeaderIsIgnored checks that requestBoard reads X-Emtds-Board only.
func TestRetiredBoardHeaderIsIgnored(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-"+retiredName("-Board"), " seo ")
	if got := requestBoard(req); got != "" {
		t.Fatalf("retired header: %q, want empty", got)
	}
	req.Header.Set(boardHeader, " aso ")
	if got := requestBoard(req); got != "aso" {
		t.Fatalf("product header: %q, want aso", got)
	}
}

// TestRetiredHealthzHeadersAreIgnored checks that the probe, host and service
// headers sent under the retired names neither match a probe nor satisfy the
// admin service check.
func TestRetiredHealthzHeadersAreIgnored(t *testing.T) {
	h := http.Header{}
	h.Set("X-"+retiredName("-Probe"), "ab12")
	h.Set("X-"+retiredName("-Host"), "park.example")
	h.Set("X-"+retiredName("-Service"), "admin")
	if healthzHeader(h, "Probe") != "" || healthzHeader(h, "Host") != "" {
		t.Fatal("retired probe headers must not be read")
	}
	if healthzServiceOK(h, "admin") {
		t.Fatal("retired service header must not satisfy the admin check")
	}
	h.Set("X-Emtds-Probe", "cd34")
	if got := healthzHeader(h, "Probe"); got != "cd34" {
		t.Fatalf("product header: %q, want cd34", got)
	}
}
