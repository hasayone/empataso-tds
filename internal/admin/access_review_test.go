package admin

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/render"
	"github.com/egerkuzma/emtds/internal/security"
	"github.com/egerkuzma/emtds/internal/store"
)

// setRole replaces the access of the role of the rig.
func (r *rolesRig) setRole(t *testing.T, a security.Access) {
	t.Helper()
	if _, err := r.s.roles.Update(r.roleID, func(role *security.Role) error {
		role.Access = a
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// editorAccess is the access of the role used by the macro and payout tests: every
// user section, full rights on campaigns, offers and landings, and the hidden ids.
func editorAccess(hidden ...string) security.Access {
	full := security.Grant{Mode: security.ModeFull}
	return security.Access{
		Sections: userSectionsAll(),
		Grants:   map[string]security.Grant{"campaigns": full, "offers": full, "landings": full, "sources": full, "networks": full},
		Hidden:   hidden,
	}
}

// storedGroups returns the campaign list the rig serves, for the test to read or seed.
func (r *rolesRig) storedGroups() *memGroups {
	return r.s.cfg.Groups.(*memGroups)
}

// renderMacroTokens reads internal/render/macros.go and returns every macro token it
// recognises, lowercase. Tokens that end without ']' or '}' are prefixes.
// The source is the one place the macro list lives, so a macro added there shows up here.
func renderMacroTokens(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../render/macros.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	add := func(tok string) { seen[strings.ToLower(tok)] = true }
	randName := regexp.MustCompile(`\[(RAND[A-Z]+)-`)
	maxSub := 0
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			if len(x.Elts) == 2 {
				if s, ok := leadingString(x.Elts[0]); ok && strings.HasPrefix(s, "[") {
					add(s)
				}
			}
		case *ast.CaseClause:
			for _, e := range x.List {
				if s, ok := leadingString(e); ok {
					add("{" + s + "}")
				}
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil {
					if m := randName.FindStringSubmatch(s); m != nil {
						add("[" + m[1] + "-")
					}
				}
			}
		case *ast.FuncDecl:
			if x.Name.Name == "subIDIndex" {
				if maxSub < config.MaxSubID {
					maxSub = config.MaxSubID
				}
				ast.Inspect(x, func(m ast.Node) bool {
					if lit, ok := m.(*ast.BasicLit); ok && lit.Kind == token.INT {
						if v, _ := strconv.Atoi(lit.Value); v > maxSub {
							maxSub = v
						}
					}
					return true
				})
			}
		}
		return true
	})
	for i := 1; i <= maxSub; i++ {
		add("{sub_id_" + strconv.Itoa(i) + "}")
	}
	out := make([]string, 0, len(seen))
	for tok := range seen {
		out = append(out, tok)
	}
	return out
}

// leadingString returns the first string literal of e: the literal itself, or the
// left end of a concatenation such as "[PAR-" + n + "]".
func leadingString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		}
	case *ast.BinaryExpr:
		return leadingString(x.X)
	}
	return "", false
}

// macroClassified reports whether tok (lowercase) is in macroFields, macroSafe or
// macroQueryRead. A prefix token matches any entry that starts with it.
func macroClassified(tok string) bool {
	if _, ok := macroFields[tok]; ok {
		return true
	}
	if _, ok := macroSafe[tok]; ok {
		return true
	}
	if _, ok := macroQueryRead[tok]; ok {
		return true
	}
	if strings.HasSuffix(tok, "]") || strings.HasSuffix(tok, "}") {
		return false
	}
	for key := range macroFields {
		if strings.HasPrefix(key, tok) {
			return true
		}
	}
	for key := range macroSafe {
		if strings.HasPrefix(key, tok) {
			return true
		}
	}
	return false
}

func TestRootSubIDMacroUsesRootCIDAccess(t *testing.T) {
	if got := macroFields["{root_subid}"]; got != "root_cid" {
		t.Fatalf("root_subid field = %q, want root_cid", got)
	}
}

