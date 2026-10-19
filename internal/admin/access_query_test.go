package admin

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/render"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

// extraMacros are the curly macros of the extra click fields: render reads them from
// the click query by their own name.
var extraMacros = []string{"{site}", "{ad_campaign_id}", "{creative_id}", "{external_id}"}

// unstoredMacros are the Sub ID macros render fills from the query and the journal
// does not store.
var unstoredMacros = []string{"{sub_id_13}", "{sub_id_14}", "{sub_id_15}"}

// seedSource stores a traffic source as the administrator.
func (r *rolesRig) seedSource(t *testing.T, id string, params map[string]string) {
	t.Helper()
	src := config.TrafficSource{ID: id, Name: id, Parameters: params}
	if code, raw := r.adminAPI(t, http.MethodPost, "/api/traffic-sources", src); code != http.StatusCreated {
		t.Fatalf("seed source %s: %d %s", id, code, raw)
	}
}

// putSource sends the source id with params and name as the user and returns the status.
func (r *rolesRig) putSource(t *testing.T, id, name string, params map[string]string) int {
	t.Helper()
	code, _ := r.api(t, r.user, r.ucsrf, http.MethodPut, "/api/traffic-sources/"+id,
		config.TrafficSource{ID: id, Name: name, Parameters: params})
	return code
}

func TestQueryReadMacroTablesMatchRender(t *testing.T) {
	deps := render.MacroDeps{Query: func(string) (string, bool) { return "V", true }}
	for _, tok := range unstoredMacros {
		if _, ok := macroQueryRead[tok]; !ok {
			t.Errorf("%s is not in macroQueryRead", tok)
		}
		if _, ok := macroSafe[tok]; ok {
			t.Errorf("%s reads the query and must not be in macroSafe", tok)
		}
		if curlyClassified(strings.Trim(tok, "{}")) {
			t.Errorf("%s must count as a query read for the curly guard", tok)
		}
		if render.Expand(tok, deps) == tok {
			t.Errorf("render does not expand %s", tok)
		}
	}
	for _, tok := range append(append([]string{}, unstoredMacros...), extraMacros...) {
		g := &macroGuard{curly: true}
		if g.find(tok) == "" {
			t.Errorf("the curly guard lets %s through", tok)
		}
	}
	for _, name := range config.ExtraSourceParams {
		if !slices.Contains(queryReadFields(), name) {
			t.Errorf("extra click field %s is not a query-read field", name)
		}
		if _, ok := sourceParamField(name); !ok {
			t.Errorf("source parameter %s is not recognised", name)
		}
	}
}

func TestQueryReadMacroRefusedWhenQueryFieldHidden(t *testing.T) {
	rig := newRolesRig(t, "")
	macros := append(append([]string{}, unstoredMacros...), extraMacros...)
	for _, hidden := range [][]string{{"keyword"}, {"sub_id_3"}, {"cost"}, {"site"}, {"ad_campaign_id"}, {"creative_id"}, {"external_id"}} {
		for _, m := range macros {
			rig.resetCampaign(plainCampaign(""))
			rig.setRole(t, editorAccess(hidden...))
			if got := rig.putCampaign(t, plainCampaign("https://t.example/?v="+m)); got != http.StatusBadRequest {
				t.Errorf("hidden %v, new %s: %d, want 400", hidden, m, got)
			}
		}
	}
	for _, hidden := range []string{"external_id", "creative_id"} {
		rig.resetCampaign(plainCampaign(""))
		rig.setRole(t, editorAccess(hidden))
		if got := rig.putCampaign(t, plainCampaign("https://t.example/?z={zone}")); got != http.StatusBadRequest {
			t.Errorf("hidden %s, new {zone}: %d, want 400", hidden, got)
		}
	}

	for _, hidden := range [][]string{{"ip"}, {"country", "useragent"}, nil} {
		for _, m := range macros {
			rig.resetCampaign(plainCampaign(""))
			rig.setRole(t, editorAccess(hidden...))
			if got := rig.putCampaign(t, plainCampaign("https://t.example/?v="+m)); got != http.StatusOK {
				t.Errorf("hidden %v, %s: %d, want 200 (no query-read field hidden)", hidden, m, got)
			}
		}
	}

	rig.setRole(t, editorAccess("keyword"))
	rig.resetCampaign(plainCampaign("https://a.example/?r={sub_id_13}&s={site}"))
	renamed := plainCampaign("https://a.example/?r={sub_id_13}&s={site}")
	renamed.Name = "renamed"
	if got := rig.putCampaign(t, renamed); got != http.StatusOK {
		t.Errorf("stored macros kept verbatim: %d", got)
	}
	if got := rig.putCampaign(t, plainCampaign("https://b.example/?r={sub_id_13}&s={site}")); got != http.StatusBadRequest {
		t.Errorf("host changed in a link with the stored macros: %d", got)
	}
	if got := rig.putCampaign(t, plainCampaign("https://a.example/?r={sub_id_13}&s={site}&t={sub_id_14}")); got != http.StatusBadRequest {
		t.Errorf("a new query-read macro beside the stored ones: %d", got)
	}
}

