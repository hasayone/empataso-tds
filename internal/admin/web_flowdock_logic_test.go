package admin

import (
	"strings"
	"testing"
)

// TestFlowDockPhone checks the phone totals bar: one scrolling row up to 760px,
// hidden while the campaign search is focused, and the wide-screen bar left as it is.
// The summary counters use the same one-row rule. A wide screen keeps its grid.
func TestFlowDockPhone(t *testing.T) {
	html := spaSource()
	must(t, html, ".flowsum{display:flex;flex-wrap:wrap;gap:14px 18px;align-items:center;margin:0;padding:8px 16px;", "wide screen keeps the wrapped totals")
	must(t, html, ".flowsum .sumi{display:flex;flex-direction:column;gap:1px;min-width:64px}", "wide screen stacks a label over its number")
	must(t, html, "body.has-flowdock main{padding-bottom:168px}", "wide screen keeps the tall dock clearance")
	must(t, html, ".kpis{display:grid;grid-auto-flow:column;grid-auto-columns:minmax(0,1fr);gap:12px;margin-bottom:14px}", "wide screen keeps the summary grid")
	phone := spaBetween(html, "@media (max-width:760px){", "th .link{font:inherit;")
	if phone == "" {
		t.Fatal("phone dock rule not found")
	}
	must(t, phone, ".flowdock{display:flex;flex-wrap:nowrap;", "phone dock is one row")
	must(t, phone, ".flowsum{flex:none;flex-wrap:nowrap;", "phone totals stay on that row")
	must(t, phone, ".flowsum .sumi{flex-direction:row;", "phone counters sit beside their labels")
	must(t, phone, "overflow-x:auto", "a long totals line scrolls sideways")
	must(t, phone, "body.has-flowdock main,body.has-pager main{padding-bottom:44px}", "phone dock clearance is one bar")
	must(t, phone, "body.dock-search .flowdock{display:none}", "a focused campaign search hides the dock")
	must(t, phone, "body.dock-search.has-flowdock main{padding-bottom:16px}", "hiding the dock gives the list its space back")
	if strings.Count(html, "body.dock-search .flowdock{display:none}") != 1 {
		t.Error("the campaign search hides the dock only on a phone")
	}
	summary := spaBetween(html, "/* Up to 760px the summary counters are one low row", "@media (prefers-reduced-motion: reduce)")
	if summary == "" {
		t.Fatal("phone summary rule not found")
	}
	must(t, summary, ".kpis{display:flex;flex-wrap:nowrap;", "phone summary counters stay on one line")
	must(t, summary, "overflow-x:auto", "a long summary line scrolls sideways")
	if strings.Index(html, "@media(max-width:820px){") > strings.Index(html, ".kpis{display:flex;flex-wrap:nowrap;") {
		t.Error("the three-column summary grid would override the phone row")
	}
	fn := spaFunctionLine(html, "bindDockSearch")
	if fn == "" {
		t.Fatal("bindDockSearch not found")
	}
	must(t, fn, "classList.add('dock-search')", "focus marks the phone search")
	must(t, fn, "classList.remove('dock-search')", "blur brings the dock back")
	must(t, html, "bindDockSearch(q('#fq'))", "only the campaign search hides the dock")
	must(t, html, "classList.remove('has-flowdock','has-pagehover','has-pager','dock-search')", "leaving a page brings the dock back")
}
