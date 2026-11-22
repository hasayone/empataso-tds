package admin

import (
	"regexp"
	"testing"

	"github.com/hasayone/empataso-tds/internal/store"
)

// subIDExtraRe matches the sub_id_N fields beyond the twelve the dictionaries list:
// the SPA fills their caption at start and fieldLabel falls back to "Sub ID N".
var subIDExtraRe = regexp.MustCompile(`^sub_id_(1[3-9]|2\d|30)$`)

// spaHiddenFields lists catalog fields the SPA drops from its field lists, so they have no caption
// and no place in FIELD_ORDER: dep2sum is stored but is not a second deposit column.
var spaHiddenFields = map[string]bool{"dep2sum": true}

// TestFieldCatalogHasCaptions checks that every field the server offers to the SPA
// (filters, breakdown, dimensions) has a fld.<name> caption in I18N for en, ru and uk,
// so the interface never shows a raw key such as fld.cost_cur.
func TestFieldCatalogHasCaptions(t *testing.T) {
	html := spaSource()
	for _, lang := range []string{"en", "ru", "uk"} {
		keys := spaDictKeys(spaConstLangBody(html, "I18N", lang))
		for _, f := range store.StaticFields() {
			if subIDExtraRe.MatchString(f.Name) || spaHiddenFields[f.Name] {
				continue
			}
			if _, ok := keys["fld."+f.Name]; !ok {
				t.Errorf("field %q has no caption fld.%s in I18N.%s", f.Name, f.Name, lang)
			}
		}
	}
}