func TestEveryRenderMacroIsClassified(t *testing.T) {
	tokens := renderMacroTokens(t)
	if len(tokens) < 30 {
		t.Fatalf("macro list of render looks incomplete: %v", tokens)
	}
	for _, tok := range tokens {
		if !macroClassified(tok) {
			t.Errorf("macro %s of render is neither in macroFields nor in macroSafe", tok)
		}
	}
}

func TestMacroTableHoldsOnlyRealMacros(t *testing.T) {
	deps := render.MacroDeps{
		Key: "K", Path: "P", IP: "1.1.1.1", Country: "C", City: "Ci", Region: "R", ASN: "A", Org: "O",
		Timezone: "T", TZOffset: "+3", Lang: "L", Device: "D", Operator: "Op", Domain: "Do", UserAgent: "U",
		CID: "Cid", Pars: []string{"p1", "p2", "p3", "p4", "p5"}, Status: "st", Payout: "7", Offer: "of",
	}
	for i := range deps.SubIDs {
		deps.SubIDs[i] = "s" + strconv.Itoa(i+1)
	}
	for tok, field := range macroFields {
		sample := tok
		if strings.HasPrefix(tok, "[") {
			sample = strings.ToUpper(tok)
		}
		if render.Expand(sample, deps) == sample {
			t.Errorf("macroFields lists %s for %s but render does not expand it", sample, field)
		}
		if !hiddenIDOK(field) {
			t.Errorf("macro %s points at %s, which cannot be hidden", tok, field)
		}
	}
}

func TestHiddenMacroRefusedInCampaign(t *testing.T) {
	cases := []struct{ hidden, macro string }{
		{"ip", "[IP]"}, {"ip", "[ip]"}, {"useragent", "[USERAGENT]"}, {"asn", "[ASN]"},
		{"keyword", "{keyword}"}, {"keyword", "[KEY]"}, {"status", "{status}"}, {"profit", "{payout}"},
		{"sub_id_3", "{sub_id_3}"}, {"sub_id_3", "[PAR-3]"}, {"country", "[()COUNTRY()]"}, {"cid", "{subid}"},
		{"root_cid", "{root_subid}"},
	}
	rig := newRolesRig(t, "")
	for _, c := range cases {
		rig.setRole(t, editorAccess(c.hidden))
		body := config.Group{ID: "c1", Name: "one", Status: true, Redirect: "stop", Out: "https://t.example/?x=" + c.macro}
		code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/groups/c1", body)
		if code != http.StatusBadRequest || !strings.Contains(string(raw), c.hidden) {
			t.Errorf("hide %s, macro %s: %d %s", c.hidden, c.macro, code, raw)
		}
	}
	if got := rig.storedGroups().list[0].Out; got != "" {
		t.Fatalf("a refused save changed the campaign: %q", got)
	}

	rig.setRole(t, editorAccess("ip"))
	ok := config.Group{ID: "c1", Name: "one", Status: true, Redirect: "stop", Out: "https://t.example/?c=[COUNTRY]"}
	if code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/groups/c1", ok); code != http.StatusOK {
		t.Fatalf("a macro of a visible field: %d %s", code, raw)
	}
	rig.hide(t)
	rig.setRole(t, editorAccess())
	with := config.Group{ID: "c1", Name: "one", Status: true, Redirect: "stop", Out: "https://t.example/?ip=[IP]"}
	if code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/groups/c1", with); code != http.StatusOK {
		t.Fatalf("nothing hidden: %d %s", code, raw)
	}
}

