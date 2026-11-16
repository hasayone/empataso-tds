package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// settingsView decodes the body of GET and PUT /api/checker/settings.
func settingsView(t *testing.T, raw []byte) checkerSettingsView {
	t.Helper()
	var v checkerSettingsView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("bad settings %s: %v", raw, err)
	}
	return v
}

func TestCheckerLocalesFileHoldsTheContractList(t *testing.T) {
	list := checkerDefaultLocales()
	if len(list) != 41 {
		t.Fatalf("%d locales, want 41", len(list))
	}
	re := regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,3})?$`)
	seen := map[string]bool{}
	for _, l := range list {
		if !re.MatchString(l.Code) || l.Label == "" || seen[strings.ToLower(l.Code)] {
			t.Fatalf("bad locale %+v", l)
		}
		seen[strings.ToLower(l.Code)] = true
	}
	if list[0].Code != "af" || list[len(list)-1].Code != "vi" || !seen["jp"] || !seen["kr"] || !seen["es-419"] {
		t.Fatalf("list order or content is off: %+v", list)
	}
}

func TestCheckerSettingsDefaultsAndSecrets(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	code, raw := r.do(http.MethodGet, "/api/checker/settings", nil)
	v := settingsView(t, raw)
	if code != http.StatusOK || v.Enabled || v.HasToken || v.Error != "" || len(v.Locales) != 0 || len(v.DefaultLocales) != 41 {
		t.Fatalf("defaults %d %s", code, raw)
	}
	if v.PostbackMethod != "GET" || !strings.Contains(v.PostbackDefault, "{checker_status}") || v.HasPostbackToken {
		t.Fatalf("postback defaults %s", raw)
	}
	if !strings.Contains(string(raw), `"locales":[]`) {
		t.Fatalf("locales must be an empty list: %s", raw)
	}
	r.settings(map[string]any{
		"base_url": chkBase, "tenant_id": chkTenant, "token": chkToken, "enabled": true, "create_apps": true,
		"admin_url": chkAdmin + "/", "app_url": chkBase + "/apps/{id}", "postback_token": chkPBToken,
		"locales": []string{"ru-RU", "en-US"},
	})
	code, raw = r.do(http.MethodGet, "/api/checker/settings", nil)
	v = settingsView(t, raw)
	if code != http.StatusOK || !v.HasToken || !v.HasPostbackToken || !v.Enabled || !v.CreateApps ||
		v.AdminURL != chkAdmin || !slices.Equal(v.Locales, []string{"ru-RU", "en-US"}) || v.BaseURL != chkBase || v.TenantID != chkTenant {
		t.Fatalf("saved %d %s", code, raw)
	}
	noSecrets(t, "GET settings", string(raw))
	st, err := os.Stat(filepath.Join(r.dir, "checker.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("checker.json mode: %v %v", st, err)
	}
	r.settings(map[string]any{"token": "••••••••", "postback_token": ""})
	if set := r.s.checkerSettings(); set.Token != chkToken || set.PostbackToken != chkPBToken {
		t.Fatal("a masked or empty token must keep the stored one")
	}
	r.settings(map[string]any{"token": "test-token-2"})
	if set := r.s.checkerSettings(); set.Token != "test-token-2" || set.PostbackToken != chkPBToken {
		t.Fatal("a new token must replace the old one")
	}
}

func TestCheckerSettingsRejectBadFields(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	long := make([]string, 201)
	for i := range long {
		long[i] = "a" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	cases := map[string]map[string]any{
		"http base":       {"base_url": "http://acme.checkmy.store"},
		"foreign host":    {"base_url": "https://acme.example.com"},
		"base with path":  {"base_url": "https://acme.checkmy.store/api"},
		"tenant space":    {"tenant_id": "ten ant"},
		"enable no token": {"enabled": true},
		"app_url no id":   {"app_url": "https://acme.checkmy.store/apps"},
		"app_url http":    {"app_url": "http://acme.checkmy.store/apps/{id}"},
		"admin_url http":  {"admin_url": "http://tds.example.com"},
		"bad locale":      {"locales": []string{"english"}},
		"twice locale":    {"locales": []string{"ru-RU", "RU-ru"}},
		"too many":        {"locales": long},
		"token space":     {"token": "bad token"},
		"pb url http":     {"postback_url": "http://acme.checkmy.store/t?s={checker_status}"},
		"pb url no st":    {"postback_url": "https://acme.checkmy.store/t?token={checker_token}"},
		"pb url host":     {"postback_url": "https://evil.example.com/t?s={checker_status}"},
		"pb method":       {"postback_method": "DELETE"},
		"pb token space":  {"postback_token": "bad token"},
	}
	for name, body := range cases {
		if code, raw := r.do(http.MethodPut, "/api/checker/settings", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if set := r.s.checkerSettings(); set.Enabled || set.BaseURL != "" {
		t.Fatalf("a rejected PUT changed the settings: %+v", set)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "checker.json")); err == nil {
		t.Fatal("a rejected PUT wrote the file")
	}
}

func TestCheckerDamagedFileIsReportedAndRepaired(t *testing.T) {
	r := newCheckerRig(t, rigSpec{files: map[string]string{"checker.json": `{"enabled": tru`}})
	code, raw := r.do(http.MethodGet, "/api/checker/settings", nil)
	v := settingsView(t, raw)
	if code != http.StatusOK || v.Error == "" || v.Enabled {
		t.Fatalf("damaged file %d %s", code, raw)
	}
	r.run()
	if r.fc.total() != 0 {
		t.Fatal("a damaged file must stop the queue")
	}
	r.enable()
	if _, raw := r.do(http.MethodGet, "/api/checker/settings", nil); settingsView(t, raw).Error != "" {
		t.Fatalf("a save must repair the file: %s", raw)
	}
}

func TestCheckerSettingsWithAnInvalidStoredValueDisableTheIntegration(t *testing.T) {
	file := `{"base_url":"http://acme.checkmy.store","tenant_id":"t","token":"test-token","enabled":true,"create_apps":true}`
	r := newCheckerRig(t, rigSpec{files: map[string]string{"checker.json": file}})
	if _, raw := r.do(http.MethodGet, "/api/checker/settings", nil); settingsView(t, raw).Error == "" {
		t.Fatalf("an invalid stored address must be reported: %s", raw)
	}
	if r.s.checkerSettings().Enabled {
		t.Fatal("the invalid file must not enable the integration")
	}
}

func TestCheckerTestRouteNeedsTheIntegrationOn(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	if code, _ := r.do(http.MethodPost, "/api/checker/test", map[string]any{}); code != http.StatusConflict {
		t.Fatalf("test while off: %d", code)
	}
	if code, _ := r.do(http.MethodPost, "/api/checker/test", map[string]any{"base_url": chkBase, "tenant_id": chkTenant, "token": chkToken, "enabled": false}); code != http.StatusConflict {
		t.Fatalf("test with enabled false: %d", code)
	}
	if r.fc.total() != 0 {
		t.Fatal("a disabled integration sent a request")
	}
	body := map[string]any{"base_url": chkBase, "tenant_id": chkTenant, "token": chkToken, "enabled": true}
	code, raw := r.do(http.MethodPost, "/api/checker/test", body)
	var out checkerTestView
	_ = json.Unmarshal(raw, &out)
	if code != http.StatusOK || !out.OK || out.User == nil || out.User.Email != "tester@example.com" {
		t.Fatalf("test %d %s", code, raw)
	}
	if strings.Contains(string(raw), "must-not-leak") {
		t.Fatalf("the profile leaked: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "checker.json")); err == nil {
		t.Fatal("the test saved the settings")
	}
	r.enable()
	code, raw = r.do(http.MethodPost, "/api/checker/test", map[string]any{"enabled": true, "token": "••••"})
	_ = json.Unmarshal(raw, &out)
	if code != http.StatusOK || !out.OK {
		t.Fatalf("a masked token must fall back to the saved one: %d %s", code, raw)
	}
	r.fc.script["GET /api/auth/me"] = []int{http.StatusUnauthorized}
	code, raw = r.do(http.MethodPost, "/api/checker/test", map[string]any{"enabled": true})
	out = checkerTestView{}
	_ = json.Unmarshal(raw, &out)
	if code != http.StatusOK || out.OK || out.Status != http.StatusUnauthorized || out.Error == "" {
		t.Fatalf("refused test %d %s", code, raw)
	}
	noSecrets(t, "test answer", string(raw))
}

func TestCheckerDisabledIntegrationSendsNothing(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.enable()
	r.settings(map[string]any{"enabled": false})
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/api/checker/consoles"},
		{http.MethodPost, "/api/checker/apps/promo/create"},
		{http.MethodPost, "/api/checker/apps/promo/refresh"},
		{http.MethodPost, "/api/checker/test"},
	} {
		if code, raw := r.do(probe.method, probe.path, map[string]any{}); code != http.StatusConflict {
			t.Errorf("%s %s: %d %s", probe.method, probe.path, code, raw)
		}
	}
	r.run()
	if r.fc.total() != 0 {
		t.Fatalf("%d requests left a disabled integration", r.fc.total())
	}
	if _, ok := r.rec("promo"); ok {
		t.Fatal("create while off must not make a record")
	}
}

func TestCheckerTokensStayOutOfAnswersAndLogs(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.settings(map[string]any{
		"base_url": chkBase, "tenant_id": chkTenant, "token": chkToken, "enabled": true, "create_apps": true,
		"postback_token": chkPBToken, "admin_url": chkAdmin,
	})
	for _, p := range []string{
		"/api/checker/settings", "/api/checker/status", "/api/s2s", "/api/checker/apps/promo",
		"/api/checker/keywords/preview", "/api/checker/consoles",
	} {
		_, raw := r.do(http.MethodGet, p, nil)
		noSecrets(t, p, string(raw))
	}
	_, raw := r.do(http.MethodPost, "/api/checker/apps/promo/create", map[string]any{})
	noSecrets(t, "create", string(raw))
	r.run()
	r.fc.script["GET /api/auth/me"] = []int{http.StatusUnauthorized}
	_, raw = r.do(http.MethodPost, "/api/checker/test", map[string]any{})
	noSecrets(t, "test", string(raw))
	r.settings(map[string]any{"token": "test-token-2"})
	noSecrets(t, "log", r.logs.String())
	if r.logs.String() == "" {
		t.Fatal("the capture saw no log at all")
	}
	secrets := r.s.processSecrets()
	for _, want := range []string{"test-token-2", chkPBToken} {
		if !slices.Contains(secrets, want) {
			t.Errorf("%q is missing from processSecrets", want)
		}
	}
}
