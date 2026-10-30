package admin

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
)

// leafName returns the field name a leaf path ends in, without list or map marks.
func leafName(path string) string {
	path = strings.TrimSuffix(strings.TrimSuffix(path, "[]"), "{}")
	return path[strings.LastIndex(path, ".")+1:]
}

// leafPaths lists the paths of every string value reachable from type t, in field
// order. A list item is "[]", a map value "{}".
func leafPaths(t reflect.Type, prefix string) []string {
	switch t.Kind() {
	case reflect.String:
		return []string{prefix}
	case reflect.Pointer:
		return leafPaths(t.Elem(), prefix)
	case reflect.Slice:
		return leafPaths(t.Elem(), prefix+"[]")
	case reflect.Map:
		return leafPaths(t.Elem(), prefix+"{}")
	case reflect.Struct:
		var out []string
		for i := 0; i < t.NumField(); i++ {
			if f := t.Field(i); f.IsExported() {
				out = append(out, leafPaths(f.Type, prefix+"."+f.Name)...)
			}
		}
		return out
	}
	return nil
}

// valueWithLeaf builds a value of type t whose only non-zero string is the leaf at
// target, set to text. found is false when target is not inside t.
func valueWithLeaf(t reflect.Type, prefix, target, text string) (rv reflect.Value, found bool) {
	rv = reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.String:
		if prefix == target {
			rv.SetString(text)
			return rv, true
		}
	case reflect.Pointer:
		if inner, ok := valueWithLeaf(t.Elem(), prefix, target, text); ok {
			rv.Set(reflect.New(t.Elem()))
			rv.Elem().Set(inner)
			return rv, true
		}
	case reflect.Slice:
		if inner, ok := valueWithLeaf(t.Elem(), prefix+"[]", target, text); ok {
			rv.Set(reflect.MakeSlice(t, 1, 1))
			rv.Index(0).Set(inner)
			return rv, true
		}
	case reflect.Map:
		if inner, ok := valueWithLeaf(t.Elem(), prefix+"{}", target, text); ok {
			rv.Set(reflect.MakeMap(t))
			rv.SetMapIndex(reflect.ValueOf("k").Convert(t.Key()), inner)
			return rv, true
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if inner, ok := valueWithLeaf(f.Type, prefix+"."+f.Name, target, text); ok {
				rv.Field(i).Set(inner)
				return rv, true
			}
		}
	}
	return rv, false
}

// macroSurface says, for each config type a non-administrator can write, which string
// fields the macro guard leaves out and why the engine never expands them.
// Every other string of these types is a template: the guard checks it.
var macroSurface = map[string]struct {
	typ   reflect.Type
	inert map[string]string
}{
	"Group": {reflect.TypeOf(config.Group{}), map[string]string{
		"Group.ID":                          "campaign id",
		"Group.Name":                        "campaign label",
		"Group.Folder":                      "list grouping label",
		"Group.BoardID":                     "panel section id; never expanded",
		"Group.Streams[].Name":              "stream label; also the identity that places a link",
		"Group.Streams[].Comment":           "operator note",
		"Group.Postbacks[].ID":              "template id",
		"Group.Postbacks[].Name":            "template label",
		"Group.Postbacks[].BoardID":         "panel section id; never expanded",
		"Group.Streams[].Out.Offers[].ID":   "offer id; the address is the offer's own",
		"Group.Streams[].Out.Landings[].ID": "landing id; the page is the landing's own",
	}},
	"Offer": {reflect.TypeOf(config.Offer{}), map[string]string{
		"Offer.ID":      "offer id",
		"Offer.Name":    "offer label",
		"Offer.Group":   "catalog label",
		"Offer.BoardID": "panel section id; never expanded",
	}},
	"Landing": {reflect.TypeOf(config.Landing{}), map[string]string{
		"Landing.ID":      "landing id",
		"Landing.Name":    "landing label",
		"Landing.Group":   "catalog label",
		"Landing.BoardID": "panel section id; never expanded",
	}},
	"AffiliateNetwork": {reflect.TypeOf(config.AffiliateNetwork{}), map[string]string{
		"AffiliateNetwork.ID":          "network id",
		"AffiliateNetwork.Name":        "network label",
		"AffiliateNetwork.Group":       "catalog label",
		"AffiliateNetwork.PostbackURL": "note for the partner cabinet; the engine does not read it",
		"AffiliateNetwork.BoardID":     "panel section id; never expanded",
	}},
	"OutboundPostback": {reflect.TypeOf(config.OutboundPostback{}), map[string]string{
		"OutboundPostback.ID":       "template id",
		"OutboundPostback.Name":     "template label",
		"OutboundPostback.BoardID":  "panel section id; never expanded",
	}},
}

