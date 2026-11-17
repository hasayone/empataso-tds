package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
	"github.com/hasayone/empataso-tds/internal/store"
)

// populated returns a T whose every exported field holds a distinct non-zero value.
// Strings are "S_<json name>", so a leaked field is visible in the body by its value.
func populated[T any]() T {
	var v T
	fillValue(reflect.ValueOf(&v).Elem(), "x")
	return v
}

// jsonName returns the JSON key of a struct field, or its lower-case name without a tag.
func jsonName(f reflect.StructField) string {
	if tag := strings.Split(f.Tag.Get("json"), ",")[0]; tag != "" {
		return tag
	}
	return strings.ToLower(f.Name)
}

// fillValue sets v and everything below it to non-zero values. name is the JSON key of v.
func fillValue(v reflect.Value, name string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("S_" + name)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Slice:
		n := 1
		if v.Type().Elem().Kind() == reflect.String {
			n = config.MaxRuleSubID
		}
		v.Set(reflect.MakeSlice(v.Type(), n, n))
		for i := 0; i < n; i++ {
			fillValue(v.Index(i), fmt.Sprintf("%s_%d", name, i))
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		elem := reflect.New(v.Type().Elem()).Elem()
		fillValue(elem, name)
		v.SetMapIndex(reflect.ValueOf("USD"), elem)
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if f := v.Type().Field(i); f.IsExported() {
				fillValue(v.Field(i), jsonName(f))
			}
		}
	}
}

// richStats answers every statistics call with fully populated rows.
type richStats struct{ fakeStats }

func (richStats) Summary(context.Context, time.Time, time.Time) (store.StatsSummary, error) {
	return populated[store.StatsSummary](), nil
}
func (r richStats) SummaryFiltered(ctx context.Context, from, to time.Time, _ []store.Cond) (store.StatsSummary, error) {
	return r.Summary(ctx, from, to)
}
func (richStats) TimeSeries(context.Context, time.Time, time.Time, int) ([]store.TSPoint, error) {
	return []store.TSPoint{populated[store.TSPoint]()}, nil
}
func (r richStats) TimeSeriesFiltered(ctx context.Context, from, to time.Time, step int, _ []store.Cond) ([]store.TSPoint, error) {
	return r.TimeSeries(ctx, from, to, step)
}
func (richStats) Performance(context.Context, time.Time, time.Time) ([]store.PerfRow, error) {
	return []store.PerfRow{populated[store.PerfRow]()}, nil
}
func (r richStats) PerformanceFiltered(ctx context.Context, from, to time.Time, _ []store.Cond) ([]store.PerfRow, error) {
	return r.Performance(ctx, from, to)
}
func (richStats) Logs(context.Context, store.LogFilter) ([]store.LogRow, int64, error) {
	return []store.LogRow{populated[store.LogRow]()}, 1, nil
}
func (richStats) QueryPostbacks(context.Context, store.PostbackQuery) ([]store.PostbackRow, store.PostbackTotals, error) {
	return []store.PostbackRow{populated[store.PostbackRow]()}, store.PostbackTotals{Profit: 3, Count: 1}, nil
}
func (richStats) Slice(context.Context, time.Time, time.Time) ([]store.SliceRow, error) {
	return []store.SliceRow{populated[store.SliceRow]()}, nil
}
func (r richStats) SliceFiltered(ctx context.Context, from, to time.Time, _ []store.Cond) ([]store.SliceRow, error) {
	return r.Slice(ctx, from, to)
}
func (richStats) ClickFacts(context.Context, time.Time, time.Time) (store.Facts, error) {
	return populated[store.Facts](), nil
}
func (r richStats) ClickFactsFiltered(ctx context.Context, from, to time.Time, _ []store.Cond) (store.Facts, error) {
	return r.ClickFacts(ctx, from, to)
}
func (richStats) KeywordStats(context.Context, time.Time, time.Time, []store.Cond, store.KeywordMode) (store.KeywordStats, error) {
	return store.KeywordStats{Rows: []store.KeywordRow{populated[store.KeywordRow]()}, Truncated: true, ConvReady: true}, nil
}
func (richStats) KeywordTrend(context.Context, time.Time, time.Time, []store.Cond, store.KeywordTrendParams) (store.KeywordTrend, error) {
	return store.KeywordTrend{
		Days:      []string{"2026-01-02"},
		Series:    []store.KeywordSeries{populated[store.KeywordSeries]()},
		ConvReady: true,
	}, nil
}

