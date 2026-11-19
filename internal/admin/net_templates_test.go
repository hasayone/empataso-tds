package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// referenceNetworkNames reads the reference list of network names kept in testdata,
// one per line, in the order the Keitaro template picker shows them.
func referenceNetworkNames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/keitaro_template_names.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// templateRig is a templates API over a temporary edits file: Do sends one request to the
// three write routes and the list route and returns the status and the body.
type templateRig struct {
	t    *testing.T
	path string
	mux  *http.ServeMux
}

// newTemplateRig returns a rig whose edits file does not exist yet.
func newTemplateRig(t *testing.T) *templateRig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "net-templates.json")
	s := &Server{cfg: Config{NetTemplatesFile: path}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/affiliate-network-templates", s.handleNetworkTemplates)
	mux.HandleFunc("POST /api/affiliate-network-templates", s.handleNetworkTemplateCreate)
	mux.HandleFunc("PUT /api/affiliate-network-templates/{id}", s.handleNetworkTemplateUpdate)
	mux.HandleFunc("DELETE /api/affiliate-network-templates/{id}", s.handleNetworkTemplateDelete)
	return &templateRig{t: t, path: path, mux: mux}
}

// do sends method and url with body (a JSON string, may be empty) and returns code and body.
func (g *templateRig) do(method, url, body string) (int, []byte) {
	g.t.Helper()
	rr := httptest.NewRecorder()
	g.mux.ServeHTTP(rr, httptest.NewRequest(method, url, strings.NewReader(body)))
	return rr.Code, rr.Body.Bytes()
}

// get returns the template with id from the list route, or false.
func (g *templateRig) get(id string) (netTemplate, bool) {
	g.t.Helper()
	code, raw := g.do(http.MethodGet, "/api/affiliate-network-templates", "")
	if code != http.StatusOK {
		g.t.Fatalf("list status %d", code)
	}
	var list []netTemplate
	if err := json.Unmarshal(raw, &list); err != nil {
		g.t.Fatal(err)
	}
	for _, tpl := range list {
		if tpl.ID == id {
			return tpl, true
		}
	}
	return netTemplate{}, false
}

// TestNetTemplatesCoverReference checks the catalog against the reference name list:
// every reference name is present, none is missing, no name or id repeats.
func TestNetTemplatesCoverReference(t *testing.T) {
	ref := referenceNetworkNames(t)
	if len(ref) != 333 {
		t.Fatalf("reference holds %d names, want 333", len(ref))
	}
	seenRef := map[string]bool{}
	for _, n := range ref {
		key := strings.ToLower(n)
		if seenRef[key] {
			t.Fatalf("reference repeats %q", n)
		}
		seenRef[key] = true
	}
	list := netTemplateCatalog()
	names := map[string]bool{}
	ids := map[string]bool{}
	for _, tpl := range list {
		key := strings.ToLower(tpl.Name)
		if tpl.Name == "" || tpl.Name != strings.TrimSpace(tpl.Name) {
			t.Errorf("name %q is empty or has edge spaces", tpl.Name)
		}
		if names[key] {
			t.Errorf("duplicate name %q", tpl.Name)
		}
		names[key] = true
		if tpl.ID == "" || ids[tpl.ID] {
			t.Errorf("id %q of %q is empty or repeated", tpl.ID, tpl.Name)
		}
		ids[tpl.ID] = true
	}
	for _, n := range ref {
		if !names[strings.ToLower(n)] {
			t.Errorf("reference name %q is missing from the catalog", n)
		}
	}
	if len(list) != len(ref) {
		t.Fatalf("catalog holds %d templates, want exactly the %d names of the reference", len(list), len(ref))
	}
	for _, tpl := range list {
		if tpl.Source != "keitaro-json" {
			t.Errorf("%q has source %q, the catalog holds only exported templates", tpl.Name, tpl.Source)
		}
		if id := tpl.ID; id == "generic-s2s" || id == "clickid-goal-sum" {
			t.Errorf("own preset %q is back in the catalog", tpl.Name)
		}
	}
}