// macroInert are the types a non-administrator can write whose strings are never
// expanded as templates: hosts, ids and query parameter names. Each lists every string
// of the type with the reason; a new field must be added here or to macroSurface.
var macroInert = map[string]struct {
	typ    reflect.Type
	reason string
	leaves []string
}{
	"Domain": {reflect.TypeOf(config.Domain{}), "a host name, a role, check results: never expanded", []string{
		"Domain.ID", "Domain.Host", "Domain.Group", "Domain.Role", "Domain.Reason", "Domain.Checked", "Domain.CreatedBy",
		"Domain.Registrar", "Domain.RegistrarAccount", "Domain.RegistrarStatus", "Domain.RegistrarPendingSince",
		"Domain.ReasonDetail", "Domain.CfZoneID", "Domain.CfStatus", "Domain.CfNS[]", "Domain.ConnectedAt", "Domain.WatchSince",
		"Domain.AdminReason", "Domain.AdminDNS",
	}},
	"TrafficSource": {reflect.TypeOf(config.TrafficSource{}), "ids, labels and query parameter names: the engine reads the names as keys, never as templates; rebinding is guarded by sourceCheck", []string{
		"TrafficSource.ID", "TrafficSource.Name", "TrafficSource.Parameters{}", "TrafficSource.Group", "TrafficSource.CreatedBy", "TrafficSource.BoardID",
	}},
	"Board": {reflect.TypeOf(config.Board{}), "section id, name, icon and members: never expanded", []string{
		"Board.ID", "Board.Name", "Board.Icon", "Board.IconColor", "Board.Members[].Role", "Board.Members[].UserID",
	}},
}

func TestEveryTemplateFieldIsGuarded(t *testing.T) {
	v := viewer{hidden: expandHidden([]string{"ip"})}
	for name, s := range macroSurface {
		for _, path := range leafPaths(s.typ, name) {
			val, ok := valueWithLeaf(s.typ, name, path, "https://x.example/?q=[IP]")
			if !ok {
				t.Fatalf("cannot build %s", path)
			}
			refused := hiddenMacroUse(v, val.Interface(), nil) != ""
			_, listed := s.inert[path]
			switch {
			case listed && refused:
				t.Errorf("%s is listed as never expanded but the guard refuses it", path)
			case !listed && !refused:
				t.Errorf("%s is a string of %s that the guard does not check; list it in macroSurface with the reason or guard it", path, name)
			}
		}
		for path := range s.inert {
			if !strings.HasPrefix(path, name+".") || !containsPath(leafPaths(s.typ, name), path) {
				t.Errorf("macroSurface lists %s, which is not a string of %s", path, name)
			}
		}
	}
}

func TestInertTypesListEveryString(t *testing.T) {
	for name, s := range macroInert {
		got := leafPaths(s.typ, name)
		want := append([]string(nil), s.leaves...)
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: strings of the type are %v, macroInert lists %v", name, got, want)
		}
	}
}

func containsPath(paths []string, p string) bool {
	for _, x := range paths {
		if x == p {
			return true
		}
	}
	return false
}

