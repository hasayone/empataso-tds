package admin

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// macroSkipFields are struct fields that hold labels or notes, not templates; their
// text is never expanded, so a macro written there inserts nothing. PostbackURL is the
// note of a network for the partner cabinet: the engine does not read it.
var macroSkipFields = map[string]bool{
	"ID": true, "Name": true, "Comment": true, "Folder": true, "Group": true, "PostbackURL": true, "BoardID": true,
}

// curlyMaxLen is the longest name render reads between curly braces.
const curlyMaxLen = 24

// textAt is one string of a record and the place it sits in. The place is the path of
// field names from the record root; a list item is named by its id or name when those
// are unique in the list, otherwise by its position.
type textAt struct{ place, text string }

// textsOf collects every string held by v with its place: strings, struct fields, list
// items and map values, at any depth. Fields in macroSkipFields are left out.
// v is not modified.
func textsOf(v any) []textAt {
	var out []textAt
	collectTexts(reflect.ValueOf(v), "", &out)
	return out
}

// collectTexts appends the strings inside rv to out. place is the path of rv.
func collectTexts(rv reflect.Value, place string, out *[]textAt) {
	switch rv.Kind() {
	case reflect.String:
		*out = append(*out, textAt{place, rv.String()})
	case reflect.Pointer, reflect.Interface:
		if !rv.IsNil() {
			collectTexts(rv.Elem(), place, out)
		}
	case reflect.Slice, reflect.Array:
		keys := itemKeys(rv)
		for i := 0; i < rv.Len(); i++ {
			collectTexts(rv.Index(i), place+"["+keys[i]+"]", out)
		}
	case reflect.Map:
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, k := range keys {
			collectTexts(rv.MapIndex(k), place+"{"+fmt.Sprint(k)+"}", out)
		}
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			if f := t.Field(i); f.IsExported() && !macroSkipFields[f.Name] {
				collectTexts(rv.Field(i), place+"."+f.Name, out)
			}
		}
	}
}

// itemKeys names the items of a list. When every item has a non-empty ID and the IDs
// differ, the key is "id:"+ID; otherwise the same is tried with Name. Without such an
// identity the key is the position, "#i".
func itemKeys(rv reflect.Value) []string {
	for _, field := range []string{"ID", "Name"} {
		if keys, ok := identityKeys(rv, field); ok {
			return keys
		}
	}
	keys := make([]string, rv.Len())
	for i := range keys {
		keys[i] = "#" + strconv.Itoa(i)
	}
	return keys
}