// TestNetTemplatesFieldsAreHonest checks that every template holds only real, safe fields:
// the level matches the fields, a name-only template draws nothing, a template with fields
// names its source, every postback goes to this tracker through {origin}, and no foreign
// address sits in an offer query or a postback address.
func TestNetTemplatesFieldsAreHonest(t *testing.T) {
	counts := map[string]int{}
	for _, tpl := range netTemplateCatalog() {
		counts[tpl.Extracted]++
		hasQuery, hasURL := tpl.OfferQuery != "", tpl.PostbackURL != ""
		hasAny := hasQuery || hasURL || tpl.Instruction != "" || len(tpl.Conversions) != 0
		switch tpl.Extracted {
		case netTplNone:
			if hasQuery || hasURL {
				t.Errorf("%q is none but holds a query or a postback", tpl.Name)
			}
		case netTplPartial:
			if hasQuery == hasURL {
				t.Errorf("%q is partial but holds %v query and %v url", tpl.Name, hasQuery, hasURL)
			}
		case netTplFull:
			if !hasQuery || !hasURL {
				t.Errorf("%q is full but lacks a field", tpl.Name)
			}
		default:
			t.Errorf("%q has level %q", tpl.Name, tpl.Extracted)
		}
		if hasAny && tpl.Source == "" {
			t.Errorf("%q has fields and no source", tpl.Name)
		}
		if !hasAny && tpl.Source != "" {
			t.Errorf("%q has a source and no fields", tpl.Name)
		}
		if hasURL && !strings.HasPrefix(tpl.PostbackURL, "{origin}/postback?") {
			t.Errorf("%q postback does not go to this tracker", tpl.Name)
		}
		for _, f := range []string{tpl.OfferQuery, tpl.PostbackURL} {
			if strings.Contains(strings.ToLower(f), "http") {
				t.Errorf("%q stores an address: %q", tpl.Name, f)
			}
		}
		if tpl.Source == "keitaro-json" && !hasQuery {
			t.Errorf("%q came from the export without an offer parameter", tpl.Name)
		}
	}
	if counts[netTplNone] != 0 || counts[netTplFull]+counts[netTplPartial] != 333 {
		t.Fatalf("levels %v, want every template to hold a real setting", counts)
	}
	t.Logf("levels: %v", counts)
}

// TestNetTemplatesKeepInstruction checks that an instruction of the export is stored as written,
// holds no tracker-side rewrite, and that a template without one has none.
func TestNetTemplatesKeepInstruction(t *testing.T) {
	byName := map[string]netTemplate{}
	withText := 0
	for _, tpl := range netTemplateCatalog() {
		byName[tpl.Name] = tpl
		if tpl.Instruction != "" {
			withText++
			if tpl.Instruction != strings.TrimSpace(tpl.Instruction) {
				t.Errorf("%q instruction has edge spaces", tpl.Name)
			}
		}
	}
	if withText == 0 {
		t.Fatal("no template carries an instruction")
	}
	g := byName["Godlike.partners"]
	if g.OfferQuery != "visit_id={subid}&web_id={campaign_id}" || !strings.Contains(g.Instruction, "<") {
		t.Errorf("Godlike.partners: query %q, instruction %q", g.OfferQuery, g.Instruction)
	}
	if _, ok := byName["Generic S2S"]; ok {
		t.Error("the Generic S2S preset must not be listed")
	}
	if _, ok := byName["clickid, goal, sum"]; ok {
		t.Error("the clickid, goal, sum preset must not be listed")
	}
}

// TestNetTemplateSlug checks the id of names with dots, spaces, commas and brackets.
func TestNetTemplateSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Gambeat.io":             "gambeat-io",
		"clickid, goal, sum":     "clickid-goal-sum",
		"Partnerize.com (iherb)": "partnerize-com-iherb",
		"7leads.biz(CPA)":        "7leads-biz-cpa",
		"Dr.Cash":                "dr-cash",
		"Моя сеть":               "моя-сеть",
	} {
		if got := netTemplateSlug(in); got != want {
			t.Errorf("slug(%q)=%q, want %q", in, got, want)
		}
	}
}

// TestNetTemplatesHandler checks the JSON body: a list with every field, conversions never null.
func TestNetTemplatesHandler(t *testing.T) {
	rr := httptest.NewRecorder()
	(&Server{}).handleNetworkTemplates(rr, httptest.NewRequest(http.MethodGet, "/api/affiliate-network-templates", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), `"conversions":null`) {
		t.Fatal("conversions is null")
	}
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(netTemplateCatalog()) {
		t.Fatalf("got %d templates", len(got))
	}
	for _, k := range []string{"id", "name", "offer_query", "postback_url", "instruction", "conversions", "source", "extracted", "edited", "custom"} {
		if _, ok := got[0][k]; !ok {
			t.Errorf("field %q missing", k)
		}
	}
}