func TestStoredMacroDoesNotBlockOtherEdits(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.storedGroups().list[0].Out = "https://a.example/?ip=[IP]"
	rig.setRole(t, editorAccess("ip"))
	same := config.Group{ID: "c1", Name: "renamed", Status: true, Redirect: "stop", Out: "https://a.example/?ip=[IP]"}
	if code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/groups/c1", same); code != http.StatusOK {
		t.Fatalf("the macro the administrator wrote blocks a rename: %d %s", code, raw)
	}
	twice := same
	twice.Out = "https://a.example/?ip=[IP]&again=[IP]"
	if code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/groups/c1", twice); code != http.StatusBadRequest {
		t.Fatalf("a second use of the macro: %d", code)
	}
	moved := same
	moved.Out = "https://b.example/?x=[IP]"
	if code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/groups/c1", moved); code != http.StatusBadRequest {
		t.Fatalf("a link with the macro of the administrator may not change: %d", code)
	}
}

func TestHiddenMacroRefusedInCatalogs(t *testing.T) {
	rig := newRolesRig(t, "")
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/offers", config.Offer{ID: "o1", Name: "o", URL: "https://x.example/?ua=[USERAGENT]", Status: true}); code != http.StatusCreated {
		t.Fatalf("seed offer %d %s", code, raw)
	}
	rig.setRole(t, editorAccess("useragent", "ip"))
	post := func(path string, body any) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, path, body)
		return code
	}
	if code := post("/api/offers", config.Offer{ID: "o2", Name: "o", URL: "https://x.example/?ua=[USERAGENT]", Status: true}); code != http.StatusBadRequest {
		t.Errorf("new offer with a hidden macro: %d", code)
	}
	if code := post("/api/offers", config.Offer{ID: "o3", Name: "o", URL: "https://x.example/?c=[COUNTRY]", Status: true}); code != http.StatusCreated {
		t.Errorf("new offer with a visible macro: %d", code)
	}
	if code := post("/api/landings", config.Landing{ID: "l1", Name: "l", URL: "https://l.example/?ip=[ip]", Status: true}); code != http.StatusBadRequest {
		t.Errorf("new landing with a hidden macro: %d", code)
	}
	put := func(url string) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/offers/o1", config.Offer{ID: "o1", Name: "renamed", URL: url, Status: true})
		return code
	}
	if code := put("https://x.example/?ua=[USERAGENT]"); code != http.StatusOK {
		t.Errorf("rename keeps the stored macro: %d", code)
	}
	if code := put("https://x.example/?ua=[USERAGENT]&ip=[IP]"); code != http.StatusBadRequest {
		t.Errorf("added macro: %d", code)
	}
}

func TestHiddenMacroRefusedInOutboundPostbacks(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.storedGroups().list[0].Postbacks = []config.OutboundPostback{{URL: "https://p.example/?ip=[IP]", Method: "GET", Statuses: []string{"lead"}}}
	rig.setRole(t, editorAccess("ip", "status"))
	put := func(list []config.OutboundPostback) int {
		code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/campaigns/c1/postbacks", list)
		return code
	}
	if code := put([]config.OutboundPostback{{URL: "https://p.example/?ip=[IP]", Method: "GET", Statuses: []string{"lead", "sale"}}}); code != http.StatusOK {
		t.Errorf("stored macro kept: %d", code)
	}
	if code := put([]config.OutboundPostback{{URL: "https://p.example/?s={status}", Method: "GET", Statuses: []string{"lead"}}}); code != http.StatusBadRequest {
		t.Errorf("new hidden macro: %d", code)
	}
	if code := put([]config.OutboundPostback{{URL: "https://p.example/?p={payout}", Method: "GET", Statuses: []string{"lead"}}}); code != http.StatusOK {
		t.Errorf("visible macro: %d", code)
	}
}

