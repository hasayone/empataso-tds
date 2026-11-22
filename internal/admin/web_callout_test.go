package admin

import (
	"strings"
	"testing"
)

// TestSPACallout checks the shared notice box: the kinds, the theme tokens in all three token blocks,
// the render and update helpers, and that the integration notices use it instead of muted hint paragraphs.
// t is the test handle. It returns nothing and fails when the component or one of its uses regresses.
func TestSPACallout(t *testing.T) {
	html := spaSource()

	for _, sel := range []string{".callout.warn", ".callout.err", ".callout.ok", ".callout[hidden]", ".callout>svg.ic", ".callout .ca"} {
		must(t, html, sel, "callout style "+sel)
	}
	for _, token := range []string{"--info:", "--info-soft:", "--info-bd:", "--ok-bd:", "--warn-bd:", "--bad-bd:"} {
		if n := strings.Count(html, token); n != 3 {
			t.Errorf("token %s is defined %d times, want 3 (light, dark, system dark)", token, n)
		}
	}

	must(t, html, "function calloutHTML(kind,text,opts)", "render helper")
	must(t, html, "function calloutSet(el,text,kind)", "update helper")
	must(t, html, "const CALLOUT_ICON={info:'info',warn:'alert',err:'circle-alert',ok:'check'}", "kind to icon map")
	for _, icon := range []string{"  info:'", "  'circle-alert':'"} {
		must(t, html, icon, "icon for callout")
	}

	cf := spaFunctionLine(html, "cloudflarePanel")
	for _, needle := range []string{
		"calloutHTML('info',tr('cf.intro'))",
		"calloutHTML(cur.local?'warn':'err'",
		"id:'cf-ip-hint'",
		"id:'cf-status'",
		"calloutSet(q('#cf-status',host)",
	} {
		must(t, cf, needle, "Cloudflare card")
	}

	nc := spaFunctionLine(html, "namecheapPanel")
	for _, needle := range []string{
		"calloutHTML('info',tr('nc.intro'))",
		"calloutHTML('warn',tr('nc.local'),{id:'nc-local-hint'})",
		"id:'nc-contacts-note'",
		"id:'nc-status'",
		"calloutSet(q('#nc-status',host)",
	} {
		must(t, nc, needle, "Namecheap card")
	}

	kt := spaFunctionLine(html, "keitaroPanel")
	for _, needle := range []string{"id:'kt-status'", "id:'kt-api'", "id:'kt-check-err'", "calloutSet(q('#kt-status')"} {
		must(t, kt, needle, "Keitaro connection card")
	}

	for _, needle := range []string{
		"${calloutHTML('warn','',{id:'pb-note',hidden:true,cls:'mt'})}",
		"calloutSet(q('#pb-note',m), isLocalHost(cur())?tr('pb.local'):'', 'warn')",
		"${calloutHTML('info','',{id:'nr-info',hidden:true})}",
		"const info=(text,kind)=>calloutSet(q('#nr-info',m), text, kind||'info')",
		"info(trFill('nc.reg.price'",
		"${calloutHTML('err','',{id:'kt-job-note',hidden:true})}",
		"calloutSet(q('#kt-job-note'), msg, 'err')",
		"calloutSet(q('#kt-job-note'), job&&job.stage==='done'?tr('kt.dock.done'):'', 'ok')",
		"calloutSet(q('#kt-job-note'), '')",
	} {
		must(t, html, needle, "status line is a callout")
	}
	must(t, html, "  code{padding:1px 6px;border-radius:5px;background:var(--surface-3);color:var(--fg);font-size:12px}", "code in a callout is the shared chip on a theme token")
	mustNot(t, html, ".callout code{", "a callout has no code style of its own")
	if n := strings.Count(html, "--surface-3:"); n != 3 {
		t.Errorf("token --surface-3 is defined %d times, want 3 (light, dark, system dark)", n)
	}
	for _, field := range []string{"pb-note", "nr-info", "kt-job-note"} {
		for _, hint := range []string{`class="hint" id="` + field + `"`, `class="hint err" id="` + field + `"`, `class="hint warn" id="` + field + `"`} {
			mustNot(t, html, hint, field+" left as a muted hint")
		}
	}
	mustNot(t, html, "classList.toggle('err'", "a status is never a toggled hint class")

	for _, legacy := range []string{
		`<p class="hint">${esc(tr('cf.intro'))}`, `<p class="hint">${esc(tr('nc.intro'))}`,
		`id="nc-local-hint"`, `class="hint err" id="rep-cut"`, `<div class="banner"><span class="dot warn"></span><div>${trFill('cron.offline'`,
	} {
		mustNot(t, html, legacy, "notice left as a muted hint")
	}
}