func TestHiddenKeywordSubID13RebindRefused(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "s1", map[string]string{"keyword": "utm_term", "sub_id_1": "p1"})
	rig.setRole(t, editorAccess("keyword"))

	for _, key := range []string{"sub_id_13", "sub_id_14", "sub_id_15"} {
		if got := rig.putSource(t, "s1", "src", map[string]string{"keyword": "utm_term", "sub_id_1": "p1", key: "utm_term"}); got != http.StatusBadRequest {
			t.Errorf("%s mapped to the keyword parameter: %d, want 400", key, got)
		}
		if got := rig.putSource(t, "s1", "src", map[string]string{"keyword": "utm_term", "sub_id_1": "p1", key: "zone"}); got != http.StatusBadRequest {
			t.Errorf("%s mapped while a keyword is hidden: %d, want 400", key, got)
		}
	}
	code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/traffic-sources",
		config.TrafficSource{ID: "s9", Name: "new", Parameters: map[string]string{"sub_id_13": "zone"}})
	if code != http.StatusBadRequest {
		t.Errorf("new source mapping sub_id_13: %d, want 400", code)
	}
	if got := rig.putSource(t, "s1", "renamed", map[string]string{"keyword": "utm_term", "sub_id_1": "p1"}); got != http.StatusOK {
		t.Errorf("rename with the parameters kept: %d", got)
	}

	rig.setRole(t, editorAccess("ip"))
	if got := rig.putSource(t, "s1", "src", map[string]string{"keyword": "utm_term", "sub_id_1": "p1", "sub_id_13": "zone"}); got != http.StatusOK {
		t.Errorf("sub_id_13 mapped while no query field is hidden: %d", got)
	}
	rig.setRole(t, editorAccess())
	if got := rig.putSource(t, "s1", "src", map[string]string{"sub_id_13": "zone", "sub_id_14": "z2"}); got != http.StatusOK {
		t.Errorf("nothing hidden: %d", got)
	}
}

func TestStoredSubID13MappingKept(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "s1", map[string]string{"sub_id_13": "zone", "sub_id_1": "p1"})
	rig.setRole(t, editorAccess("keyword"))
	if got := rig.putSource(t, "s1", "renamed", map[string]string{"sub_id_13": "zone", "sub_id_1": "p9"}); got != http.StatusOK {
		t.Errorf("the stored sub_id_13 name kept: %d", got)
	}
	if got := rig.putSource(t, "s1", "renamed", map[string]string{"sub_id_1": "p9"}); got != http.StatusBadRequest {
		t.Errorf("the stored sub_id_13 name cleared: %d", got)
	}
}

func TestExtraFieldSourceParameterIsLocked(t *testing.T) {
	rig := newRolesRig(t, "")
	for _, name := range config.ExtraSourceParams {
		rig.seedSource(t, "x-"+name, map[string]string{name: "orig", "sub_id_1": "p1"})
		rig.setRole(t, editorAccess(name))
		id := "x-" + name
		if got := rig.putSource(t, id, "src", map[string]string{name: "other", "sub_id_1": "p1"}); got != http.StatusBadRequest {
			t.Errorf("%s remapped while hidden: %d", name, got)
		}
		if got := rig.putSource(t, id, "src", map[string]string{"sub_id_1": "p1"}); got != http.StatusBadRequest {
			t.Errorf("%s cleared while hidden: %d", name, got)
		}
		if got := rig.putSource(t, id, "renamed", map[string]string{name: "orig", "sub_id_1": "p2"}); got != http.StatusOK {
			t.Errorf("%s kept, a visible parameter changed: %d", name, got)
		}
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/traffic-sources",
			config.TrafficSource{ID: "n-" + name, Name: "new", Parameters: map[string]string{name: "orig"}})
		if code != http.StatusBadRequest {
			t.Errorf("new source mapping hidden %s: %d", name, code)
		}
		rig.setRole(t, editorAccess("ip"))
		if got := rig.putSource(t, id, "src", map[string]string{name: "other"}); got != http.StatusOK {
			t.Errorf("%s remapped while visible: %d", name, got)
		}
	}
}