// TestNetTemplatesRouteIsGated checks the access rule and the documented route of each of the
// four template routes: the networks section, no admin-only rule, and the three writes closed
// to an Api-Key.
func TestNetTemplatesRouteIsGated(t *testing.T) {
	cookie := map[string]bool{}
	for _, p := range cookieRoutes {
		cookie[p] = true
	}
	for _, pattern := range []string{
		"GET /api/affiliate-network-templates", "POST /api/affiliate-network-templates",
		"PUT /api/affiliate-network-templates/{id}", "DELETE /api/affiliate-network-templates/{id}",
	} {
		rule, ok := routeAccess[pattern]
		if !ok || rule.sec != secNetworks || rule.admin {
			t.Fatalf("%s: access rule %+v ok=%v", pattern, rule, ok)
		}
		if _, ok := buildRouteDocs()[pattern]; !ok {
			t.Fatalf("%s is not in routeDocs", pattern)
		}
		if write := !strings.HasPrefix(pattern, "GET "); write != cookie[pattern] {
			t.Errorf("%s: cookie-only is %v, want %v", pattern, cookie[pattern], write)
		}
	}
}

// TestNetTemplateEditFillsEmptyAndSurvivesRereads checks the main promise of the edits: a
// template that holds no instruction gets one, an offer query and a postback from a PUT, the
// name stays, other templates stay, and the result is still there when the file is read again
// against a freshly parsed embedded catalog.
func TestNetTemplateEditFillsEmptyAndSurvivesRereads(t *testing.T) {
	g := newTemplateRig(t)
	var bare netTemplate
	for _, tpl := range netTemplateCatalog() {
		if tpl.Instruction == "" && tpl.Source == "keitaro-json" {
			bare = tpl
			break
		}
	}
	if bare.ID == "" {
		t.Skip("every catalog template carries an instruction")
	}
	body := `{"instruction":"<p>My note</p>","postback_url":"{origin}/postback?subid={sid}&status={st}"}`
	code, raw := g.do(http.MethodPut, "/api/affiliate-network-templates/"+bare.ID, body)
	if code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", code, raw)
	}
	got, ok := g.get(bare.ID)
	if !ok || got.Name != bare.Name {
		t.Fatalf("template after the edit: %+v", got)
	}
	if got.Instruction != "<p>My note</p>" || got.PostbackURL != "{origin}/postback?subid={sid}&status={st}" || !got.Edited {
		t.Errorf("edit not applied: %+v", got)
	}
	if got.OfferQuery != bare.OfferQuery {
		t.Errorf("offer query changed to %q, want the catalog value %q", got.OfferQuery, bare.OfferQuery)
	}
	edits, err := loadNetTemplateEdits(g.path)
	if err != nil {
		t.Fatal(err)
	}
	again, found := findTemplateByID(mergeNetTemplates(parseEmbeddedCatalog(t), edits), bare.ID)
	if !found || again.Instruction != got.Instruction || again.Name != bare.Name {
		t.Fatalf("edit lost on a re-read: %+v", again)
	}
	other := netTemplateCatalog()[0]
	if o, _ := g.get(other.ID); o.Edited && other.ID != bare.ID {
		t.Errorf("an edit of one template touched %q", other.Name)
	}
}

// parseEmbeddedCatalog parses net_templates.json again, as a process that just started would.
func parseEmbeddedCatalog(t *testing.T) []netTemplate {
	t.Helper()
	var list []netTemplate
	if err := json.Unmarshal(netTemplatesJSON, &list); err != nil {
		t.Fatal(err)
	}
	return finishNetTemplates(list)
}

// findTemplateByID returns the template with id from list.
func findTemplateByID(list []netTemplate, id string) (netTemplate, bool) {
	for _, tpl := range list {
		if tpl.ID == id {
			return tpl, true
		}
	}
	return netTemplate{}, false
}

// TestNetTemplateEmptyFieldKeepsCatalog checks that an empty field in a PUT, and a field equal
// to the catalog value, are not stored, so the catalog value stays and a later change of the
// catalog would show through.
func TestNetTemplateEmptyFieldKeepsCatalog(t *testing.T) {
	g := newTemplateRig(t)
	base := catalogNetTemplate("godlike-partners")
	if base.OfferQuery == "" || base.PostbackURL == "" {
		t.Fatalf("Godlike.partners lacks its catalog fields: %+v", base)
	}
	body := `{"offer_query":"","postback_url":"` + base.PostbackURL + `","instruction":""}`
	if code, raw := g.do(http.MethodPut, "/api/affiliate-network-templates/godlike-partners", body); code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", code, raw)
	}
	got, _ := g.get("godlike-partners")
	if got.OfferQuery != base.OfferQuery || got.PostbackURL != base.PostbackURL || got.Instruction != base.Instruction || got.Edited {
		t.Errorf("an empty or equal field changed the template: %+v", got)
	}
	if edits, _ := loadNetTemplateEdits(g.path); len(edits) != 0 {
		t.Errorf("nothing should be stored, got %+v", edits)
	}
	g.do(http.MethodPut, "/api/affiliate-network-templates/godlike-partners", `{"instruction":"mine"}`)
	g.do(http.MethodPut, "/api/affiliate-network-templates/godlike-partners", `{"instruction":""}`)
	if got, _ = g.get("godlike-partners"); got.Edited || got.Instruction != base.Instruction {
		t.Errorf("clearing a field should drop its edit: %+v", got)
	}
}

