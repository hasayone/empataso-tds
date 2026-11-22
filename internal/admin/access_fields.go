package admin

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
)

// moneyFields maps a money group to the journal fields it covers. Hiding the
// group id or any of its fields hides all of them.
var moneyFields = map[string][]string{
	"profit": {"profit"},
	"cost":   {"cost_cents", "cost_cur"},
}

// fieldExtraKeys lists the JSON keys that carry a journal field under a name
// other than the field name: aggregates, id columns and renamed columns.
// A field absent here is carried by the key equal to its name.
var fieldExtraKeys = map[string][]string{
	"campaign":    {"group"},
	"campaign_id": {"group_id"},
	"uniq":        {"unique", "uniq_first"},
	"bot":         {"bots"},
	"ts":          {"day"},
	"offer_id":    {"offer"},
	"source_id":   {"source"},
	"profit":      {"revenue", "payout", "depsum", "dep2sum"},
	"cost_cents":  {"cost", "spend"},
	"cost_cur":    {"currency"},
	"status":      {"original_status", "prev_status", "leads", "sales", "rejected"},
}

// derivedFields maps a journal field to the fields whose value is a refinement or a
// copy of it: a status before a change, the version of a system or browser, the model
// of a brand or device. Hiding the key hides every listed field too, because the
// derived value would give the hidden one away.
var derivedFields = map[string][]string{
	"status":             {"original_status", "prev_status"},
	"os":                 {"os_version"},
	"browser":            {"browser_ver"},
	"brand":              {"device_model"},
	"device":             {"device_model"},
	"profit":             {"depsum", "dep2sum"},
	"keyword":            {"origin_keyword"},
	"stream":             {"origin_stream"},
	"cid":                {"root_cid"},
	"origin_campaign":    {"root_cid", "origin_stream", "origin_keyword"},
	"origin_campaign_id": {"root_cid", "origin_stream", "origin_keyword"},
}

// macroFields maps every macro of internal/render that inserts a click value to the
// journal field it exposes. Keys are lowercase: the lookup ignores case, so a macro
// written in any case is caught. A macro of a hidden field must not be written into
// a link by a user who may not see that field.
var macroFields = buildMacroFields()

// macroSafe lists render macros that insert no journal value, with the reason.
// Prefixes end in '-' or '('. A macro of render that is in neither table fails
// TestEveryRenderMacroIsClassified.
var macroSafe = map[string]string{
	"[randnum-":  "random number",
	"[randstr-":  "random string",
	"[randline-": "line of a data file chosen by the template",
	"[randdfl-":  "line of a data file chosen by the template",
	"[remote]":   "body fetched from the stream's own address",
	"{offer}":    "offer-step path, not a click value",
}

// macroQueryRead lists render macros that read a click query parameter and have no
// journal field: {sub_id_13}..{sub_id_15}. A traffic source names the parameter, so
// the macro can carry the value of any hidden field read from the query. It is
// treated like a curly macro {name} of an unknown name: refused as a new use when the
// viewer has a keyword, a Sub ID, an extra click field or the price hidden. It
// counts as classified for TestEveryRenderMacroIsClassified.
var macroQueryRead = buildMacroQueryRead()

// buildMacroQueryRead builds macroQueryRead from the Sub ID slots the engine reads
// beyond the stored ones: MaxRuleSubID+1 through MaxSubID.
func buildMacroQueryRead() map[string]string {
	out := make(map[string]string, config.MaxSubID-config.MaxRuleSubID)
	for n := config.MaxRuleSubID + 1; n <= config.MaxSubID; n++ {
		out["{"+config.SubIDRuleKey(n)+"}"] = "query parameter named by the traffic source, not stored"
	}
	return out
}