func TestOfferPayoutHiddenWithProfit(t *testing.T) {
	rig := newRolesRig(t, "")
	seed := config.Offer{ID: "p1", Name: "p", URL: "https://x.example/", Status: true, PayoutModel: "cpa", Payout: 5, PayoutCurrency: "USD"}
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/offers", seed); code != http.StatusCreated {
		t.Fatalf("seed %d %s", code, raw)
	}
	rig.setRole(t, editorAccess())
	_, body := rig.get(t, rig.user, "/api/offers/p1")
	if !strings.Contains(body, `"payout"`) || !strings.Contains(body, "USD") {
		t.Fatalf("a viewer who may see profit gets no payout: %s", body)
	}

	rig.setRole(t, editorAccess("profit"))
	for _, path := range []string{"/api/offers", "/api/offers/p1"} {
		if _, body := rig.get(t, rig.user, path); strings.Contains(body, "payout\"") || strings.Contains(body, "payout_currency") || strings.Contains(body, "USD") {
			t.Errorf("%s leaks the payout: %s", path, body)
		}
	}
	changed := seed
	changed.Name, changed.Payout, changed.PayoutCurrency = "renamed", 99, "EUR"
	code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPut, "/api/offers/p1", changed)
	if code != http.StatusOK || strings.Contains(string(raw), "payout\"") || strings.Contains(string(raw), "EUR") {
		t.Fatalf("replace %d %s", code, raw)
	}
	var stored config.Offer
	_, adminBody := rig.get(t, rig.admin, "/api/offers/p1")
	if err := json.Unmarshal([]byte(adminBody), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Name != "renamed" || stored.Payout != 5 || stored.PayoutCurrency != "USD" {
		t.Fatalf("the hidden payout was overwritten: %+v", stored)
	}

	created := config.Offer{ID: "p2", Name: "n", URL: "https://y.example/", Status: true, Payout: 10, PayoutCurrency: "USD"}
	if code, raw := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/offers", created); code != http.StatusCreated || strings.Contains(string(raw), "USD") {
		t.Fatalf("create %d %s", code, raw)
	}
	_, adminBody = rig.get(t, rig.admin, "/api/offers/p2")
	if strings.Contains(adminBody, "payout\"") || strings.Contains(adminBody, "USD") {
		t.Fatalf("a payout set by a viewer who cannot see it was stored: %s", adminBody)
	}
}

// meOf decodes GET /api/me for the session of c.
func meOf(t *testing.T, c *http.Client, base string) meResponse {
	t.Helper()
	resp, err := c.Get(base + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var me meResponse
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	return me
}

func TestAttachingRoleEmptiesOwnAccess(t *testing.T) {
	rig := newRolesRig(t, "")
	own := map[string]any{"sections": []string{"logs"}, "grants": map[string]any{}}
	code, raw := rig.adminAPI(t, http.MethodPost, "/api/users", map[string]any{
		"login": "withrole", "password": "secret1", "enabled": true, "role_id": rig.roleID, "access": own,
	})
	if code != http.StatusBadRequest || !strings.Contains(string(raw), "own access") {
		t.Fatalf("role with own access on create: %d %s", code, raw)
	}
	if _, err := rig.s.createUser(userDraft{Login: "x1", Password: "secret1", Type: security.UserRegular, RoleID: rig.roleID,
		Access: security.Access{Hidden: []string{"ip"}}}); err == nil {
		t.Fatal("a draft with a role and hidden fields must be refused")
	}
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/users", map[string]any{
		"login": "withrole", "password": "secret1", "enabled": true, "role_id": rig.roleID,
	}); code != http.StatusCreated {
		t.Fatalf("role without own access: %d %s", code, raw)
	}

	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/users", map[string]any{
		"login": "plain", "password": "secret1", "enabled": true, "access": own,
	}); code != http.StatusCreated {
		t.Fatalf("plain user: %d %s", code, raw)
	}
	plain, _ := rig.s.users.ByLogin("plain")
	c := newClient(t)
	login(t, c, rig.srv.URL, "plain", "secret1")
	if got := meOf(t, c, rig.srv.URL).Pages; !reflect.DeepEqual(got, []string{"logs"}) {
		t.Fatalf("own access before the role: %v", got)
	}
	if code, raw := rig.adminAPI(t, http.MethodPut, "/api/users/"+plain.ID, map[string]any{"role_id": rig.roleID}); code != http.StatusOK {
		t.Fatalf("attach %d %s", code, raw)
	}
	if u, _ := rig.s.users.ByLogin("plain"); len(u.Access.Sections) != 0 || len(u.Access.Grants) != 0 || len(u.Access.Hidden) != 0 {
		t.Fatalf("own access survived the role: %+v", u.Access)
	}
	if got := meOf(t, c, rig.srv.URL).Pages; !reflect.DeepEqual(got, sectionStrings(userSections)) {
		t.Fatalf("role access: %v", got)
	}
	if code, raw := rig.adminAPI(t, http.MethodPut, "/api/users/"+plain.ID, map[string]any{"role_id": ""}); code != http.StatusOK {
		t.Fatalf("detach %d %s", code, raw)
	}
	if got := meOf(t, c, rig.srv.URL).Pages; len(got) != 0 {
		t.Fatalf("after the role is taken off the user has no access: %v", got)
	}
}