// TestNetTemplateNameOfCatalogNeverChanges checks that a PUT with another name is refused, that
// a name from the catalog cannot be added again, and that the catalog template cannot be removed.
func TestNetTemplateNameOfCatalogNeverChanges(t *testing.T) {
	g := newTemplateRig(t)
	if code, _ := g.do(http.MethodPut, "/api/affiliate-network-templates/godlike-partners", `{"name":"Renamed"}`); code != http.StatusBadRequest {
		t.Errorf("rename status %d, want 400", code)
	}
	if code, _ := g.do(http.MethodPost, "/api/affiliate-network-templates", `{"name":"GODLIKE.partners"}`); code != http.StatusConflict {
		t.Errorf("duplicate name status %d, want 409", code)
	}
	if code, _ := g.do(http.MethodDelete, "/api/affiliate-network-templates/godlike-partners", ""); code != http.StatusNoContent {
		t.Errorf("reset status %d, want 204", code)
	}
	if _, ok := g.get("godlike-partners"); !ok {
		t.Error("a catalog template disappeared after DELETE")
	}
	if code, _ := g.do(http.MethodDelete, "/api/affiliate-network-templates/no-such", ""); code != http.StatusNotFound {
		t.Errorf("unknown id status %d, want 404", code)
	}
}

// TestNetTemplateCustomLifecycle checks adding, editing and removing a template with a new
// name: it appears after the catalog, keeps its instruction as typed, takes new fields on PUT
// as sent, and is gone after DELETE.
func TestNetTemplateCustomLifecycle(t *testing.T) {
	g := newTemplateRig(t)
	code, raw := g.do(http.MethodPost, "/api/affiliate-network-templates",
		`{"name":"  My Network ","offer_query":"sub1={subid}","instruction":"line one\nline two"}`)
	if code != http.StatusCreated {
		t.Fatalf("POST status %d: %s", code, raw)
	}
	got, ok := g.get("my-network")
	if !ok || !got.Custom || got.Name != "My Network" || got.Source != netTplSourceCustom || got.Extracted != netTplPartial {
		t.Fatalf("custom template: %+v ok=%v", got, ok)
	}
	if got.Instruction != "line one\nline two" {
		t.Errorf("instruction %q", got.Instruction)
	}
	if code, _ = g.do(http.MethodPost, "/api/affiliate-network-templates", `{"name":"my network"}`); code != http.StatusConflict {
		t.Errorf("duplicate custom status %d, want 409", code)
	}
	if code, _ = g.do(http.MethodPost, "/api/affiliate-network-templates", `{"name":"...","offer_query":"a=b"}`); code != http.StatusBadRequest {
		t.Errorf("nameless status %d, want 400", code)
	}
	if code, _ = g.do(http.MethodPut, "/api/affiliate-network-templates/my-network", `{"postback_url":"{origin}/postback?subid={s}"}`); code != http.StatusOK {
		t.Fatalf("PUT status %d", code)
	}
	got, _ = g.get("my-network")
	if got.OfferQuery != "" || got.Extracted != netTplPartial || got.PostbackURL == "" {
		t.Errorf("a custom template takes the fields as sent: %+v", got)
	}
	if code, _ = g.do(http.MethodDelete, "/api/affiliate-network-templates/my-network", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE status %d", code)
	}
	if _, ok = g.get("my-network"); ok {
		t.Error("the custom template is still listed")
	}
}

