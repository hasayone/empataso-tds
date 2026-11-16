package admin

import (
	"strings"
	"testing"
)

// TestSPAKeyLifetime checks the keys tab: the lifetime select on create only, the
// lifetime badge and last-use columns, the Extend action that sends ttl_days, the
// key prefix taken from the record, no way to show a secret again, and every
// dictionary key once per language.
func TestSPAKeyLifetime(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	for _, fn := range []string{"keyLife", "keyLastUse"} {
		must(t, pure, "function "+fn+"(", fn+" in the pure block")
	}
	must(t, pure, "const KEY_TTL_DAYS=[0, 7, 30, 90, 365];", "lifetimes match security.KeyTTLDays")
	table := spaFunctionLine(html, "paintKeyTable")
	for _, want := range []string{"keyLifeHTML(item)", "keyUseHTML(item)", "{id:'expires_at',label:'key.life',on:1}", "{id:'last_used_at',label:'key.used',on:1}", "on:()=>extendKey(item)", "(item&&item.lead||'kz_')"} {
		must(t, table, want, "paintKeyTable: "+want)
	}
	must(t, spaFunctionLine(html, "keyFormHTML"), "${item?'':keyTTLSelectHTML(tr('key.ttl'))}", "lifetime is chosen on create")
	must(t, spaFunctionLine(html, "readKeyForm"), "body.ttl_days=+ttl.value||0", "create sends ttl_days")
	must(t, spaFunctionLine(html, "extendKey"), "{ttl_days:+q('#key-ttl', box).value||0}", "extend sends ttl_days")
	must(t, spaFunctionLine(html, "keyLifeHTML"), "life.tone?' '+life.tone:''", "badge tone")
	mustNot(t, table, "icon('eye')", "a stored key is a hash; it cannot be shown again")
	for _, k := range []string{
		"key.life", "key.life.never", "key.life.expired", "key.life.soon", "key.life.until",
		"key.used", "key.used.never", "key.ttl", "key.ttl.days", "key.ttl.hint", "key.extend",
	} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
}