// identityKeys returns "<field>:<value>" for each item of rv when every item is a
// struct whose string field is non-empty and unique in the list.
func identityKeys(rv reflect.Value, field string) ([]string, bool) {
	keys := make([]string, rv.Len())
	seen := make(map[string]bool, rv.Len())
	for i := range keys {
		item := rv.Index(i)
		for item.Kind() == reflect.Pointer || item.Kind() == reflect.Interface {
			if item.IsNil() {
				return nil, false
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.Struct {
			return nil, false
		}
		f := item.FieldByName(field)
		if !f.IsValid() || f.Kind() != reflect.String || f.String() == "" || seen[f.String()] {
			return nil, false
		}
		seen[f.String()] = true
		keys[i] = field + ":" + f.String()
	}
	return keys, true
}

// hiddenMacroTokens returns the macros of macroFields whose field is hidden for v,
// sorted. An administrator or a viewer with nothing hidden gets none.
func hiddenMacroTokens(v viewer) []string {
	if v.admin || len(v.hidden) == 0 {
		return nil
	}
	var out []string
	for tok, field := range macroFields {
		if fieldHidden(v, field) {
			out = append(out, tok)
		}
	}
	sort.Strings(out)
	return out
}

// queryReadFields are the fields whose value the engine reads from a query parameter
// the panel records name: the keyword, the Sub IDs and the extra click fields
// (config.ExtraSourceParams) by a traffic source, the click price by a campaign.
// A curly macro {name} reads any query parameter, so it can carry them.
func queryReadFields() []string {
	out := []string{"keyword", "cost"}
	for n := 1; n <= config.MaxRuleSubID; n++ {
		out = append(out, config.SubIDRuleKey(n))
	}
	return append(out, config.ExtraSourceParams...)
}

// queryReadHidden reports whether v has the keyword, a Sub ID, an extra click field
// or the click price hidden.
func queryReadHidden(v viewer) bool {
	if v.admin || len(v.hidden) == 0 {
		return false
	}
	for _, f := range queryReadFields() {
		if fieldHidden(v, f) {
			return true
		}
	}
	return false
}

// macroText returns the macro as written in docs: bracket macros in capitals.
func macroText(tok string) string {
	if strings.HasPrefix(tok, "[") {
		return strings.ToUpper(tok)
	}
	return tok
}

// macroGuard finds in a text the macros a viewer may not write.
type macroGuard struct {
	tokens []string
	curly  bool
}

// newMacroGuard returns the guard for v, or nil when v may write any macro.
// An administrator and a viewer with nothing hidden get nil.
func newMacroGuard(v viewer) *macroGuard {
	g := &macroGuard{tokens: hiddenMacroTokens(v), curly: queryReadHidden(v)}
	if len(g.tokens) == 0 && !g.curly {
		return nil
	}
	return g
}

// find returns the client message for the first forbidden macro in text, or "".
// A macro of a hidden field is forbidden in any case. When the viewer has the keyword, a
// Sub ID, an extra click field or the price hidden, a curly macro {name} that is not
// exactly one of the macros of macroFields or macroSafe is forbidden too: it reads a
// click parameter whose name is set by a record the viewer cannot see. That includes
// the macros of macroQueryRead ({sub_id_13}..{sub_id_15}) and {site}, {ad_campaign_id},
// {creative_id} and {external_id}.
func (g *macroGuard) find(text string) string {
	lower := strings.ToLower(text)
	for _, tok := range g.tokens {
		if strings.Contains(lower, tok) {
			return "macro " + macroText(tok) + " uses hidden field " + macroFields[tok]
		}
	}
	if !g.curly {
		return ""
	}
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		end := strings.IndexByte(text[i:], '}')
		if end <= 1 || end > curlyMaxLen {
			continue
		}
		name := text[i+1 : i+end]
		if curlyName(name) && !curlyClassified(name) {
			return "macro {" + name + "} may read a hidden field"
		}
	}
	return ""
}

// curlyName reports whether name is read by render as a click-query macro: 1 to
// curlyMaxLen ASCII letters, digits, '_' or '-'.
func curlyName(name string) bool {
	if name == "" || len(name) > curlyMaxLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// curlyClassified reports whether {name}, with exactly this spelling, is in macroFields
// or macroSafe: a macro that reads no click query parameter. The tables are lowercase
// and render matches names case-sensitively, so {Keyword} is a query parameter and is
// not classified; neither are the macros of macroQueryRead.
func curlyClassified(name string) bool {
	tok := "{" + name + "}"
	if _, ok := macroFields[tok]; ok {
		return true
	}
	_, ok := macroSafe[tok]
	return ok
}

// hiddenMacroUse reports the first forbidden macro of candidate that the stored record
// base does not already carry in the same place. candidate and base are values holding
// templates (a campaign, an offer, a list of postbacks); base is nil for a new record.
//
// A string with a forbidden macro passes only when base holds the identical string at
// the same place: the same field of the same stream or postback. Any change of such a
// string, and a move to another field, stream or postback, is refused, so a macro an
// administrator wrote cannot be reused with another host. Strings without a forbidden
// macro are not compared. The result is the client message, or "" when allowed.
func hiddenMacroUse(v viewer, candidate, base any) string {
	g := newMacroGuard(v)
	if g == nil {
		return ""
	}
	var stored map[textAt]int
	if base != nil {
		stored = make(map[textAt]int)
		for _, t := range textsOf(base) {
			stored[t]++
		}
	}
	for _, t := range textsOf(candidate) {
		msg := g.find(t.text)
		if msg == "" {
			continue
		}
		if stored[t] == 0 {
			return msg
		}
		stored[t]--
	}
	return ""
}

// validateGroupMacros refuses a campaign that gained a forbidden macro or changed the
// string that holds one, or changed the output of a stream whose Curl or Remote.URL
// holds one. stored is the file before the save and merged the candidate list. Campaigns that did not change are skipped. It returns the client message, or ""
// when allowed.
func validateGroupMacros(v viewer, stored, merged []config.Group) string {
	guard := newMacroGuard(v)
	if guard == nil {
		return ""
	}
	storedByID := make(map[string]config.Group, len(stored))
	for _, g := range stored {
		storedByID[g.ID] = g
	}
	for _, g := range merged {
		old, had := storedByID[g.ID]
		if had && groupsEqual(old, g) {
			continue
		}
		var base any
		if had {
			base = old
			if msg := frozenStreamOut(guard, old, g); msg != "" {
				return msg
			}
		}
		if msg := hiddenMacroUse(v, g, base); msg != "" {
			return msg
		}
	}
	return ""
}