func TestParameterCannotReadHiddenQueryName(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "k", map[string]string{"keyword": "utm_term", "sub_id_1": "p1"})
	rig.seedSource(t, "u", map[string]string{"sub_id_1": "p1"})
	rig.seedSource(t, "e", map[string]string{"external_id": "ext", "sub_id_1": "p1"})

	rig.setRole(t, editorAccess("keyword"))
	if got := rig.putSource(t, "k", "src", map[string]string{"keyword": "utm_term", "sub_id_1": "utm_term"}); got != http.StatusBadRequest {
		t.Errorf("sub_id_1 mapped onto the keyword parameter: %d, want 400", got)
	}
	if got := rig.putSource(t, "k", "src", map[string]string{"keyword": "utm_term", "sub_id_1": "p1", "site": "utm_term"}); got != http.StatusBadRequest {
		t.Errorf("site mapped onto the keyword parameter: %d, want 400", got)
	}
	if got := rig.putSource(t, "u", "src", map[string]string{"sub_id_1": "q"}); got != http.StatusBadRequest {
		t.Errorf("sub_id_1 mapped onto q, the plain keyword parameter: %d, want 400", got)
	}
	if got := rig.putSource(t, "k", "src", map[string]string{"keyword": "utm_term", "sub_id_1": "p9"}); got != http.StatusOK {
		t.Errorf("sub_id_1 mapped to another name: %d", got)
	}

	rig.setRole(t, editorAccess("sub_id_3"))
	if got := rig.putSource(t, "u", "src", map[string]string{"sub_id_1": "sub_id_3"}); got != http.StatusBadRequest {
		t.Errorf("sub_id_1 mapped onto the plain name of a hidden Sub ID: %d, want 400", got)
	}
	if got := rig.putSource(t, "u", "src", map[string]string{"sub_id_1": "sub_id_4"}); got != http.StatusOK {
		t.Errorf("sub_id_1 mapped onto the name of a visible Sub ID: %d", got)
	}

	rig.setRole(t, editorAccess("external_id"))
	if got := rig.putSource(t, "e", "src", map[string]string{"external_id": "ext", "sub_id_1": "ext"}); got != http.StatusBadRequest {
		t.Errorf("sub_id_1 mapped onto the parameter of a hidden extra field: %d, want 400", got)
	}

	rig.setRole(t, editorAccess("ip"))
	if got := rig.putSource(t, "u", "src", map[string]string{"sub_id_1": "q"}); got != http.StatusOK {
		t.Errorf("no query-read field hidden: %d", got)
	}
	rig.setRole(t, editorAccess())
	if got := rig.putSource(t, "k", "src", map[string]string{"keyword": "utm_term", "sub_id_1": "utm_term"}); got != http.StatusOK {
		t.Errorf("nothing hidden: %d", got)
	}
}

func TestCampaignSourceSwitchRefusedForUnstoredSubID(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.seedSource(t, "m14", map[string]string{"sub_id_14": "zone"})
	rig.seedSource(t, "msite", map[string]string{"site": "zone"})
	rig.seedSource(t, "clean", map[string]string{"sub_id_1": "p1"})
	base := plainCampaign("https://a.example/")

	for _, c := range []struct {
		hidden []string
		source string
		want   int
	}{
		{[]string{"keyword"}, "m14", http.StatusBadRequest},
		{[]string{"site"}, "msite", http.StatusBadRequest},
		{[]string{"keyword"}, "msite", http.StatusOK},
		{[]string{"keyword"}, "clean", http.StatusOK},
		{[]string{"ip"}, "m14", http.StatusOK},
		{nil, "m14", http.StatusOK},
	} {
		rig.resetCampaign(base)
		rig.setRole(t, editorAccess(c.hidden...))
		next := base
		next.SourceID = c.source
		if got := rig.putCampaign(t, next); got != c.want {
			t.Errorf("hidden %v, source %s: %d, want %d", c.hidden, c.source, got, c.want)
		}
	}
}

