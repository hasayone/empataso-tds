package admin

import (
	"strings"
	"testing"
)

// TestAutomationJobCards checks the collapsed source tab, the conversion
// template switch, the flagged-domain checkbox and the live-domain check.
func TestAutomationJobCards(t *testing.T) {
	html := spaSource()
	card := spaFunctionLine(html, "cronJobCardHTML")
	bodyAt := strings.Index(card, "jobBody(job)")
	srcAt := strings.Index(card, "jobSources(job)")
	statusAt := strings.Index(card, `class="status"`)
	if bodyAt < 0 || srcAt < bodyAt || statusAt < srcAt {
		t.Fatalf("card order body=%d sources=%d status=%d", bodyAt, srcAt, statusAt)
	}
	fold := spaFunctionLine(html, "cronSourcesFold")
	if !strings.Contains(fold, `data-srcfold`) || !strings.Contains(fold, `CRON_SRC_OPEN.has(job)?' open':''`) {
		t.Fatal("source tab must start closed")
	}
	if !strings.Contains(fold, "cron.sources") {
		t.Fatal("source tab label")
	}
	sources := spaFunctionLine(html, "jobSources")
	for _, id := range []string{"ip_lists", "bot_signatures", "geo_db"} {
		if !strings.Contains(sources, "case '"+id+"'") || !strings.Contains(sources, `data-add="`+id+`"`) {
			t.Fatalf("sources missing %s", id)
		}
	}
	if strings.Contains(spaFunctionLine(html, "jobBody"), "ip_lists.sources") {
		t.Fatal("ip sources must sit in the tab, under the schedule")
	}
	if !strings.Contains(spaFunctionLine(html, "jobBody"), "data-botsig-state") {
		t.Fatal("bot signature state stays above the source tab")
	}
	tpl := spaFunctionLine(html, "convTemplatesHTML")
	if !strings.Contains(tpl, `data-conv-tpl="each"`) || !strings.Contains(tpl, "conversions.template") || !strings.Contains(tpl, `data-conv-tpl="daily"`) || !strings.Contains(tpl, "data-conv-summary") {
		t.Fatal("both conversion templates must be on the card")
	}
	paint := spaFunctionLine(html, "paintAutomation")
	if !strings.Contains(paint, "summary_template") || !strings.Contains(paint, "data-conv-tpl") {
		t.Fatal("switching the schedule must keep both templates")
	}
	if !strings.Contains(paint, "convSummaryPlaceholder") {
		t.Fatal("an unchanged daily template must not be stored")
	}
	if !strings.Contains(paint, "cronCardOrder(cronDefaultIds(), cronOrderLoad())") {
		t.Fatal("card order must come from the saved list")
	}
	bind := spaFunctionLine(html, "bindCronArrange")
	if !strings.Contains(bind, "closest('.job-grip')") || strings.Contains(bind, "if(!CRON_ARRANGING) return") {
		t.Fatal("a card moves by its handle without arrange mode")
	}
	if strings.Contains(html, ".job-grip{display:none") {
		t.Fatal("the drag handle must stay visible")
	}
	if !strings.Contains(spaFunctionLine(html, "jobBody"), `data-p="virustotal.notify"`) || !strings.Contains(html, "cron.vt.notify") {
		t.Fatal("flagged-domain notification checkbox")
	}
	live := spaFunctionLine(html, "liveDomains")
	if !strings.Contains(live, "!d.disabled") || !strings.Contains(live, "domainPending") {
		t.Fatal("live check must skip disabled and pending domains")
	}
	if !strings.Contains(html, `id="dom-check-live"`) || !strings.Contains(html, "liveDomains(DOMAINS)") || !strings.Contains(html, "checkDomain(d, ()=>{}, true)") {
		t.Fatal("live-domain button must use the existing domain check")
	}
}

// TestConvSummaryFieldVariables checks that the daily field shows the summary
// tokens and does not show a sample amount. A stored text with those tokens
// is kept; the amount placeholder is not the template.
func TestConvSummaryFieldVariables(t *testing.T) {
	html := spaSource()
	const key = "'cron.conv.summary.skel':'"
	rest := html
	n := 0
	for {
		i := strings.Index(rest, key)
		if i < 0 {
			break
		}
		rest = rest[i+len(key):]
		end := strings.Index(rest, "'")
		if end < 0 {
			t.Fatal("unterminated summary template")
		}
		val := rest[:end]
		rest = rest[end:]
		n++
		if strings.Contains(val, "12.50") {
			t.Fatalf("summary template %d contains 12.50: %s", n, val)
		}
		for _, tok := range []string{"[PERIOD]", "[PROFIT]", "[CAMPAIGNS]", "[STREAMS]", "[COUNTRIES]", "[DEVICES]", "[KEYWORDS]"} {
			if !strings.Contains(val, tok) {
				t.Fatalf("summary template %d missing %s", n, tok)
			}
		}
	}
	if n != 3 {
		t.Fatalf("summary templates = %d, want 3", n)
	}
	field := spaFunctionLine(html, "convTemplatesHTML")
	if strings.Contains(field, "12.50") || !strings.Contains(field, "convSummaryPlaceholder") || !strings.Contains(field, "convSummarySkeleton") {
		t.Fatal("the daily field must show the variable template")
	}
	keep := spaFunctionLine(html, "convSummaryPlaceholder")
	if !strings.Contains(keep, "convSummarySamples") || !strings.Contains(keep, "convSummarySkeleton") {
		t.Fatal("a saved template with variables must stay, an amount placeholder must not")
	}
}