func TestStripKeysEditsInPlace(t *testing.T) {
	tree := map[string]any{
		"ip": "1", "keep": 1.0,
		"rows": []any{map[string]any{"ip": "2", "n": 1.0, "deep": map[string]any{"ip": "3", "x": 2.0}}},
	}
	keys := map[string]bool{"ip": true}
	out := stripKeys(tree, keys)
	if reflect.ValueOf(out).Pointer() != reflect.ValueOf(tree).Pointer() {
		t.Fatal("stripKeys returned a copy")
	}
	want := map[string]any{
		"keep": 1.0,
		"rows": []any{map[string]any{"n": 1.0, "deep": map[string]any{"x": 2.0}}},
	}
	if !reflect.DeepEqual(tree, want) {
		t.Fatalf("tree %v", tree)
	}
	if n := testing.AllocsPerRun(50, func() { stripKeys(tree, keys) }); n != 0 {
		t.Fatalf("stripKeys allocates %v times on a clean tree", n)
	}
}

// BenchmarkStripKeys measures the cost of removing hidden keys from a table of
// 2000 rows; it must not allocate per row.
func BenchmarkStripKeys(b *testing.B) {
	keys := map[string]bool{"ip": true, "profit": true}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		rows := make([]any, 2000)
		for j := range rows {
			rows[j] = map[string]any{"ip": "1.1.1.1", "profit": 1.5, "country": "x", "n": 3.0}
		}
		b.StartTimer()
		stripKeys(map[string]any{"rows": rows}, keys)
	}
}