func TestExpandHiddenAddsDerivedFields(t *testing.T) {
	cases := map[string][]string{
		"status":  {"original_status", "prev_status"},
		"os":      {"os_version"},
		"browser": {"browser_ver"},
		"brand":   {"device_model"},
		"device":  {"device_model"},
	}
	for parent, children := range cases {
		hidden := expandHidden([]string{parent})
		for _, c := range append([]string{parent}, children...) {
			if !hidden[c] {
				t.Errorf("hiding %s does not hide %s", parent, c)
			}
			if !fieldHidden(viewer{hidden: hidden}, c) {
				t.Errorf("fieldHidden(%s) is false with %s hidden", c, parent)
			}
			if fieldHidden(viewer{admin: true, hidden: hidden}, c) {
				t.Errorf("administrator sees %s", c)
			}
		}
		for _, c := range children {
			own := expandHidden([]string{c})
			if own[parent] {
				t.Errorf("hiding %s must not hide %s", c, parent)
			}
		}
	}
	if got := fieldJSONKeys("status"); !slices.Contains(got, "original_status") || !slices.Contains(got, "prev_status") {
		t.Errorf("fieldJSONKeys(status) = %v", got)
	}
	for _, f := range store.StaticFields() {
		for _, kids := range derivedFields {
			for _, k := range kids {
				if k == f.Name && !hiddenIDOK(k) {
					t.Errorf("derived field %s cannot be hidden by itself", k)
				}
			}
		}
	}
	if hidden := expandHidden([]string{"country", "ip"}); hidden["os_version"] || hidden["original_status"] {
		t.Errorf("unrelated fields hide derived ones: %v", hidden)
	}
}

func TestHiddenStatusHidesOriginalAndPreviousStatus(t *testing.T) {
	rig := newRolesRig(t, "")
	derived := []string{"original_status", "prev_status", "os_version", "browser_ver", "device_model"}
	paths := func(field string) []string {
		return []string{
			"/api/log-values?table=conversions&field=" + field,
			"/api/stats/breakdown?dim=" + field,
			"/api/postbacks?f=" + field + ":eq:x",
			"/api/stats/summary?f=" + field + ":in:x",
		}
	}
	for _, c := range []struct{ hidden, field string }{
		{"status", "original_status"}, {"status", "prev_status"},
		{"os", "os_version"}, {"browser", "browser_ver"}, {"brand", "device_model"}, {"device", "device_model"},
	} {
		rig.hide(t, c.hidden)
		for _, p := range paths(c.field) {
			code, body := rig.get(t, rig.user, p)
			if code != http.StatusBadRequest || !strings.Contains(body, "hidden field "+c.field) {
				t.Errorf("hide %s: GET %s = %d %s", c.hidden, p, code, body)
			}
		}
	}

	rig.hide(t, "status")
	_, body := rig.get(t, rig.user, "/api/postbacks")
	for _, k := range []string{"original_status", "prev_status"} {
		if strings.Contains(body, k) {
			t.Errorf("postbacks carry %s with status hidden: %s", k, body)
		}
	}
	_, body = rig.get(t, rig.user, "/api/log-fields")
	for _, k := range []string{"original_status", "prev_status"} {
		if strings.Contains(body, `"`+k+`"`) {
			t.Errorf("log-fields list %s with status hidden", k)
		}
	}
	if code, _ := rig.get(t, rig.user, "/api/stats/facts"); code != http.StatusOK {
		t.Errorf("facts with status hidden: %d", code)
	}

	conversionPaths := func(field string) []string {
		p := paths(field)
		return []string{p[0], p[2]}
	}
	rig.hide(t, "ip")
	for _, f := range derived {
		for _, p := range conversionPaths(f) {
			if code, body := rig.get(t, rig.user, p); code != http.StatusOK {
				t.Errorf("hide ip: GET %s = %d %s", p, code, body)
			}
		}
	}
	rig.hide(t, "status")
	for _, p := range conversionPaths("original_status") {
		if code, body := rig.get(t, rig.admin, p); code != http.StatusOK {
			t.Errorf("administrator: GET %s = %d %s", p, code, body)
		}
	}
	rig.hide(t, "original_status")
	if code, _ := rig.get(t, rig.user, "/api/log-values?table=conversions&field=status"); code != http.StatusOK {
		t.Errorf("hiding a derived field must not hide status: %d", code)
	}
}

func TestFactsKeepStatusRowsButNotDerivedStatus(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.hide(t, "status")
	code, body := rig.get(t, rig.user, "/api/stats/facts")
	if code != http.StatusOK {
		t.Fatalf("facts %d %s", code, body)
	}
	for _, k := range []string{"original_status", "prev_status"} {
		if strings.Contains(body, `"`+k+`"`) {
			t.Errorf("facts carry %s: %s", k, body)
		}
	}
	if !strings.Contains(body, `"clicks"`) {
		t.Errorf("click facts are gone: %s", body)
	}
}