// buildMacroFields builds macroFields: the fixed macros, [PAR-1]..[PAR-5] and
// {sub_id_1}..{sub_id_12}.
func buildMacroFields() map[string]string {
	out := map[string]string{
		"[key]": "keyword", "{keyword}": "keyword",
		"[path]": "domain", "[domain]": "domain",
		"[ip]":      "ip",
		"[country]": "country", "[()country()]": "country", "{country}": "country",
		"[city]": "city", "[()city()]": "city",
		"[region]": "region", "[asn]": "asn", "[org]": "org",
		"[timezone]": "timezone", "[utc]": "timezone",
		"[lang]": "lang", "[device]": "device", "[operator]": "operator",
		"[useragent]": "useragent",
		"[cid]":       "cid", "[subid]": "cid", "{subid}": "cid",
		"{status}": "status", "{payout}": "profit",
		"{origin}":     "origin_campaign_id",
		"{root_subid}": "root_cid",
	}
	for n := 1; n <= config.MaxRuleSubID; n++ {
		field := config.SubIDRuleKey(n)
		out["{"+field+"}"] = field
		if n <= 5 {
			out["[par-"+strconv.Itoa(n)+"]"] = field
		}
	}
	return out
}

// fieldAlias maps an old API name onto the journal field name.
var fieldAlias = map[string]string{"group": "campaign"}

// hiddenIDs is every id a role or a user may hide: the journal field names
// and the money group "cost".
var hiddenIDs = buildHiddenIDs()

// unhideableFields are journal fields no role or user may hide: every statistics
// view is built on them, so hiding them would break the tables, not protect data.
var unhideableFields = map[string]bool{"ts": true}

// buildHiddenIDs collects the ids accepted in access.hidden.
// The names come from the store field list, the one source of journal columns;
// unhideableFields are left out.
func buildHiddenIDs() map[string]bool {
	out := map[string]bool{"cost": true}
	for _, f := range store.StaticFields() {
		if !unhideableFields[f.Name] {
			out[f.Name] = true
		}
	}
	return out
}

// fieldView is one entry of GET /api/log-fields: the store field plus whether
// a role or a user may hide it.
type fieldView struct {
	store.FieldInfo
	Hideable bool `json:"hideable"`
}

// fieldsResponse is the body of GET /api/log-fields.
type fieldsResponse struct {
	Fields []fieldView                `json:"fields"`
	Ready  map[string]map[string]bool `json:"ready"`
}

// fieldsResponseOf marks the hideable fields of info for the client.
func fieldsResponseOf(info store.FieldsInfo) fieldsResponse {
	out := fieldsResponse{Fields: make([]fieldView, len(info.Fields)), Ready: info.Ready}
	for i, f := range info.Fields {
		out.Fields[i] = fieldView{FieldInfo: f, Hideable: hiddenIDOK(f.Name)}
	}
	return out
}

// hiddenIDOK reports whether name may appear in access.hidden.
func hiddenIDOK(name string) bool {
	return hiddenIDs[name]
}

// canonicalField resolves an old API name to the journal field name.
func canonicalField(name string) string {
	if a, ok := fieldAlias[name]; ok {
		return a
	}
	return name
}

// expandHidden turns the stored hidden ids into the set the server enforces.
// ids are field names or "cost". Names are canonical. When an id of a money
// group or one of its fields is present, the whole group is added; a field with
// entries in derivedFields adds those fields too.
func expandHidden(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[canonicalField(id)] = true
	}
	for parent, children := range derivedFields {
		if !out[parent] {
			continue
		}
		for _, c := range children {
			out[c] = true
		}
	}
	for group, members := range moneyFields {
		hit := out[group]
		for _, m := range members {
			hit = hit || out[m]
		}
		if !hit {
			continue
		}
		out[group] = true
		for _, m := range members {
			out[m] = true
		}
	}
	return out
}

// fieldHidden reports whether the journal field name is hidden for v.
// name is a field name, the old name group or the money group "cost".
// Administrators see every field.
func fieldHidden(v viewer, name string) bool {
	if v.admin {
		return false
	}
	return v.hidden[canonicalField(name)]
}

// hides reports whether the money group or field id is hidden for v.
func (v viewer) hides(id string) bool {
	return fieldHidden(v, id)
}

