package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
)

func accessTestServer(t *testing.T) *Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := New(Config{
		AdminUser:    "admin",
		PasswordHash: hash,
		Sessions:     security.NewMemoryStore(),
		Stats:        fakeStats{},
		Groups:       &fakeGroups{},
		Lists:        &fakeLists{data: map[string]string{"ip_test.dat": "1.2.3.4\n"}},
		Keys:         fakeKeys{},
		Offers:       NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings:     NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:      NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks:     NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		Domains:      NewJSONStore[config.Domain](filepath.Join(dir, "domains.json")),
		Limiter:      allowAll{},
		SessionTTL:   time.Hour,
		CookieSecure: false,
		UsersFile:    filepath.Join(dir, "users.json"),
		APIKeysFile:  filepath.Join(dir, "api-keys.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Handler()
	return s
}

func TestRouteAccessCoversEveryRoute(t *testing.T) {
	s := accessTestServer(t)
	if len(s.routes) == 0 {
		t.Fatal("no routes recorded")
	}
	seen := make(map[string]int)
	for _, p := range s.routes {
		seen[p]++
		if _, ok := routeAccess[p]; !ok {
			t.Errorf("route %q missing from routeAccess", p)
		}
		if seen[p] > 1 {
			t.Errorf("duplicate route registration %q", p)
		}
	}
	for p := range routeAccess {
		if seen[p] == 0 {
			t.Errorf("routeAccess[%q] never registered", p)
		}
	}
}

var cookieOnlyRoutes = map[string]bool{
	"POST /api/logout":               true,
	"GET /api/me":                    true,
	"POST /api/password":             true,
	"GET /api/api-keys":              true,
	"POST /api/api-keys":             true,
	"POST /api/api-keys/{id}/rotate": true,
	"PUT /api/api-keys/{id}":         true,
	"DELETE /api/api-keys/{id}":      true,
	"GET /api/users":                 true,
	"POST /api/users":                true,
	"GET /api/users/{id}":            true,
	"PUT /api/users/{id}":            true,
	"DELETE /api/users/{id}":         true,
	"PUT /api/users/{id}/password":   true,
	"PUT /api/users/{id}/access":     true,
	"GET /api/roles":                 true,
	"POST /api/roles":                true,
	"POST /api/roles/reload":         true,
	"GET /api/roles/{id}":            true,
	"PUT /api/roles/{id}":            true,
	"DELETE /api/roles/{id}":         true,
	"GET /api/keitaro/settings":      true,
	"PUT /api/keitaro/settings":      true,

	"GET /api/registrars/namecheap":           true,
	"PUT /api/registrars/namecheap":           true,
	"POST /api/registrars/namecheap/test":     true,
	"POST /api/registrars/namecheap/register": true,
	"POST /api/domains/{id}/ns":               true,
}

var expect401Routes = map[string]bool{
	"POST /api/password": true,
}

// catalogCreateRoutes lists routes that require grant create rights, not only POST.
// A user with every section but no ModeFull/ModeCreated grant gets 403 here.
var catalogCreateRoutes = map[string]bool{
	"POST /api/offers":             true,
	"POST /api/landings":           true,
	"POST /api/traffic-sources":    true,
	"POST /api/affiliate-networks": true,
	"POST /api/domains":            true,
	"PUT /api/groups/{id}":         true,
}

// skipAllowed lists routes skipped for roles that would otherwise pass the guard.
// Side effects or long external work after the guard are avoided.
var skipAllowed = map[string]string{
	"POST /api/logout": "terminates the cookie session under test",
}

type matrixRole struct {
	name    string
	cookie  bool
	apiKey  string
	login   string
	pass    string
	isRoot  bool
	isAdmin bool
	isFull  bool
	isEmpty bool
}

func TestRouteMatrix(t *testing.T) {
	s := accessTestServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	allSecs := sectionStrings(userSections)
	if _, err := s.createUser(userDraft{
		Login: "full", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Sections: allSecs, Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createUser(userDraft{
		Login: "empty", Password: "secret1", Type: security.UserRegular, Enabled: true,
		Access: security.Access{Sections: []string{}, Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createUser(userDraft{
		Login: "adm", Password: "secret1", Type: security.UserAdmin, Enabled: true,
		Access: security.Access{Grants: map[string]security.Grant{}},
	}); err != nil {
		t.Fatal(err)
	}

	s.keyMu.Lock()
	rawKey, _, err := s.keys.Generate("t", []string{security.ScopeAdmin})
	s.keyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	patterns := make([]string, 0, len(routeAccess))
	for p := range routeAccess {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	// Logout last for cookie clients so earlier routes keep the session.
	sort.SliceStable(patterns, func(i, j int) bool {
		if patterns[i] == "POST /api/logout" {
			return false
		}
		if patterns[j] == "POST /api/logout" {
			return true
		}
		return patterns[i] < patterns[j]
	})

	roles := []matrixRole{
		{name: "root", cookie: true, login: "admin", pass: "p@ss", isRoot: true, isAdmin: true},
		{name: "adminUser", cookie: true, login: "adm", pass: "secret1", isAdmin: true},
		{name: "fullUser", cookie: true, login: "full", pass: "secret1", isFull: true},
		{name: "emptyUser", cookie: true, login: "empty", pass: "secret1", isEmpty: true},
		{name: "apiKey", apiKey: rawKey, isAdmin: true},
	}

	for _, ro := range roles {
		t.Run(ro.name, func(t *testing.T) {
			var c *http.Client
			var csrf string
			if ro.cookie {
				jar, _ := cookiejar.New(nil)
				c = &http.Client{Jar: jar}
				_, csrf = login(t, c, srv.URL, ro.login, ro.pass)
			} else {
				c = &http.Client{}
			}
			for _, pattern := range patterns {
				rule := routeAccess[pattern]
				if ro.isFull || ro.isAdmin || ro.isRoot {
					if _, skip := skipAllowed[pattern]; skip && ro.cookie {
						continue
					}
				}
				method, path := splitPattern(pattern)
				url := srv.URL + substitutePath(path)
				body := ""
				if method == http.MethodPost || method == http.MethodPut {
					body = `{}`
				}
				code := doStatus(t, c, method, url, csrf, ro.apiKey, body)
				wantForbidden := expectForbidden(ro, pattern, rule)
				if wantForbidden {
					if code != http.StatusForbidden {
						t.Errorf("%s %s: want 403, got %d", method, path, code)
					}
					continue
				}
				if ro.cookie && expect401Routes[pattern] {
					if code != http.StatusUnauthorized && code != http.StatusForbidden {
						t.Errorf("%s %s: want 401 or 403, got %d", method, path, code)
					}
					continue
				}
				if code == http.StatusUnauthorized || code == http.StatusForbidden {
					t.Errorf("%s %s: unexpected %d", method, path, code)
				}
			}
		})
	}

	t.Run("me_pages", func(t *testing.T) {
		wantFull := sectionStrings(userSections)
		if pages := mePagesForLogin(t, srv.URL, "full", "secret1"); !sameStrings(pages, wantFull) {
			t.Fatalf("fullUser pages: got %v want %v", pages, wantFull)
		}
		if pages := mePagesForLogin(t, srv.URL, "empty", "secret1"); len(pages) != 0 {
			t.Fatalf("emptyUser pages: got %v want []", pages)
		}
	})
}

func mePagesForLogin(t *testing.T, base, userLogin, pass string) []string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	login(t, c, base, userLogin, pass)
	req, err := http.NewRequest(http.MethodGet, base+"/api/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me %d", resp.StatusCode)
	}
	var me struct {
		Pages []string `json:"pages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	return me.Pages
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func expectForbidden(ro matrixRole, pattern string, rule routeRule) bool {
	if ro.isAdmin && ro.cookie {
		return false
	}
	if ro.apiKey != "" {
		return cookieOnlyRoutes[pattern]
	}
	if ro.isEmpty {
		return rule.sec != secAny
	}
	if ro.isFull {
		if catalogCreateRoutes[pattern] {
			return true
		}
		return rule.admin
	}
	return true
}

func splitPattern(pattern string) (method, path string) {
	i := strings.IndexByte(pattern, ' ')
	return pattern[:i], pattern[i+1:]
}

func substitutePath(path string) string {
	path = strings.ReplaceAll(path, "{id}", "zz")
	path = strings.ReplaceAll(path, "{name}", "ip_test.dat")
	path = strings.ReplaceAll(path, "{path...}", "a.txt")
	return path
}

func doStatus(t *testing.T, c *http.Client, method, url, csrf, apiKey, body string) int {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" && method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if apiKey != "" {
		req.Header.Set("Api-Key", apiKey)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestNormalizeAccess(t *testing.T) {
	_, err := normalizeAccess(security.Access{Sections: []string{"nope"}})
	if !errorsIsAccess(err, errAccessSection) {
		t.Fatalf("section: %v", err)
	}
	_, err = normalizeAccess(security.Access{
		Sections: []string{"users"},
		Grants:   map[string]security.Grant{},
	})
	if !errorsIsAccess(err, errAccessSection) {
		t.Fatalf("admin section: %v", err)
	}
	_, err = normalizeAccess(security.Access{
		Grants: map[string]security.Grant{"bad": {Mode: security.ModeRead}},
	})
	if !errorsIsAccess(err, errAccessResource) {
		t.Fatalf("resource: %v", err)
	}
	_, err = normalizeAccess(security.Access{
		Grants: map[string]security.Grant{"offers": {Mode: "weird"}},
	})
	if !errorsIsAccess(err, errAccessMode) {
		t.Fatalf("mode: %v", err)
	}
	_, err = normalizeAccess(security.Access{
		Hidden: []string{"nope"},
	})
	if !errorsIsAccess(err, errAccessHidden) {
		t.Fatalf("hidden: %v", err)
	}
	out, err := normalizeAccess(security.Access{
		Sections: []string{"dashboard", "dashboard", " flows "},
		Grants: map[string]security.Grant{
			"offers": {Mode: security.ModeFull, IDs: []string{"x"}},
		},
		Hidden: []string{"profit", "profit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sections) != 2 || out.Sections[0] != "dashboard" {
		t.Fatalf("sections: %v", out.Sections)
	}
	if g := out.Grants["offers"]; len(g.IDs) != 0 {
		t.Fatalf("full mode should clear ids: %+v", g)
	}
	if len(out.Hidden) != 1 {
		t.Fatalf("hidden dedupe: %v", out.Hidden)
	}
}

func errorsIsAccess(err, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}

func TestUserPages(t *testing.T) {
	admin := security.User{Type: security.UserAdmin}
	if len(userPages(admin, admin.Access)) != len(allSections) {
		t.Fatalf("admin pages len %d", len(userPages(admin, admin.Access)))
	}
	u := security.User{
		Type: security.UserRegular,
		Access: security.Access{
			Sections: []string{"tools", "dashboard"},
		},
	}
	pages := userPages(u, u.Access)
	if len(pages) != 2 || pages[0] != "dashboard" || pages[1] != "tools" {
		t.Fatalf("user pages order: %v", pages)
	}
}
