package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// ownPostbacks returns the templates of other owners that the managed ones must never disturb.
func ownPostbacks() []config.OutboundPostback {
	return []config.OutboundPostback{
		{ID: "pb-59f7f66e85", Name: "Own", URL: "https://example.com/p?x={subid}", Method: "GET", Statuses: []string{"lead"}},
		{ID: "alloffers", Name: "All", URL: "https://example.com/all", Method: "POST", Statuses: []string{"sale"}},
		{ID: "seooffers", Name: "Seo", URL: "https://example.com/seo", Method: "GET", Statuses: []string{"rejected"}, BoardID: "seo"},
	}
}

// seedS2S stores the templates of other owners in s2s.json.
func (r *checkerRig) seedS2S(list []config.OutboundPostback) {
	r.t.Helper()
	if err := r.s.cfg.S2S.Save(context.Background(), list); err != nil {
		r.t.Fatal(err)
	}
}

// s2sFile returns the bytes of s2s.json; a missing file is empty.
func (r *checkerRig) s2sFile() []byte {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, "s2s.json"))
	if err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
	return b
}

// s2sSplit returns the stored templates apart: other owners first, then the managed ones.
func (r *checkerRig) s2sSplit() (own, managed []config.OutboundPostback) {
	r.t.Helper()
	list, err := r.s.cfg.S2S.List(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return splitCheckerManaged(list)
}

// jsonOf returns the JSON of v for byte comparison.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// connect saves the connection settings with the integration on and merges patch over them.
func (r *checkerRig) connect(patch map[string]any) {
	r.t.Helper()
	body := map[string]any{"base_url": chkBase, "tenant_id": chkTenant, "token": chkToken, "enabled": true}
	for k, v := range patch {
		body[k] = v
	}
	r.settings(body)
}

// wantManaged returns the three templates the default address gives for token.
func wantManaged(token string) []config.OutboundPostback {
	tpl := strings.ReplaceAll(checkerPostbackDefault(chkBase), checkerTokenMacro, url.QueryEscape(token))
	out := make([]config.OutboundPostback, 0, 3)
	for _, k := range checkerPostbackKinds {
		out = append(out, config.OutboundPostback{
			ID: k.id, Name: k.name, URL: strings.ReplaceAll(tpl, checkerStatusMacro, k.status),
			Method: "GET", Statuses: []string{k.status},
		})
	}
	return out
}

func TestCheckerPostbackNeedsAnEnabledIntegrationAndAToken(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.seedS2S(ownPostbacks())
	before := r.s2sFile()
	r.connect(map[string]any{"enabled": false})
	if !bytes.Equal(r.s2sFile(), before) {
		t.Fatal("a disabled integration touched s2s.json")
	}
	r.connect(map[string]any{})
	if !bytes.Equal(r.s2sFile(), before) {
		t.Fatal("the default template asks for a token, none is saved, yet s2s.json changed")
	}
	r.connect(map[string]any{"postback_token": chkPBToken})
	if _, managed := r.s2sSplit(); len(managed) != 3 {
		t.Fatalf("a filled postback gave %d templates", len(managed))
	}
	r.connect(map[string]any{"enabled": false})
	own, managed := r.s2sSplit()
	if len(managed) != 0 || !bytes.Equal(r.s2sFile(), before) || jsonOf(t, own) != jsonOf(t, ownPostbacks()) {
		t.Fatalf("switching off left %d managed templates", len(managed))
	}
}

func TestCheckerPostbackWritesThreeTemplates(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.seedS2S(ownPostbacks())
	r.connect(map[string]any{"postback_token": "a&b=c"})
	own, managed := r.s2sSplit()
	if jsonOf(t, managed) != jsonOf(t, wantManaged("a&b=c")) {
		t.Fatalf("managed templates:\n%s", jsonOf(t, managed))
	}
	if !strings.Contains(managed[0].URL, "token=a%26b%3Dc&") || !strings.Contains(managed[1].URL, "status=sale") {
		t.Fatalf("token or status misplaced: %s", managed[0].URL)
	}
	if jsonOf(t, own) != jsonOf(t, ownPostbacks()) {
		t.Fatalf("other templates changed: %s", jsonOf(t, own))
	}
	r.connect(map[string]any{"postback_token": "next-token"})
	own, managed = r.s2sSplit()
	if jsonOf(t, managed) != jsonOf(t, wantManaged("next-token")) || len(managed) != 3 {
		t.Fatalf("after the token change:\n%s", jsonOf(t, managed))
	}
	if jsonOf(t, own) != jsonOf(t, ownPostbacks()) {
		t.Fatal("the token change touched other templates")
	}
}

func TestCheckerPostbackOwnTemplateAndMethod(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.connect(map[string]any{
		"postback_token": chkPBToken, "postback_method": "POST",
		"postback_url": "https://hooks.checkmy.store/in?t={checker_token}&s={checker_status}",
	})
	_, managed := r.s2sSplit()
	if len(managed) != 3 || managed[2].URL != "https://hooks.checkmy.store/in?t=test-pb-token&s=rejected" || managed[2].Method != "POST" {
		t.Fatalf("custom template: %s", jsonOf(t, managed))
	}
	r.connect(map[string]any{"postback_url": "https://hooks.checkmy.store/in?s={checker_status}"})
	_, managed = r.s2sSplit()
	if len(managed) != 3 || managed[0].URL != "https://hooks.checkmy.store/in?s=lead" {
		t.Fatalf("a template without a token placeholder: %s", jsonOf(t, managed))
	}
}

func TestCheckerPostbackRefusesBadTemplates(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.seedS2S(ownPostbacks())
	before := r.s2sFile()
	for name, patch := range map[string]map[string]any{
		"no status":    {"postback_url": "https://hooks.checkmy.store/in?t={checker_token}"},
		"http":         {"postback_url": "http://hooks.checkmy.store/in?s={checker_status}"},
		"other host":   {"postback_url": "https://example.com/in?s={checker_status}"},
		"bare suffix":  {"postback_url": "https://.checkmy.store/in?s={checker_status}"},
		"with a port":  {"postback_url": "https://hooks.checkmy.store:8443/in?s={checker_status}"},
		"with a login": {"postback_url": "https://u:p@hooks.checkmy.store/in?s={checker_status}"},
		"bad method":   {"postback_method": "PUT"},
	} {
		body := map[string]any{"base_url": chkBase, "tenant_id": chkTenant, "token": chkToken, "enabled": true, "postback_token": chkPBToken}
		for k, v := range patch {
			body[k] = v
		}
		if code, raw := r.do(http.MethodPut, "/api/checker/settings", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if !bytes.Equal(r.s2sFile(), before) {
		t.Fatal("a refused template changed s2s.json")
	}
}

func TestCheckerPostbackTokenStaysOutOfEveryAnswer(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.seedS2S(ownPostbacks())
	r.connect(map[string]any{"postback_token": chkPBToken})
	for _, p := range []string{"/api/s2s", "/api/checker/settings", "/api/checker/status"} {
		code, raw := r.do(http.MethodGet, p, nil)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", p, code)
		}
		noSecrets(t, p, string(raw))
		if strings.Contains(string(raw), checkerManagedPrefix) && p == "/api/s2s" {
			t.Errorf("%s lists a managed template", p)
		}
	}
	var list []config.OutboundPostback
	_, raw := r.do(http.MethodGet, "/api/s2s", nil)
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, p := range list {
		if p.ID == "pb-59f7f66e85" || p.ID == "alloffers" {
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("other templates are missing from GET /api/s2s: %s", raw)
	}
	_, raw = r.do(http.MethodGet, "/api/checker/status", nil)
	var st struct {
		Postback checkerPostbackView `json:"postback"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Postback.Available || st.Postback.Method != "GET" || !strings.Contains(st.Postback.URLMasked, "token="+checkerMaskedToken) ||
		!strings.Contains(st.Postback.URLMasked, checkerStatusMacro) {
		t.Fatalf("status postback %+v", st.Postback)
	}
	r.connect(map[string]any{"enabled": false})
	_, raw = r.do(http.MethodGet, "/api/checker/status", nil)
	st.Postback = checkerPostbackView{}
	if err := json.Unmarshal(raw, &st); err != nil || st.Postback.Available || st.Postback.URLMasked != "" {
		t.Fatalf("status of a disabled postback: %s", raw)
	}
	noSecrets(t, "log", r.logs.String())
}

func TestCheckerPostbackManagedTemplatesSurviveS2SWrites(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.seedS2S(ownPostbacks())
	r.connect(map[string]any{"postback_token": chkPBToken})
	_, managed := r.s2sSplit()
	want := jsonOf(t, managed)
	before := r.s2sFile()
	for _, id := range []string{"checker-lead", " checker-sale", "checker-new"} {
		body := []config.OutboundPostback{{ID: id, Name: "X", URL: "https://example.com/x", Method: "GET"}}
		if code, raw := r.do(http.MethodPut, "/api/s2s", body); code != http.StatusBadRequest || !strings.Contains(string(raw), msgCheckerManaged) {
			t.Errorf("PUT with id %q: %d %s", id, code, raw)
		}
	}
	if !bytes.Equal(r.s2sFile(), before) {
		t.Fatal("a refused PUT changed s2s.json")
	}
	_, raw := r.do(http.MethodGet, "/api/s2s", nil)
	var shown []config.OutboundPostback
	_ = json.Unmarshal(raw, &shown)
	if code, raw := r.do(http.MethodPut, "/api/s2s", shown); code != http.StatusOK {
		t.Fatalf("round trip: %d %s", code, raw)
	}
	if _, now := r.s2sSplit(); jsonOf(t, now) != want {
		t.Fatalf("a PUT of the shown list changed the managed templates: %s", jsonOf(t, now))
	}
	if code, raw := r.do(http.MethodPut, "/api/s2s", []config.OutboundPostback{}); code != http.StatusOK {
		t.Fatalf("empty PUT: %d %s", code, raw)
	}
	if _, now := r.s2sSplit(); jsonOf(t, now) != want {
		t.Fatalf("an empty PUT dropped the managed templates: %s", jsonOf(t, now))
	}
}

func TestCheckerPostbackSyncWritesOnlyChanges(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	r.seedS2S(ownPostbacks())
	r.connect(map[string]any{"postback_token": chkPBToken})
	path := filepath.Join(r.dir, "s2s.json")
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := r.s2sFile()
	for i := 0; i < 3; i++ {
		if err := r.s.syncCheckerPostbacks(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(path)
	if err != nil || !bytes.Equal(before, r.s2sFile()) || !after.ModTime().Equal(first.ModTime()) {
		t.Fatal("an unchanged sync rewrote s2s.json")
	}
	if r.fc.total() != 0 {
		t.Fatal("the postback sync went to the network")
	}
}

func TestCheckerPostbackSyncWithoutFilesIsQuiet(t *testing.T) {
	r := newCheckerRig(t, rigSpec{})
	if err := r.s.syncCheckerPostbacks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "s2s.json")); err == nil {
		t.Fatal("an off integration created s2s.json")
	}
}

func TestBoardCopyLeavesCheckerTemplatesAlone(t *testing.T) {
	groups := &memGroups{list: []config.Group{{ID: "mainCamp", Name: "Main", BoardID: "main", Num: 3, Redirect: "stop"}}}
	hs, _, dir := boardCopyServer(t, groups)
	store := NewJSONStore[config.OutboundPostback](filepath.Join(dir, "s2s.json"))
	managed := wantManaged(chkPBToken)
	own := config.OutboundPostback{ID: "pb-own", Name: "Own", URL: "https://example.com/p", Method: "GET", BoardID: "main"}
	if err := store.Save(context.Background(), append([]config.OutboundPostback{own}, managed...)); err != nil {
		t.Fatal(err)
	}
	c, csrf := boardLogin(t, hs.URL, "admin", "p@ss")
	pick := []string{"checker-lead", "checker-sale", "checker-rejected", "pb-own"}
	resp, raw := boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards/import", "seo", csrf, boardPickBody{From: "main", S2S: pick})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import %d %s", resp.StatusCode, raw)
	}
	var in boardImportResponse
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if in.Copied.S2S != 1 || len(in.Map.S2S) != 1 || in.Map.S2S["pb-own"] == "" {
		t.Fatalf("import copied %+v %+v", in.Copied, in.Map.S2S)
	}
	list, _ := store.List(context.Background())
	if len(list) != 5 {
		t.Fatalf("%d templates after the import, want 5: %s", len(list), jsonOf(t, list))
	}
	if _, now := splitCheckerManaged(list); jsonOf(t, now) != jsonOf(t, managed) {
		t.Fatalf("the import changed the managed templates: %s", jsonOf(t, now))
	}
	resp, raw = boardJSON(t, c, http.MethodPost, hs.URL+"/api/boards/move", "main", csrf, boardPickBody{To: "seo", S2S: pick[:3]})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move %d %s", resp.StatusCode, raw)
	}
	var mv boardMoveResponse
	if err := json.Unmarshal(raw, &mv); err != nil || mv.Moved.S2S != 0 {
		t.Fatalf("move moved %+v (%v)", mv.Moved, err)
	}
	list, _ = store.List(context.Background())
	if _, now := splitCheckerManaged(list); jsonOf(t, now) != jsonOf(t, managed) {
		t.Fatalf("the move changed the managed templates: %s", jsonOf(t, now))
	}
}
