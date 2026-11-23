//go:build uitest

// Behaviour of the traffic log helpers of the SPA, run under node: how a stored click path becomes the fields of the
// visitor tester, and the short texts of the log list. Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run TrafficLogLogic
package admin

import (
	"strings"
	"testing"
)

// TestTrafficLogLogicTester checks that a replayed click reproduces the visitor and drops what cannot be replayed.
func TestTrafficLogLogicTester(t *testing.T) {
	prelude := nodeEq + "const TRACE_HIDDEN='[hidden]';\n"
	runSPANode(t, prelude, []string{"traceQueryOf", "traceTester", "traceDurText", "traceBrief"}, `
eq(traceQueryOf('/promo?sub_id_1=a&token=[hidden]&x=1'), 'sub_id_1=a&x=1', 'a hidden value is not replayed');
eq(traceQueryOf('/promo'), '', 'an address without a query');
eq(traceQueryOf('/promo?'), '', 'an empty query');
eq(traceQueryOf(undefined), '', 'no address');
eq(traceQueryOf('/p?a=1&&b=2'), 'a=1&b=2', 'empty pairs are dropped');

const uas = {'iPhone':'UA-IPHONE','Windows':'UA-WIN'};
const click = {ip:'203.0.113.7', url:'/promo?sub_id_1=abc&api=[hidden]',
  visitor:{country:'DE', lang:'de', referer:'https://r.example/', keyword:'shoes', useragent:'UA-IPHONE', unique:false}};
eq(traceTester(click, uas), {ip:'203.0.113.7', ua:'iPhone', uaRaw:'', country:'de', lang:'de', referer:'https://r.example/',
  key:'shoes', query:'sub_id_1=abc', repeat:true}, 'a click with a preset browser');
const odd = traceTester({ip:'1.2.3.4', visitor:{useragent:'Custom/1.0', unique:true}}, uas);
eq([odd.ua, odd.uaRaw, odd.repeat, odd.country], ['', 'Custom/1.0', false, ''], 'a browser outside the presets is kept as it was');
eq(traceTester({}, uas).ip, '', 'an empty path fills nothing');
eq(traceTester({ip:'1.1.1.1', visitor:{useragent:''}}, uas).uaRaw, '', 'no browser stays empty');

eq(traceDurText(740), '740 µs', 'microseconds');
eq(traceDurText(3200), '3.2 ms', 'milliseconds with a decimal');
eq(traceDurText(120400), '120 ms', 'long times are whole milliseconds');
eq(traceDurText(-5), '0 µs', 'a negative time is zero');

eq(traceBrief({ip:'1.2.3.4', stream:'-', action:'show_text', visitor:{country:'RU', os:'Android', browser:'Chrome'}}),
  {ip:'1.2.3.4', country:'ru', device:'Android Chrome', stream:'-', action:'show_text'}, 'the summary of a click');
eq(traceBrief({ip:'1.2.3.4', visitor:{country:'-'}}).country, '', 'an unknown country is not shown');
eq(traceBrief({}), {ip:'', country:'', device:'', stream:'', action:''}, 'the summary of nothing');
`)
}

// TestTrafficLogLogicWiring checks that the page offers the log as the third item of both campaign menus, the click
// path in the visit card and the replay in both, and that every text has all three languages.
func TestTrafficLogLogicWiring(t *testing.T) {
	html := spaSource()
	for _, want := range []string{
		"openTrafficLog(g)",
		"loadClickPath(el,x)",
		"replayClick(list[0])",
		"replayClick(t)",
		"SIM.trace?",
		"TV.uaRaw||UAS[TV.ua]",
		"if(TESTER_RUN){TESTER_RUN=false; run()}",
	} {
		must(t, html, want, "traffic log wiring")
	}
	if n := strings.Count(html, "on:()=>openTrafficLog(g)"); n != 2 {
		t.Errorf("the log is offered in %d campaign menus, want 2", n)
	}
	i18n := spaBetween(html, "const I18N={", "\n};")
	used := map[string]bool{}
	for _, part := range []string{
		spaBetween(html, "function traceHTML(", "function replayClick("),
		spaBetween(html, "function replayClick(", "/* ---------- stream editor (drawer)"),
		spaBetween(html, "async function loadClickPath(", "function logDetail("),
	} {
		for _, open := range []string{"tr('", "trFill('"} {
			for _, k := range quotedKeys(part, open) {
				used[k] = true
			}
		}
	}
	for _, lang := range []string{"en", "ru", "uk"} {
		dict := spaBetween(i18n, "\n"+lang+":{", "\n}")
		for k := range used {
			if k == "tl.path.step" {
				continue // the step number is appended; both steps are checked by name below
			}
			if strings.HasPrefix(k, "tl.") || k == "flows.log.traffic" {
				if !strings.Contains(dict, "'"+k+"':") {
					t.Errorf("%s has no text for %s", lang, k)
				}
			}
		}
	}
	for _, k := range []string{"tl.note.api", "tl.note.bot_served", "tl.note.antiflood", "tl.note.group_link", "tl.note.curl_error", "tl.err.group_link", "tl.path.step0", "tl.path.step1", "tl.title", "flows.log.traffic"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !strings.Contains(spaBetween(i18n, "\n"+lang+":{", "\n}"), "'"+k+"':") {
				t.Errorf("%s has no text for %s", lang, k)
			}
		}
	}
}

// quotedKeys returns the literal keys that follow every occurrence of open in src, up to the closing quote.
func quotedKeys(src, open string) []string {
	var out []string
	for rest := src; ; {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		if j := strings.IndexByte(rest, '\''); j > 0 {
			out = append(out, rest[:j])
		}
	}
}