func TestNetworkOfferQueryErrorHidesClosedRecord(t *testing.T) {
	rig := newRolesRig(t, "")
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/affiliate-networks", config.AffiliateNetwork{ID: "bad", Name: "bad", Status: true, OfferQuery: "sub1=[IP]&k={zone}"}); code != http.StatusCreated {
		t.Fatalf("seed network %d %s", code, raw)
	}
	post := func() (int, string) {
		code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/offers",
			config.Offer{ID: "n1", Name: "n1", URL: "https://me.example/", Status: true, NetworkID: "bad"})
		return code, string(raw)
	}
	noSection := editorAccess("ip")
	noSection.Sections = slices.DeleteFunc(slices.Clone(noSection.Sections), func(s string) bool { return s == string(secNetworks) })
	notMine := editorAccess("ip")
	notMine.Grants[resNetworks] = security.Grant{Mode: security.ModeCreated}

	for name, acc := range map[string]security.Access{"no networks section": noSection, "network of another user": notMine} {
		rig.setRole(t, acc)
		code, body := post()
		if code != http.StatusBadRequest || !strings.Contains(body, errNetworkQuery) {
			t.Errorf("%s: %d %s, want the fixed text", name, code, body)
		}
		for _, leak := range []string{"[IP]", "IP", "ip", "{zone}", "zone", "sub1"} {
			if strings.Contains(strings.ReplaceAll(body, errNetworkQuery, ""), leak) {
				t.Errorf("%s: the answer names %q: %s", name, leak, body)
			}
		}
	}

	rig.setRole(t, editorAccess("ip"))
	code, body := post()
	if code != http.StatusBadRequest || !strings.Contains(body, "network offer_query: macro [IP] uses hidden field ip") {
		t.Errorf("a role that reads networks: %d %s, want the macro named", code, body)
	}
	rig.setRole(t, editorAccess())
	if code, body := post(); code != http.StatusCreated {
		t.Errorf("nothing hidden: %d %s", code, body)
	}
}

func TestCreatedGrantHoldsNoListsInMemory(t *testing.T) {
	dir := t.TempDir()
	const withLists = `[{"id":"r1","name":"buyers","access":{"sections":["offers"],` +
		`"grants":{"offers":{"mode":"created","ids":["o1"],"labels":["L"]}}}}]`
	path := filepath.Join(dir, "roles.json")
	if err := os.WriteFile(path, []byte(withLists), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newRolesServer(t, dir)
	check := func(when string) {
		t.Helper()
		role, ok := s.roles.Get("r1")
		if !ok {
			t.Fatalf("%s: role missing: %s", when, s.roles.Failure())
		}
		want := security.Grant{Mode: security.ModeCreated}
		if g := role.Access.Grants["offers"]; !reflect.DeepEqual(g, want) {
			t.Errorf("%s: grant %+v, want %+v", when, g, want)
		}
	}
	check("start")

	if err := os.WriteFile(path, []byte(withLists), 0o600); err != nil {
		t.Fatal(err)
	}
	s.userMu.Lock()
	err := s.reloadRoles()
	s.userMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	check("reload")

	v := viewer{uid: "u2", grants: map[string]security.Grant{resOffers: {Mode: security.ModeCreated, IDs: []string{"o1"}, Labels: []string{"L"}}}}
	if v.canSee(resOffers, ref{id: "o1", label: "L", owner: "u1"}) {
		t.Error("a created grant with a leftover list sees a record of another user")
	}
	if !v.canSee(resOffers, ref{id: "o2", owner: "u2"}) {
		t.Error("a created grant must still see own records")
	}
}

func TestNormalizeAccessDropsListsOfCreatedMode(t *testing.T) {
	out, err := normalizeAccess(security.Access{Grants: map[string]security.Grant{
		resOffers:    {Mode: security.ModeCreated, IDs: []string{"a"}, Labels: []string{"b"}},
		resLandings:  {Mode: security.ModeSelected, IDs: []string{"c"}},
		resCampaigns: {Mode: security.ModeFull, IDs: []string{"d"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if g := out.Grants[resOffers]; g.IDs != nil || g.Labels != nil || g.Mode != security.ModeCreated {
		t.Errorf("created: %+v", g)
	}
	if g := out.Grants[resLandings]; !reflect.DeepEqual(g.IDs, []string{"c"}) {
		t.Errorf("selected keeps its list: %+v", g)
	}
	if g := out.Grants[resCampaigns]; g.IDs != nil {
		t.Errorf("full: %+v", g)
	}
}