func TestEveryStoredTypeIsInTheMacroInventory(t *testing.T) {
	known := map[reflect.Type]bool{}
	for _, s := range macroSurface {
		known[s.typ] = true
	}
	for _, s := range macroInert {
		known[s.typ] = true
	}
	// A file listing of landing directories; the pages themselves are guarded by
	// landingFileMacro and archiveMacroUse, listed in writeRouteGuard.
	known[reflect.TypeOf(FileInfo{})] = true
	cfg := reflect.TypeOf(Config{})
	for i := 0; i < cfg.NumField(); i++ {
		f := cfg.Field(i)
		list, ok := f.Type.MethodByName("List")
		if !ok || list.Type.NumOut() != 2 || list.Type.Out(0).Kind() != reflect.Slice {
			continue
		}
		elem := list.Type.Out(0).Elem()
		if elem.Kind() == reflect.Struct && !known[elem] {
			t.Errorf("Config.%s stores %s, which is in neither macroSurface nor macroInert", f.Name, elem)
		}
	}
}

// writeRouteGuard says how each route a non-administrator can use to change data keeps
// a hidden macro out. A new route fails TestEveryWriteRouteIsInventoried until listed.
var writeRouteGuard = map[string]string{
	"POST /api/logout":                          "session only, stores nothing",
	"POST /api/password":                        "own password",
	"PUT /api/me/views":                         "own sort and filter state, never expanded",
	"PUT /api/groups":                           "validateGroupMacros, validateGroupParams",
	"PUT /api/groups/{id}":                      "validateGroupMacros, validateGroupParams",
	"DELETE /api/groups/{id}":                   "removes a campaign",
	"PUT /api/campaigns/{id}/postbacks":         "hiddenMacroUse on the template list",
	"POST /api/spend":                           "amounts, no templates",
	"DELETE /api/spend/{id}":                    "removes a row",
	"POST /api/offers":                          "offerCheck",
	"PUT /api/offers/{id}":                      "offerCheck",
	"DELETE /api/offers/{id}":                   "removes a record",
	"POST /api/landings":                        "landingCheck: macroCheck and the entry rule",
	"PUT /api/landings/{id}":                    "landingCheck: macroCheck and the entry rule",
	"DELETE /api/landings/{id}":                 "removes a record",
	"POST /api/affiliate-networks":              "macroCheck on offer_query",
	"PUT /api/affiliate-networks/{id}":          "macroCheck on offer_query",
	"DELETE /api/affiliate-networks/{id}":       "removes a record",
	"POST /api/traffic-sources":                 "sourceCheck",
	"PUT /api/traffic-sources/{id}":             "sourceCheck",
	"DELETE /api/traffic-sources/{id}":          "removes a record",
	"POST /api/domains":                         "host names, never expanded",
	"PUT /api/domains/{id}":                     "host names, never expanded",
	"DELETE /api/domains/{id}":                  "removes a record",
	"POST /api/domains/{id}/check":              "reads DNS, stores a result",
	"POST /api/domains/connected/ack":           "ids of domains, sets a flag",
	"POST /api/landings/{id}/archive":           "archiveMacroUse: every file and the resulting entry",
	"PUT /api/landings/{id}/files/{path...}":    "fileOpMacro: the file and the resulting entry",
	"DELETE /api/landings/{id}/files/{path...}": "fileOpMacro: the resulting entry",
	"POST /api/simulate":                        "dry run, stores nothing",
	"POST /api/boards/import":                   "copy by id; ids are not templates",
	"POST /api/boards/move":                     "move by id; ids are not templates",
}

func TestEveryWriteRouteIsInventoried(t *testing.T) {
	seen := map[string]bool{}
	for pattern, rule := range routeAccess {
		if rule.admin || strings.HasPrefix(pattern, "GET ") {
			continue
		}
		seen[pattern] = true
		if _, ok := writeRouteGuard[pattern]; !ok {
			t.Errorf("route %s is open to non-administrators and changes data: say in writeRouteGuard how it keeps a hidden macro out", pattern)
		}
	}
	for pattern := range writeRouteGuard {
		if !seen[pattern] {
			t.Errorf("writeRouteGuard lists %s, which is not a non-administrator write route", pattern)
		}
	}
}