func TestFoldConfirmed(t *testing.T) {
	if got := foldConfirmed(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil input: %#v", got)
	}
	rows := []store.StatusFact{
		{OfferID: "o1", NetworkID: "n", GroupID: "c1", Status: "lead", N: 2, Profit: 5, DepSum: 1},
		{OfferID: "o2", NetworkID: "n", GroupID: "c1", Status: "rejected", N: 9, DepSum: 9},
		{OfferID: "o1", NetworkID: "n", GroupID: "c1", Status: "sale", N: 1, Profit: 2, DepSum: 2},
		{OfferID: "o1", NetworkID: "n", GroupID: "c1", Status: "rejected", N: 4},
		{OfferID: "", NetworkID: "", GroupID: "c1", Status: "lead", N: 3, Profit: 1.5, DepSum: 3},
	}
	got := foldConfirmed(rows)
	want := []store.StatusFact{
		{OfferID: "o1", NetworkID: "n", GroupID: "c1", Status: "sale", N: 3, Profit: 7, DepSum: 3},
		{GroupID: "c1", Status: "sale", N: 3, Profit: 1.5, DepSum: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if rows[0].Status != "lead" {
		t.Fatal("foldConfirmed changed its input")
	}
}

// factsStats serves fixed conversion status rows through the facts routes.
type factsStats struct {
	richStats
	status []store.StatusFact
}

func (f factsStats) ClickFacts(context.Context, time.Time, time.Time) (store.Facts, error) {
	return store.Facts{Ready: true, Clicks: []store.ClickFact{}, Status: f.status}, nil
}

func (f factsStats) ClickFactsFiltered(ctx context.Context, from, to time.Time, _ []store.Cond) (store.Facts, error) {
	return f.ClickFacts(ctx, from, to)
}

func TestFactsFoldWhenStatusHidden(t *testing.T) {
	rows := []store.StatusFact{
		{OfferID: "o1", NetworkID: "n", GroupID: "c1", Status: "lead", N: 2, Profit: 5},
		{OfferID: "o1", NetworkID: "n", GroupID: "c1", Status: "sale", N: 1, Profit: 2},
		{OfferID: "o1", NetworkID: "n", GroupID: "c1", Status: "rejected", N: 4},
		{GroupID: "c1", Status: "lead", N: 3, Profit: 1.5},
	}
	rig := newRolesRigStats(t, "", factsStats{status: rows})
	type facts struct {
		Status []store.StatusFact `json:"status"`
	}
	read := func(c *http.Client) facts {
		code, body := rig.get(t, c, "/api/stats/facts")
		var out facts
		if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("facts %d %s", code, body)
		}
		return out
	}
	if got := read(rig.admin); len(got.Status) != 4 {
		t.Fatalf("administrator sees the rows as stored: %+v", got.Status)
	}
	rig.hide(t, "status")
	got := read(rig.user)
	want := []store.StatusFact{
		{OfferID: "o1", NetworkID: "n", GroupID: "c1", Status: "sale", N: 3, Profit: 7},
		{GroupID: "c1", Status: "sale", N: 3, Profit: 1.5},
	}
	if !reflect.DeepEqual(got.Status, want) {
		t.Fatalf("folded rows %+v", got.Status)
	}
	var admin, user int64
	for _, r := range read(rig.admin).Status {
		if r.Status == "lead" || r.Status == "sale" {
			admin += r.N
		}
	}
	for _, r := range got.Status {
		user += r.N
	}
	if admin != user {
		t.Fatalf("confirmed count differs: administrator %d, folded %d", admin, user)
	}

	rig.hide(t, "status", "profit")
	_, body := rig.get(t, rig.user, "/api/stats/facts")
	if strings.Contains(body, "profit") || strings.Contains(body, "rejected") || !strings.Contains(body, `"status":"sale"`) {
		t.Fatalf("status and profit hidden: %s", body)
	}
}

func TestTimestampCannotBeHidden(t *testing.T) {
	if hiddenIDOK("ts") {
		t.Fatal("ts is accepted as a hidden id")
	}
	if _, err := normalizeAccess(security.Access{Hidden: []string{"ts"}}); !errorsIsAccess(err, errAccessHidden) {
		t.Fatalf("normalizeAccess: %v", err)
	}
	rig := newRolesRig(t, "")
	code, raw := rig.adminAPI(t, http.MethodPost, "/api/roles", map[string]any{"name": "t", "access": map[string]any{"hidden": []string{"ts"}}})
	if code != http.StatusBadRequest {
		t.Fatalf("role hiding ts: %d %s", code, raw)
	}
	_, body := rig.get(t, rig.admin, "/api/log-fields")
	var fields fieldsResponse
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range fields.Fields {
		seen[f.Name] = f.Hideable
	}
	if hide, ok := seen["ts"]; !ok || hide {
		t.Errorf("ts must be listed and not hideable: %v %v", ok, hide)
	}
	if !seen["ip"] || !seen["campaign"] {
		t.Errorf("ordinary fields must be hideable: %v", seen)
	}
	if _, ok := seen["step"]; ok {
		t.Error("step must not be listed")
	}
}

func TestRolesReloadAfterManualRepair(t *testing.T) {
	rig := newRolesRig(t, "")
	if code, raw := rig.adminAPI(t, http.MethodPost, "/api/roles", map[string]any{"name": "extra"}); code != http.StatusCreated {
		t.Fatalf("save %d %s", code, raw)
	}
	path := filepath.Join(rig.dir, "roles.json")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reload := func() (int, string) {
		code, raw := rig.adminAPI(t, http.MethodPost, "/api/roles/reload", nil)
		return code, string(raw)
	}

	for name, damaged := range map[string]string{
		"bad json":      "{",
		"admin section": `[{"id":"r1","name":"x","access":{"sections":["users"],"grants":{}}}]`,
		"hidden ts":     `[{"id":"r1","name":"x","access":{"sections":[],"grants":{},"hidden":["ts"]}}]`,
	} {
		if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, body := reload(); code != http.StatusServiceUnavailable || !strings.Contains(body, "roles unavailable") {
			t.Fatalf("%s: reload %d %s", name, code, body)
		}
		if rig.s.rolesEnabled() || rig.s.roles.Failure() == "" {
			t.Fatalf("%s: the failure is not recorded", name)
		}
		if got := rig.me(t).Pages; len(got) != 0 {
			t.Fatalf("%s: a user with a role keeps access: %v", name, got)
		}
		if code, _ := rig.adminAPI(t, http.MethodGet, "/api/roles", nil); code != http.StatusServiceUnavailable {
			t.Fatalf("%s: list %d", name, code)
		}
		if code, _ := rig.adminAPI(t, http.MethodPost, "/api/roles", map[string]any{"name": "n"}); code != http.StatusServiceUnavailable {
			t.Fatalf("%s: create %d", name, code)
		}
		if got, _ := os.ReadFile(path); string(got) != damaged {
			t.Fatalf("%s: the damaged file was overwritten", name)
		}
	}

	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, body := reload(); code != http.StatusOK || !strings.Contains(body, `"roles":2`) {
		t.Fatalf("reload of a repaired file: %d %s", code, body)
	}
	if !rig.s.rolesEnabled() {
		t.Fatal("failure not cleared")
	}
	if got := rig.me(t).Pages; !reflect.DeepEqual(got, sectionStrings(userSections)) {
		t.Fatalf("access after repair: %v", got)
	}
	if code, _ := rig.adminAPI(t, http.MethodPost, "/api/roles", map[string]any{"name": "after"}); code != http.StatusCreated {
		t.Fatalf("create after repair: %d", code)
	}
	if code, _ := rig.api(t, rig.user, rig.ucsrf, http.MethodPost, "/api/roles/reload", nil); code != http.StatusForbidden {
		t.Fatalf("reload by a user: %d", code)
	}
}

func TestRolesReloadWithoutStorage(t *testing.T) {
	s := newRolesServer(t, t.TempDir())
	s.cfg.RolesFile, s.roles = "", nil
	srv := newRolesHTTP(t, s)
	admin := newClient(t)
	_, csrf := login(t, admin, srv.URL, "admin", "p@ss")
	if code, _, _ := doJSON(admin, http.MethodPost, srv.URL+"/api/roles/reload", csrf, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("reload without storage: %d", code)
	}
}

func TestSectionRoutesLeakNoHiddenFieldToRole(t *testing.T) {
	rig := newRolesRig(t, "")
	full := adminJSONRoutes(t, rig)
	var all []string
	for id := range hiddenIDs {
		all = append(all, id)
	}
	rig.hide(t, all...)
	hidden := map[string]bool{}
	for _, id := range all {
		for _, k := range keysGone(id) {
			hidden[k] = true
		}
	}
	// With everything hidden the keys that remain are counters and wrappers.
	for ep, keys := range full {
		code, body := rig.get(t, rig.user, ep)
		if code != http.StatusOK || !jsonBody(body) {
			continue
		}
		got := jsonKeys(t, body)
		for k := range keys {
			if hidden[k] && got[k] {
				if strings.HasPrefix(ep, "/api/stats/facts") && k == "status" {
					continue
				}
				t.Errorf("%s still has key %q with every field hidden", ep, k)
			}
		}
	}
}