// rolesRig is a server with one user whose access comes from one role.
type rolesRig struct {
	s      *Server
	srv    *httptest.Server
	dir    string
	roleID string
	user   *http.Client
	admin  *http.Client
	csrf   string // CSRF token of admin
	ucsrf  string // CSRF token of user
}

// newRolesRig starts an admin server with users.json, roles.json and richStats.
// The user "ops" has every user section and a role with no hidden fields.
// Files lives in dir; when dir is empty a temporary directory is used.
func newRolesRig(t *testing.T, dir string) *rolesRig {
	t.Helper()
	return newRolesRigStats(t, dir, richStats{})
}

// newRolesRigStats is newRolesRig over the statistics source stats.
func newRolesRigStats(t *testing.T, dir string, stats StatsProvider) *rolesRig {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	s := newRolesServerStats(t, dir, stats)
	role, err := s.roles.Add(security.Role{Name: "ops", Access: security.Access{Sections: userSectionsAll()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.createUser(userDraft{
		Login: "ops", Password: "secret1", Type: security.UserRegular, Enabled: true, RoleID: role.ID,
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	rig := &rolesRig{s: s, srv: srv, dir: dir, roleID: role.ID, user: newClient(t), admin: newClient(t)}
	code, ucsrf := login(t, rig.user, srv.URL, "ops", "secret1")
	if code != http.StatusOK {
		t.Fatalf("ops login %d", code)
	}
	rig.ucsrf = ucsrf
	_, rig.csrf = login(t, rig.admin, srv.URL, "admin", "p@ss")
	return rig
}

// newRolesServer builds a server over dir with the files of the roles tests.
func newRolesServer(t *testing.T, dir string) *Server {
	t.Helper()
	return newRolesServerStats(t, dir, richStats{})
}

// newRolesServerStats is newRolesServer over the statistics source stats.
// The offer, landing and shared postback catalogs live in dir.
func newRolesServerStats(t *testing.T, dir string, stats StatsProvider) *Server {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		UsersFile:    filepath.Join(dir, "users.json"),
		RolesFile:    filepath.Join(dir, "roles.json"),
		Sessions:     security.NewMemoryStore(),
		Stats:        stats,
		Groups:       &memGroups{list: scopeCampaigns()},
		Offers:       NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings:     NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:      NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks:     NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		LandingFiles: NewFileLandings(filepath.Join(dir, "landing-files")),
		S2S:          NewJSONStore[config.OutboundPostback](filepath.Join(dir, "s2s.json")),
		Keys:         fakeKeys{},
		Limiter:      allowAll{}, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// hide makes the role of the rig hide exactly ids; they are stored as given.
func (r *rolesRig) hide(t *testing.T, ids ...string) {
	t.Helper()
	if _, err := r.s.roles.Update(r.roleID, func(role *security.Role) error {
		role.Access.Hidden = ids
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// get requests path with c and returns the status and the body.
func (r *rolesRig) get(t *testing.T, c *http.Client, path string) (int, string) {
	t.Helper()
	resp, err := c.Get(r.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// jsonKeys collects every object key in the decoded JSON body.
func jsonKeys(t *testing.T, body string) map[string]bool {
	t.Helper()
	var raw any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("bad json %q: %v", body, err)
	}
	out := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				out[k] = true
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(raw)
	return out
}

// keysGone is the test oracle: the JSON keys that must disappear when the journal field is hidden.
// It is written out here, apart from fieldExtraKeys, so a wrong mapping fails the test.
func keysGone(field string) []string {
	// USD stands for the currency codes that key the per-currency cost maps.
	cost := []string{"cost_cents", "cost", "spend", "cost_cur", "currency", "USD"}
	switch field {
	case "campaign":
		return []string{"campaign", "group"}
	case "campaign_id":
		return []string{"campaign_id", "group_id"}
	case "uniq":
		return []string{"uniq", "unique", "uniq_first"}
	case "bot":
		return []string{"bot", "bots"}
	case "offer_id":
		return []string{"offer_id", "offer"}
	case "source_id":
		return []string{"source_id", "source"}
	case "profit":
		return []string{"profit", "revenue", "payout", "depsum"}
	case "status":
		return []string{"status", "original_status", "prev_status", "leads", "sales", "rejected"}
	case "os":
		return []string{"os", "os_version"}
	case "browser":
		return []string{"browser", "browser_ver"}
	case "brand", "device":
		return []string{field, "device_model"}
	case "cost_cents", "cost_cur", "cost":
		return cost
	}
	return []string{field}
}

// sectionsUnderTest are the non-admin sections whose GET routes show click data.
var sectionsUnderTest = map[section]bool{
	secDashboard: true, secFlows: true, secLogs: true, secConversions: true, secReport: true, secKeywords: true,
}

// routeQueries gives the GET routes that need parameters a query to answer at all.
var routeQueries = map[string]string{
	"/api/stats/breakdown": "?dim=device",
	"/api/log-values":      "?table=events&field=device",
}

// statsEndpoints are the GET paths of routeAccess in sectionsUnderTest, so a new route
// of those sections is covered without touching the test. A path parameter becomes c1
// for a campaign id and x otherwise.
var statsEndpoints = routeGETs()

// routeGETs lists the paths of the GET routes of routeAccess that belong to
// sectionsUnderTest and are not administrator-only, sorted.
func routeGETs() []string {
	var out []string
	for pattern, rule := range routeAccess {
		path, ok := strings.CutPrefix(pattern, "GET ")
		if !ok || rule.admin || !sectionsUnderTest[rule.sec] {
			continue
		}
		path = strings.ReplaceAll(path, "/campaigns/{id}/", "/campaigns/c1/")
		for strings.Contains(path, "{") {
			i, j := strings.Index(path, "{"), strings.Index(path, "}")
			path = path[:i] + "x" + path[j+1:]
		}
		out = append(out, path+routeQueries[path])
	}
	sort.Strings(out)
	return out
}

// jsonBody reports whether body is a JSON object or array.
func jsonBody(body string) bool {
	body = strings.TrimSpace(body)
	return strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[")
}

// adminJSONRoutes returns the routes whose administrator answer is JSON with 200,
// with the keys of each answer.
func adminJSONRoutes(t *testing.T, rig *rolesRig) map[string]map[string]bool {
	t.Helper()
	full := map[string]map[string]bool{}
	for _, ep := range statsEndpoints {
		code, body := rig.get(t, rig.admin, ep)
		if code == http.StatusOK && jsonBody(body) {
			full[ep] = jsonKeys(t, body)
		}
	}
	if len(full) < 9 {
		t.Fatalf("only %d routes answer JSON to an administrator: %v", len(full), statsEndpoints)
	}
	return full
}

func TestHiddenFieldAbsentFromJSON(t *testing.T) {
	rig := newRolesRig(t, "")
	full := adminJSONRoutes(t, rig)
	for _, f := range store.StaticFields() {
		if strings.HasPrefix(f.Name, "sub_id_") || !hiddenIDOK(f.Name) {
			continue
		}
		rig.hide(t, f.Name)
		for ep, keys := range full {
			code, body := rig.get(t, rig.user, ep)
			if code == http.StatusBadRequest || code == http.StatusForbidden {
				continue // the route refuses the hidden field itself
			}
			if code != http.StatusOK {
				t.Fatalf("hide %s %s: %d %s", f.Name, ep, code, body)
			}
			got := jsonKeys(t, body)
			gone := map[string]bool{}
			for _, hidden := range append([]string{f.Name}, derivedFields[f.Name]...) {
				for _, k := range keysGone(hidden) {
					gone[k] = true
				}
			}
			if strings.HasPrefix(ep, "/api/postbacks") && f.Name == "profit" {
				gone["total"] = true
			}
			if strings.HasPrefix(ep, "/api/stats/facts") && f.Name == "status" {
				// Facts keep the status rows folded to confirmed ones, see TestFactsFoldWhenStatusHidden.
				continue
			}
			for k := range keys {
				if gone[k] && got[k] {
					t.Errorf("hide %s: %s still has key %q", f.Name, ep, k)
				}
				if !gone[k] && !got[k] {
					t.Errorf("hide %s: %s lost unrelated key %q", f.Name, ep, k)
				}
			}
		}
	}
}
func TestHiddenSubIDsBlankedInRows(t *testing.T) {
	rig := newRolesRig(t, "")
	for _, ep := range []string{"/api/logs", "/api/postbacks"} {
		rig.hide(t, "sub_id_3", "sub_id_12")
		_, body := rig.get(t, rig.user, ep)
		var out struct {
			Rows []struct {
				Subs []string `json:"subs"`
			} `json:"rows"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Rows) != 1 {
			t.Fatalf("%s body %s err %v", ep, body, err)
		}
		subs := out.Rows[0].Subs
		if len(subs) != config.MaxRuleSubID {
			t.Fatalf("%s subs %v", ep, subs)
		}
		for i, v := range subs {
			hidden := i == 2 || i == 11
			if hidden && v != "" {
				t.Errorf("%s: sub %d leaked %q", ep, i+1, v)
			}
			if !hidden && v == "" {
				t.Errorf("%s: sub %d lost", ep, i+1)
			}
		}
		all := make([]string, 0, config.MaxRuleSubID)
		for n := 1; n <= config.MaxRuleSubID; n++ {
			all = append(all, config.SubIDRuleKey(n))
		}
		rig.hide(t, all...)
		_, body = rig.get(t, rig.user, ep)
		if jsonKeys(t, body)["subs"] {
			t.Errorf("%s: subs key kept with every sub id hidden: %s", ep, body)
		}
	}
}

func TestHiddenFieldRejectedInFilters(t *testing.T) {
	rig := newRolesRig(t, "")
	for _, f := range store.StaticFields() {
		rig.hide(t, f.Name)
		paths := []string{
			"/api/logs?f=" + f.Name + ":eq:x",
			"/api/postbacks?f=" + f.Name + ":eq:x",
			"/api/report?f=" + f.Name + ":ne:x",
			"/api/stats/summary?f=" + f.Name + ":in:x",
			"/api/stats/breakdown?dim=" + f.Name,
			"/api/log-values?table=events&field=" + f.Name,
			"/api/log-values?table=conversions&field=" + f.Name,
			"/api/logs/export?f=" + f.Name + ":eq:x",
		}
		for _, p := range paths {
			code, body := rig.get(t, rig.user, p)
			if code != http.StatusBadRequest || !strings.Contains(body, "hidden field") {
				t.Errorf("hide %s: GET %s = %d %s", f.Name, p, code, body)
			}
		}
	}
	rig.hide(t, "country")
	if code, body := rig.get(t, rig.user, "/api/logs?f=city:eq:x"); code != http.StatusOK {
		t.Errorf("filter by a visible field: %d %s", code, body)
	}
}

func TestHiddenFieldRejectedInLegacyParams(t *testing.T) {
	rig := newRolesRig(t, "")
	cases := []struct {
		path   string
		params []paramField
	}{{"/api/logs", legacyLogParams}, {"/api/logs/export", legacyLogParams}, {"/api/postbacks", legacyPostbackParams}, {"/api/postbacks/export", legacyPostbackParams}}
	for _, c := range cases {
		for _, p := range c.params {
			rig.hide(t, p.field)
			code, body := rig.get(t, rig.user, c.path+"?"+p.param+"=1")
			if code != http.StatusBadRequest || !strings.Contains(body, "hidden field "+p.field) {
				t.Errorf("GET %s?%s=1 with %s hidden: %d %s", c.path, p.param, p.field, code, body)
			}
			rig.hide(t)
			if code, body := rig.get(t, rig.user, c.path+"?"+p.param+"=1"); code != http.StatusOK {
				t.Errorf("GET %s?%s=1 nothing hidden: %d %s", c.path, p.param, code, body)
			}
		}
	}
}

func TestHiddenFieldOutOfListsAndKeys(t *testing.T) {
	rig := newRolesRig(t, "")
	rig.hide(t, "country", "campaign", "keyword")

	_, body := rig.get(t, rig.user, "/api/log-fields")
	var info store.FieldsInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatal(err)
	}
	hidden := expandHidden([]string{"country", "campaign", "keyword"})
	for _, f := range info.Fields {
		if hidden[f.Name] {
			t.Errorf("hidden field %s listed", f.Name)
		}
	}
	hiddenN := 0
	for _, f := range store.StaticFields() {
		if hidden[f.Name] {
			hiddenN++
		}
	}
	if len(info.Fields) != len(store.StaticFields())-hiddenN {
		t.Errorf("fields %d of %d, hidden %d", len(info.Fields), len(store.StaticFields()), hiddenN)
	}
	_, body = rig.get(t, rig.admin, "/api/log-fields")
	if err := json.Unmarshal([]byte(body), &info); err != nil || len(info.Fields) != len(store.StaticFields()) {
		t.Errorf("admin must see every field: %d err %v", len(info.Fields), err)
	}

	_, body = rig.get(t, rig.user, "/api/logs/filters")
	var dims map[string][]string
	if err := json.Unmarshal([]byte(body), &dims); err != nil {
		t.Fatal(err)
	}
	if _, ok := dims["country"]; ok {
		t.Error("hidden dimension country in filters")
	}
	if _, ok := dims["group"]; ok {
		t.Error("hidden dimension campaign in filters")
	}
	if _, ok := dims["device"]; !ok {
		t.Error("visible dimension device missing from filters")
	}
	if code, _ := rig.get(t, rig.user, "/api/keys?group=c1"); code != http.StatusForbidden {
		t.Errorf("keys with keyword hidden: %d", code)
	}
	if code, _ := rig.get(t, rig.admin, "/api/keys?group=c1"); code != http.StatusOK {
		t.Errorf("admin keys: %d", code)
	}
	if code, _ := rig.get(t, rig.user, "/api/keys/download?group=c1&date=2026-06-07"); code != http.StatusForbidden {
		t.Errorf("keys download with keyword hidden: %d", code)
	}
}

func TestHiddenFieldAbsentFromCSV(t *testing.T) {
	rig := newRolesRig(t, "")
	read := func(c *http.Client) [][]string {
		code, body := rig.get(t, c, "/api/logs/export")
		if code != http.StatusOK {
			t.Fatalf("export %d %s", code, body)
		}
		recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
		if err != nil || len(recs) != 2 {
			t.Fatalf("csv %q err %v", body, err)
		}
		return recs
	}
	full := read(rig.admin)
	for i, col := range full[0] {
		field := col
		if col == "group" {
			field = "campaign"
		}
		rig.hide(t, field)
		got := read(rig.user)
		// A field takes its derived columns with it, see derivedFields.
		dropped := append([]string{col}, derivedFields[field]...)
		left := 0
		for _, h := range full[0] {
			if !slices.Contains(dropped, h) {
				left++
			}
		}
		if len(got[0]) != left || len(got[1]) != left {
			t.Errorf("hide %s: %d header and %d cells, want %d", field, len(got[0]), len(got[1]), left)
			continue
		}
		for _, h := range got[0] {
			if slices.Contains(dropped, h) {
				t.Errorf("hide %s: header still has %s", field, h)
			}
		}
		if cell := full[1][i]; strings.HasPrefix(cell, "S_") {
			for _, c := range got[1] {
				if c == cell {
					t.Errorf("hide %s: value %s still in the row", field, cell)
				}
			}
		}
	}
	rig.hide(t, "profit", "cost")
	if got := read(rig.user); len(got[0]) != len(full[0]) {
		t.Errorf("money groups are not in the export: %d columns, want %d", len(got[0]), len(full[0]))
	}
}

func TestExpandHiddenMoneyGroups(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, []string{}},
		{[]string{"country"}, []string{"country"}},
		{[]string{"group"}, []string{"campaign"}},
		{[]string{"cost"}, []string{"cost", "cost_cents", "cost_cur"}},
		{[]string{"cost_cents"}, []string{"cost", "cost_cents", "cost_cur"}},
		{[]string{"profit"}, []string{"depsum", "profit"}},
	}
	for _, c := range cases {
		got := hiddenList(viewer{hidden: expandHidden(c.in)})
		want := append([]string{}, c.want...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("expandHidden(%v) = %v, want %v", c.in, got, want)
		}
	}
	if got := hiddenList(viewer{admin: true, hidden: map[string]bool{"x": true}}); len(got) != 0 || got == nil {
		t.Errorf("admin hidden list %#v", got)
	}
}

func TestFieldHiddenAliasAndAdmin(t *testing.T) {
	v := viewer{hidden: expandHidden([]string{"campaign", "cost"})}
	for _, name := range []string{"campaign", "group", "cost", "cost_cents", "cost_cur"} {
		if !fieldHidden(v, name) {
			t.Errorf("%s must be hidden", name)
		}
	}
	for _, name := range []string{"country", "profit", ""} {
		if fieldHidden(v, name) {
			t.Errorf("%s must be visible", name)
		}
	}
	if fieldHidden(viewer{admin: true, hidden: v.hidden}, "campaign") {
		t.Error("administrator must see every field")
	}
}

func TestHiddenIDsAreStoreFields(t *testing.T) {
	for _, f := range store.StaticFields() {
		if hiddenIDOK(f.Name) == unhideableFields[f.Name] {
			t.Errorf("field %s: hideable=%v, unhideable=%v", f.Name, hiddenIDOK(f.Name), unhideableFields[f.Name])
		}
	}
	for _, id := range []string{"cost", "profit"} {
		if !hiddenIDOK(id) {
			t.Errorf("%s cannot be hidden", id)
		}
	}
	for _, id := range []string{"", "nope", "group", "Country"} {
		if hiddenIDOK(id) {
			t.Errorf("%q must not be accepted", id)
		}
	}
	if _, err := normalizeAccess(security.Access{Hidden: []string{"country", "sub_id_4", "cost"}}); err != nil {
		t.Errorf("valid hidden ids: %v", err)
	}
}

// TestEveryResponseKeyIsClassified fails when a statistics response gets a key that is
// neither a journal field nor a known counter, so a new column cannot skip the hiding rules.
func TestEveryResponseKeyIsClassified(t *testing.T) {
	known := map[string]bool{}
	for _, f := range store.StaticFields() {
		for _, k := range keysGone(f.Name) {
			known[k] = true
		}
	}
	// Keys that are not a journal field: counters, wrappers and row markers.
	// The checker status answer is switches and a masked postback, not a click column.
	plain := map[string]bool{
		"hits": true, "conv": true, "n": true, "forwarded": true, "total": true, "count": true,
		"rows": true, "clicks": true, "ready": true, "subs": true, "key": true, "content": true,
		"truncated": true, "conv_ready": true, "days": true, "series": true,
		"lines": true, "size": true, "max_lines": true,
		"enabled": true, "create_apps": true, "postback": true,
		"url_masked": true, "available": true, "method": true,
	}
	for ep, keys := range adminJSONRoutes(t, newRolesRig(t, "")) {
		for k := range keys {
			if !known[k] && !plain[k] {
				t.Errorf("%s: key %q is neither a journal field nor a known counter; map it in fieldExtraKeys", ep, k)
			}
		}
	}
}