// TestNetTemplateWriteLimits checks the refusals: a body that is not JSON, a field over its limit,
// and a server without an edits file.
func TestNetTemplateWriteLimits(t *testing.T) {
	g := newTemplateRig(t)
	if code, _ := g.do(http.MethodPost, "/api/affiliate-network-templates", `{`); code != http.StatusBadRequest {
		t.Errorf("bad json status %d, want 400", code)
	}
	long := strings.Repeat("a", netTplMaxInstruction+1)
	if code, _ := g.do(http.MethodPut, "/api/affiliate-network-templates/godlike-partners", `{"instruction":"`+long+`"}`); code != http.StatusBadRequest {
		t.Errorf("long instruction status %d, want 400", code)
	}
	s := &Server{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/affiliate-network-templates", strings.NewReader(`{"name":"X"}`))
	s.handleNetworkTemplateCreate(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("no file status %d, want 503", rr.Code)
	}
}

// TestNetTemplateDamagedFileDoesNotBreakTheList checks that an edits file that is not JSON is
// ignored by the list, which serves the plain catalog, and that a write refuses to overwrite it.
func TestNetTemplateDamagedFileDoesNotBreakTheList(t *testing.T) {
	g := newTemplateRig(t)
	if err := os.WriteFile(g.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.get("godlike-partners"); !ok {
		t.Fatal("the list is empty with a damaged edits file")
	}
	if code, _ := g.do(http.MethodPut, "/api/affiliate-network-templates/godlike-partners", `{"instruction":"x"}`); code != http.StatusInternalServerError {
		t.Errorf("write over a damaged file status %d, want 500", code)
	}
	if raw, _ := os.ReadFile(g.path); string(raw) != "{not json" {
		t.Error("a damaged edits file was overwritten")
	}
}

// TestNetTemplatePostbackIsOursOnly checks that a template cannot hold a foreign host or a path
// token: POST and PUT refuse such a postback address with 400, and nothing is stored; the form
// {origin}/postback with or without a query is taken.
func TestNetTemplatePostbackIsOursOnly(t *testing.T) {
	g := newTemplateRig(t)
	for _, bad := range []string{
		"https://tracker.example/abc123/postback?subid={sub1}&from=Net",
		"{origin}/abc123/postback?subid={sub1}",
		"{origin}/postbackx",
		"{origin}/postback?next=https://tracker.example/x",
	} {
		body, _ := json.Marshal(map[string]string{"name": "Foreign", "offer_query": "sub1={subid}", "postback_url": bad})
		if code, _ := g.do(http.MethodPost, "/api/affiliate-network-templates", string(body)); code != http.StatusBadRequest {
			t.Errorf("POST with %q: status %d, want 400", bad, code)
		}
		put, _ := json.Marshal(map[string]string{"postback_url": bad})
		if code, _ := g.do(http.MethodPut, "/api/affiliate-network-templates/godlike-partners", string(put)); code != http.StatusBadRequest {
			t.Errorf("PUT with %q: status %d, want 400", bad, code)
		}
	}
	if _, err := os.Stat(g.path); err == nil {
		t.Error("a refused request wrote the edits file")
	}
	for i, ok := range []string{"{origin}/postback", "{origin}/postback?subid={sub1}&status=lead"} {
		body, _ := json.Marshal(map[string]string{"name": "Ours " + string(rune('A'+i)), "postback_url": ok})
		if code, raw := g.do(http.MethodPost, "/api/affiliate-network-templates", string(body)); code != http.StatusCreated {
			t.Errorf("POST with %q: status %d: %s", ok, code, raw)
		}
	}
}

// TestNetTemplateUnknownNetworkIsAdded checks the body the dialog sends for a network that has no
// template: the name and the offer parameters as the network has them, no instruction, and no
// foreign address anywhere. The template is listed after the catalog as custom; the embedded
// catalog and its 333 names stay as they were.
func TestNetTemplateUnknownNetworkIsAdded(t *testing.T) {
	g := newTemplateRig(t)
	before := len(netTemplateCatalog())
	code, raw := g.do(http.MethodPost, "/api/affiliate-network-templates",
		`{"name":"Unlisted Network","offer_query":"sub1={subid}","postback_url":"","instruction":""}`)
	if code != http.StatusCreated {
		t.Fatalf("POST status %d: %s", code, raw)
	}
	got, ok := g.get("unlisted-network")
	if !ok || !got.Custom || got.OfferQuery != "sub1={subid}" || got.Instruction != "" || got.PostbackURL != "" {
		t.Fatalf("added template: %+v ok=%v", got, ok)
	}
	for _, f := range []string{got.Name, got.OfferQuery, got.PostbackURL, got.Instruction} {
		if strings.Contains(f, "://") {
			t.Errorf("an address leaked into the template: %q", f)
		}
	}
	if len(netTemplateCatalog()) != before || before != 333 {
		t.Errorf("the embedded catalog changed: %d templates, want 333", len(netTemplateCatalog()))
	}
	if code, _ = g.do(http.MethodPost, "/api/affiliate-network-templates", `{"name":"Unlisted Network","offer_query":"sub1={subid}"}`); code != http.StatusConflict {
		t.Errorf("a second add of the same name: status %d, want 409", code)
	}
}