// hiddenList returns the hidden set of v, sorted, never nil.
// It lists fields and money group ids, so a client can derive computed columns.
func hiddenList(v viewer) []string {
	out := make([]string, 0, len(v.hidden))
	if v.admin {
		return out
	}
	for id := range v.hidden {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// fieldJSONKeys returns the JSON keys that carry the hidden id name.
// For a money group it is the keys of every member field.
func fieldJSONKeys(name string) []string {
	keys := append([]string{name}, fieldExtraKeys[name]...)
	for _, m := range moneyFields[name] {
		if m != name {
			keys = append(keys, fieldJSONKeys(m)...)
		}
	}
	return keys
}

// hiddenSubIndexes returns the zero-based positions of Sub IDs hidden for v.
func hiddenSubIndexes(v viewer) []int {
	var out []int
	for n := 1; n <= config.MaxRuleSubID; n++ {
		if fieldHidden(v, config.SubIDRuleKey(n)) {
			out = append(out, n-1)
		}
	}
	return out
}

// blankSubs clears the hidden positions of every subs array inside v.
// v is a decoded JSON tree, idx the positions from hiddenSubIndexes. When all
// positions are hidden the subs key is dropped. It returns the changed tree.
func blankSubs(v any, idx []int) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if arr, ok := val.([]any); ok && k == "subs" {
				if len(idx) >= config.MaxRuleSubID {
					delete(x, k)
					continue
				}
				for _, i := range idx {
					if i < len(arr) {
						arr[i] = ""
					}
				}
				continue
			}
			x[k] = blankSubs(val, idx)
		}
		return x
	case []any:
		for i, val := range x {
			x[i] = blankSubs(val, idx)
		}
		return x
	default:
		return v
	}
}

// paramField binds a legacy query parameter to the journal field it filters.
type paramField struct{ param, field string }

// legacyLogParams are the old click-journal parameters outside f=.
var legacyLogParams = []paramField{
	{"group", "campaign"}, {"stream", "stream"}, {"country", "country"}, {"device", "device"},
	{"os", "os"}, {"browser", "browser"}, {"brand", "brand"}, {"ip", "ip"},
	{"bots", "bot"}, {"humans", "bot"},
}

// legacyPostbackParams are the old conversion-journal parameters outside f=.
var legacyPostbackParams = []paramField{
	{"group", "campaign"}, {"status", "status"}, {"stream", "stream"}, {"offer", "offer_id"},
	{"landing", "landing_id"}, {"source", "source_id"}, {"network", "network_id"}, {"cid", "cid"},
}

// rejectHiddenParams refuses a request that filters by a hidden field through an old parameter.
// params is the parameter list of the endpoint. A non-empty parameter whose field is
// hidden answers 400 "bad filter: hidden field <field>". The bool is false after the answer.
func (s *Server) rejectHiddenParams(w http.ResponseWriter, r *http.Request, q url.Values, params []paramField) bool {
	v := s.viewerFrom(r.Context())
	for _, p := range params {
		if q.Get(p.param) != "" && fieldHidden(v, p.field) {
			writeErr(w, http.StatusBadRequest, "bad filter: hidden field "+p.field)
			return false
		}
	}
	return true
}

// visibleFields returns info without the fields hidden for v.
func visibleFields(v viewer, info store.FieldsInfo) store.FieldsInfo {
	if v.admin || len(v.hidden) == 0 {
		return info
	}
	kept := make([]store.FieldInfo, 0, len(info.Fields))
	for _, f := range info.Fields {
		if !fieldHidden(v, f.Name) {
			kept = append(kept, f)
		}
	}
	info.Fields = kept
	return info
}

// visibleColumns returns the positions in header whose journal field is not hidden for v.
// header holds CSV column names; the column group is the field campaign.
func visibleColumns(v viewer, header []string) []int {
	out := make([]int, 0, len(header))
	for i, col := range header {
		if !fieldHidden(v, col) {
			out = append(out, i)
		}
	}
	return out
}

// pickColumns returns the cells of rec at the positions in idx.
func pickColumns(rec []string, idx []int) []string {
	out := make([]string, len(idx))
	for i, p := range idx {
		out[i] = rec[p]
	}
	return out
}
