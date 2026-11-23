package admin

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// spaSource returns the embedded SPA with LF line endings for substring checks.
// The text is built once per test binary, so every caller shares one string.
var spaSource = sync.OnceValue(func() string {
	return strings.ReplaceAll(string(indexHTML), "\r\n", "\n")
})

// TestServeUIServesSPA — the embedded SPA is served at the root without auth,
// with the correct Content-Type and a non-empty body.
func TestServeUIServesSPA(t *testing.T) {
	rec := httptest.NewRecorder()
	serveUI(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("serveUI → 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, expected text/html", ct)
	}
	if rec.Body.Len() < 5000 {
		t.Errorf("SPA body suspiciously small: %d bytes", rec.Body.Len())
	}
}

// TestSPAServedViaHandler — the GET / route in the shared Handler() serves the SPA.
func TestSPAServedViaHandler(t *testing.T) {
	srv, _, _ := fullServer(t, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / → 200, got %d", resp.StatusCode)
	}
}

// TestSPAStructure — the markup contains the key elements of the interface:
// one top bar with the navigation (and no sidebar), the flow canvas with its
// wires, the drawer editors, the save bar, and both themes.
func TestSPAStructure(t *testing.T) {
	html := spaSource()

	// Shell: a single top bar; the navigation is built from the NAV table.
	must(t, html, `class="top"`, "top bar")
	must(t, html, `id="nav"`, "navigation")
	for _, page := range []string{"dashboard", "flows", "logs", "conversions", "keywords", "lists", "tools", "automation"} {
		must(t, html, `['`+page+`','`, "navigation entry "+page)
	}
	mustNot(t, html, `class="sidebar"`, "a sidebar — the layout has none")
	must(t, html, `id="period"`, "period selector")
	must(t, html, `data-pick`, "flow row checkbox")
	must(t, html, `id="clang"`, "country language on settings")
	must(t, html, `Испания`, "russian country name")
	must(t, html, `/api/affiliate-networks`, "affiliate networks")
	must(t, html, `period.today`, "period presets")
	must(t, html, `id="userbtn"`, "user menu")
	must(t, html, `window.addEventListener('hashchange',render)`, "hash routing")

	// Light/dark theme: a toggle in the top bar, tokens for both, the system
	// preference honoured until the user picks one.
	must(t, html, `data-theme-btn`, "theme toggle")
	must(t, html, `:root[data-theme="dark"]`, "dark theme tokens")
	must(t, html, `prefers-color-scheme: dark`, "system theme preference")
	must(t, html, `localStorage.getItem('emtds-theme')`, "remembered theme")

	// Dashboard: the performance table by flow and stream.
	must(t, html, `/api/stats/performance`, "performance request")
	must(t, html, `class="perft"`, "performance table")

	// Flows: table by default, cards on demand, then a canvas per flow.
	must(t, html, `id="fgrid"`, "flow list")
	must(t, html, `FLOW_VIEW='table'`, "flows table by default")
	must(t, html, `emtds-flows-view`, "remembered flows layout")
	must(t, html, `data-flow-view="grid"`, "cards layout switch")
	must(t, html, `id="canvas"`, "flow canvas")
	must(t, html, `id="wires"`, "wires layer")
	must(t, html, `data-grip`, "drag handle")
	must(t, html, `data-toggle`, "per-stream switch")
	must(t, html, `id="fbnode"`, "the no-match node")
	must(t, html, `data-goto=`, "link to another flow")
	must(t, html, `/api/simulate`, "visitor test request")

	// Editors live in a drawer; conditions are added from a menu.
	must(t, html, `class="drawer"`, "drawer")
	must(t, html, `data-cond="${c.k}"`, "condition row")
	must(t, html, `data-c="${c.k}"`, "add-condition menu")
	for _, k := range []string{"country", "asn", "org", "timezone", "get", "operators", "ip_list", "schedule", "limit"} {
		must(t, html, `{k:'`+k+`'`, "condition "+k)
	}
	must(t, html, `id="g_savekeys"`, "save-keywords flow setting")

	// Nobody types separators: lists are chips, outputs are rows of variants,
	// page rewrites are find → replace pairs; a text condition can switch to a
	// regular expression. The helpers behind them are checked under node.
	must(t, html, `<div class="tags" id="${id}"`, "chip list field")
	must(t, html, `data-addvar=`, "add-a-variant button")
	must(t, html, `data-addrw=`, "add-a-replacement button")
	must(t, html, `data-rxon=`, "switch to a regular expression")
	must(t, html, `f_iplist_t`, "ip list chips")
	must(t, html, `data-iptext`, "ip list text switch")
	must(t, html, `tags.fold`, "chip lists fold to two rows")
	must(t, html, `data-listtext`, "list condition text switch")
	must(t, html, `data-tagmore`, "chip list expand button")
	must(t, html, `function deviceFlags`, "device flag encoder")
	must(t, html, `data-dyes`, "device yes or no")
	must(t, html, `data-dfull`, "drawer full screen")
	must(t, html, `function openStreamOrder`, "stream order from the number")
	must(t, html, `f_iplist_text`, "ip list textarea")
	must(t, html, `function ipEntries`, "ip entry splitter")
	must(t, html, `flushIPDraft`, "ip list flush before campaigns")
	mustNot(t, html, `id="f_iplist_file"`, "ip list file field removed")
	must(t, html, `// ---- pure: begin ----`, "start of the helpers block run by web_logic_test.go")
	must(t, html, `// ---- pure: end ----`, "end of the helpers block run by web_logic_test.go")
	mustNot(t, html, `(<code>|||</code>)`, "asking people to type ||| between variants")

	// One save bar for everything that can be edited.
	must(t, html, `id="savebar"`, "save bar")
	must(t, html, `id="dirty"`, "unsaved-changes label")

	// Logs: condition builder backed by the field catalog.
	must(t, html, `function filterBuilder(`, "filter builder")
	must(t, html, `/api/log-fields`, "log field catalog")
	must(t, html, `/api/log-values`, "log field values")
	must(t, html, `const flag=`, "country flag helper")
	must(t, html, `class="flag"`, "country flag in the geo column")

	// Tools and automation.
	must(t, html, `/api/lookup`, "IP lookup request")
	must(t, html, `/api/cron`, "cron config request")
	must(t, html, `/api/cron/run`, "run-now request")
}

// TestSPAKeyFunctions — critical UI functions are present (protection against
// accidental removal when refactoring the markup/script).
func TestSPAKeyFunctions(t *testing.T) {
	html := spaSource()
	for _, fn := range []string{
		"function app(", "function render(", "function route(",
		"function flows(", "function flowPage(", "function renderFlow(",
		"function drawWires(", "function bindSort(", "function moveStream(",
		"function openStream(", "function paintStream(", "function collectStream(",
		"function openFlowSettings(", "function collectFlow(",
		"function condChips(", "function condRow(", "function resetCond(", "function configured(",
		"function readLF(", "function outHTML(", "function paintTester(",
		"function tagField(", "function bindTags(", "function variantsField(", "function bindVariants(",
		"function rewriteField(", "function bindRewrite(",
		"function altWords(", "function listForm(", "function listValue(", "function condOps(", "function rxProblem(",
		"function splitVariants(", "function joinVariants(", "function rewriteRows(", "function joinRewrite(",
		"function humanSeconds(", "function dayList(",
		"function saveAll(", "function saveFlows(", "function saveCron(", "function syncSaveBar(", "function markDirty(",
		"function condsQS(", "function ensureCatalogs(", "function logDetail(",
		"function toggleTheme(", "function drawChart(", "function renderPerf(",
		"function tools(", "function automation(", "function paintCronStatus(",
	} {
		must(t, html, fn, fn)
	}
}

// TestFlowBandsByPosition checks stripe bands follow visible metric columns in display order.
func TestFlowBandsByPosition(t *testing.T) {
	html := spaSource()
	mustNot(t, html, `FLOW_BAND`, "fixed band id set")
	must(t, html, `const FLOW_METRIC=new Set(`, "metric column set")
	i := strings.Index(html, `const FLOW_METRIC=new Set(`)
	if i < 0 {
		t.Fatal("FLOW_METRIC declaration missing")
	}
	line := html[i:]
	if n := strings.Index(line, "\n"); n >= 0 {
		line = line[:n]
	}
	must(t, line, `'leads'`, "leads is a metric column")
	must(t, line, `'epc'`, "epc is a metric column")
	must(t, line, `'cpc'`, "cpc is a metric column")
	bands := spaFunctionLine(html, "flowBands")
	must(t, bands, `k%2===0`, "band on every other metric column")
	cell := spaFunctionLine(html, "flowMetricCell")
	must(t, cell, `bands&&bands.has(id)`, "band comes from the caller set")
	paint := spaFunctionLine(html, "paintFlowCards")
	must(t, paint, `const bands=flowBands(cols);`, "bands computed from visible columns")
	must(t, paint, `flowMetricCell(g,c.id,bands)`, "cells receive the band set")
	must(t, paint, `FLOW_METRIC.has(c.id)`, "numeric header uses the metric set")
}

// TestDashboardUIFiltersBrand — dashboard quiet refresh, UI prefs, brand, integrations, and chart tokens.
func TestDashboardUIFiltersBrand(t *testing.T) {
	html := spaSource()
	form := spaFunctionLine(html, "formModal")
	must(t, form, `if(res!=='stay') render()`, "formModal skips render when onSave returns stay")
	must(t, html, "function dashQS(", "dashQS helper")
	must(t, html, "let DASH_PERF", "dashboard-scoped performance cache")
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	must(t, dash, "/api/stats/performance?'+qs", "filtered performance uses dashQS")
	must(t, dash, `id="dash-refresh"`, "dashboard refresh control")
	must(t, dash, `id="dash-panels"`, "dashboard panels control")
	must(t, html, "--chart-cost:#d1242f", "light chart cost colour")
	must(t, html, "--chart-cost:#ff6b6b", "dark chart cost colour")
	must(t, html, "async function loadUI(", "loadUI fetches /api/ui")
	must(t, html, "async function saveUI(", "saveUI writes /api/ui")
	saveCols := spaFunctionLine(html, "saveCols")
	mustNot(t, saveCols, "localStorage.setItem", "column prefs go to the server")
	must(t, html, "/api/brand", "public brand endpoint on login")
	mustNot(t, html, "EmpatASO", "EmpatASO branding removed")
	mustNot(t, html, "base64,iVBOR", "embedded logo removed")
	menuLine := spaFunctionLine(html, "userMenuItems")
	must(t, menuLine, "adminMenuEntries()", "user menu takes its entries from the settings list")
	must(t, spaBetween(html, "const SETTINGS_TABS=[", "\n];"), "key:'menu.integrations', route:'integrations'", "user menu integrations")
	mustNot(t, menuLine, `tr('menu.keitaro')`, "user menu without keitaro")
	pagesAt := strings.Index(html, "const PAGES=")
	pagesEnd := strings.Index(html[pagesAt:], "\n")
	must(t, html[pagesAt:pagesAt+pagesEnd], "keitaro:integrations", "keitaro route alias")
	set := spaFunctionLine(html, "settings")
	must(t, set, `id="company-card"`, "company settings card")
	must(t, html, "defaultCompany", "default company helper")
	must(t, html, `let BRAND={name:''`, "BRAND defaults to an empty name")
}

// TestDashboardRecentClicks — the filter button is gone and the recent-clicks card reads the click log only with access.
func TestDashboardRecentClicks(t *testing.T) {
	html := spaSource()
	mustNot(t, html, `id="dash-filters"`, "dashboard filter button")
	mustNot(t, html, `id="dash-chips"`, "dashboard filter chips")
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	must(t, dash, `id="dash-panels"`, "panels gear stays")
	must(t, dash, `id="recent"`, "recent clicks card")
	must(t, dash, `pageAllowed(ME,'logs')`, "recent clicks only with logs access")
	must(t, dash, "/api/logs?'+dashQS()+'&limit=10", "recent clicks read the click log for the period and the kept campaign")
	must(t, dash, "recentRows(", "recent rows go through recentRows")
	must(t, dash, "x.campaign_id||", "recent clicks link by campaign slug")
	mustNot(t, dash, "#/flows/${esc(x.group)}", "recent clicks must not link by campaign name")
	must(t, dash, "function recentDest(", "recent clicks destination helper")
	must(t, dash, "idLabel('landing_id'", "landing destination shows the catalog name")
	must(t, dash, "NOOUT.has(red)", "types without an address show the type label")
	must(t, dash, "ensureLandings()", "recent clicks load the landing catalog")
	must(t, dash, "function destHTML(", "destination cell helper")
	must(t, dash, "'LP · '+name", "landing destination caption is LP with the name")
	must(t, dash, "entityHTML('landing_id', id)", "landing destination links the landing")
	must(t, dash, "${destHTML(x)}</td>", "recent clicks render the destination cell")
	must(t, dash, "if(id==='out') bits.push('ell')", "the destination header clips with its cell")
	must(t, html, "#recent-table table{width:100%;table-layout:fixed}", "the recent-clicks table stays inside its card")
	must(t, html, "#recent-table th .link{display:block;box-sizing:border-box;min-width:0;max-width:100%;overflow:hidden;text-overflow:ellipsis}", "the destination header ellipsizes inside the cell")
	must(t, html, "if(id==='out') return destHTML(x);", "click log renders the destination cell")
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	must(t, pure, "function recentRows(", "recentRows in pure block")
	must(t, html, `'dash.recent':'Последние клики'`, "ru recent clicks")
	must(t, html, `'dash.recent':'Останні кліки'`, "uk recent clicks")
	must(t, html, `'dash.recent.out':'Destination'`, "en destination")
}

// TestDashboardPerfControls — performance table defaults, pure helpers, export scope, KPI tiles, and fold nav.
func TestDashboardPerfControls(t *testing.T) {
	html := spaSource()
	must(t, html, `PSORT={k:'conv'`, "default perf sort by conversions")
	must(t, html, `let PERF_TOP=true, PERF_FOLD=true`, "perf fold and top flags")
	must(t, html, `const PERF_TOP_N=10`, "perf top limit")
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	must(t, pure, "function perfTop(", "perfTop in pure block")
	must(t, pure, "function perfCmp(", "perfCmp in pure block")
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	must(t, dash, `id="perftop"`, "perf top button")
	must(t, dash, `id="perffold"`, "perf fold button")
	must(t, dash, `<div class="legend">${legs}</div>`, "chart legend under the title")
	must(t, dash, `id="perfexport"`, "perf export button")
	must(t, dash, `menu(e.currentTarget,[{label:tr('Export CSV'),on:()=>exportPerf(`, "perf export menu")
	mustNot(t, dash, `id="perfcsv"`, "no separate csv button")
	mustNot(t, dash, `id="perfhtml"`, "no separate html button")
	must(t, dash, `class="lg-name"`, "legend button keeps its name")
	must(t, html, `.legend{display:flex;flex-wrap:nowrap;justify-content:flex-end`, "legend stays one row on the right")
	must(t, html, `.legend .lg:hover .lg-name,.legend .lg:active .lg-name`, "mobile legend name shows on hover and press")
	must(t, html, `.legend .lg{position:relative;width:32px;height:32px`, "mobile legend is an icon button")
	must(t, html, `@media(min-width:1181px){.bars .k{width:auto;flex:1 1 auto;min-width:68px} .bars .tr{flex:1 1 140px;max-width:140px;min-width:16px}}`, "wide breakdown bar is up to 140px and the label keeps 68px")
	sorted := spaFunctionLine(html, "perfSorted")
	must(t, sorted, "perfCmp(", "perfSorted uses perfCmp")
	render := spaFunctionLine(html, "renderPerf")
	must(t, render, "PERF_FOLD", "renderPerf respects fold flag")
	must(t, render, `icon(PERF_FOLD?'expand':'collapse')`, "fold button is an icon")
	mustNot(t, render, `btnFold.textContent=`, "fold button is not a text label")
	mustNot(t, spaFunctionLine(html, "exportPerf"), "perfTop(", "export skips perfTop")
	mustNot(t, spaFunctionLine(html, "perfSheet"), "perfTop(", "html export skips perfTop")
	mustNot(t, spaFunctionLine(html, "paintFlowCards"), "perfTop(", "flow cards skip perfTop")
	must(t, html, `'Top 10':'Топ-10'`, "ru Top 10")
	must(t, html, `'Expand':'Развернуть'`, "ru Expand")
	must(t, html, `'Collapse':'Свернуть'`, "ru Collapse")
	must(t, html, `'Expand':'Розгорнути'`, "uk Expand")
	must(t, html, `'Collapse':'Згорнути'`, "uk Collapse")
	must(t, html, `.top.nav-fold .navburger{display:inline-flex;order:-1}`, "burger left of brand when folded")
	roiPos := strings.Index(dash, `tr('col.roi')`)
	botsPos := strings.Index(dash, `tr('Bots')`)
	if roiPos < 0 || botsPos < 0 || botsPos <= roiPos {
		t.Error("Bots KPI must follow ROI on the dashboard")
	}
	must(t, dash, `'bots'`, "bots KPI tone class")
	must(t, dash, `'clicks'`, "clicks KPI tone class")
	mustNot(t, dash, `tr('Bots'),pct(bots,total),'warn'`, "bots KPI without warn tone")
}

// TestDashboardKPIMoney — money tiles use moneyText, tiles are ordered clicks→money→rates, and the grid is one row on desktop.
func TestDashboardKPIMoney(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	mustNot(t, pure, "function kpiMoney(", "the tile helper is moneyText itself")
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	must(t, dash, "profitOf(rev, chart.spend.by, chart.ready)", "the profit tile is revenue less spend in USD")
	must(t, dash, "moneyText(chart.ready?rev:0,'USD',2)", "the revenue tile is always dollars")
	mustNot(t, dash, "tr('conv / visits')", "CR tile without conv/visits sub-label")
	must(t, dash, "class=\"vp", "KPI percent uses vp")
	must(t, dash, "vp-end", "bots KPI percent at tile end")
	must(t, html, ".kpi .vp{font-size:8px", "KPI percent font size")
	mustNot(t, dash, "chartAmt(n, curCode, d)", "tiles no longer use chartAmt")
	order := []string{"'clicks')", "'uniq',pct(", "'conv')", "'cost')", "'rev')", "'profit')", "'cr')", "'roi')", "'bots',pct("}
	last := -1
	for _, k := range order {
		p := strings.Index(dash, k)
		if p < 0 || p <= last {
			t.Errorf("KPI tile %s out of order", k)
		}
		last = p
	}
	must(t, html, ".kpis{display:grid;grid-auto-flow:column;grid-auto-columns:minmax(0,1fr)", "one KPI row on desktop")
	must(t, html, ".kpi .v{font-size:clamp(15px,1.3vw,19px)", "KPI value fits nine tiles in the page container")
	must(t, html, ".kpis{grid-auto-flow:row;grid-template-columns:repeat(3,minmax(0,1fr))}", "three KPI tiles per row on small screens")
	mustNot(t, html, ".kpis{grid-template-columns:repeat(2,minmax(0,1fr))}", "two-per-row KPI grid")
}

// TestDashboardChartAxes — chart axes come from shared scales, and percent series are stroked.
func TestDashboardChartAxes(t *testing.T) {
	html := spaSource()
	draw := spaFunctionLine(html, "drawChart")
	mustNot(t, draw, "maxN*k/4", "fixed quarter of the count max")
	mustNot(t, draw, "k/4*h", "fixed quarter of the plot height")
	must(t, draw, "cssVar('--chart-'+", "series colour from a chart CSS variable")
	must(t, draw, "axisScale(", "axis range comes from axisScale")
	must(t, draw, "tickEvery(", "time labels use tickEvery")
	must(t, draw, "tickDecimals(", "money labels use tickDecimals")
	must(t, html, "function axisScale(", "axisScale is defined")
	must(t, html, "function tickEvery(", "tickEvery is defined")
	must(t, html, "function roiPct(", "roiPct is defined")
	must(t, html, "function overlapShare(", "overlapShare is defined")
	series := ""
	if i := strings.Index(html, "const CH_SERIES=["); i >= 0 {
		rest := html[i:]
		if j := strings.Index(rest, "];"); j >= 0 {
			series = rest[:j]
		}
	}
	must(t, series, "id:'cr'", "cr is a chart series")
	must(t, series, "id:'roi'", "roi is a chart series")
	must(t, series, "ic:'click'", "clicks series icon")
	must(t, series, "ic:'user'", "unique series icon")
	must(t, series, "ic:'mail'", "conversions series icon")
	must(t, series, "ic:'trendup'", "revenue series icon")
	must(t, series, "ic:'trenddown'", "cost series icon")
	must(t, series, "ic:'banknote'", "profit series icon")
	must(t, series, "ic:'percent'", "cr series icon")
	must(t, series, "ic:'chart'", "roi series icon")
	mustNot(t, series, "fact:true", "fact flag on a series")
	mustNot(t, series, "money:true", "money flag on a series")
	dash := spaFunctionLine(html, "dashboard")
	must(t, dash, "icon(s.ic)", "legend button uses the series icon")
	mustNot(t, dash, `<i class="${s.sw}">`, "legend button is not a colour stroke")
	tip := spaFunctionLine(html, "chartTip")
	must(t, tip, "sw-cr", "tooltip marks conversion rate")
	must(t, tip, "periodROI()", "tooltip uses period ROI")
	must(t, html, `.ctip i{display:inline-block;width:14px;height:0;border-top:2px solid var(--chart-clicks)}`, "tooltip keeps the colour stroke")
	must(t, html, `.lg svg.ic{width:16px;height:16px;stroke-width:2;color:var(--chart-clicks)}`, "legend icon uses the clicks colour")
	roi := spaFunctionLine(html, "metricROI")
	must(t, roi, "roiPct(", "ROI metric uses roiPct")
}

// TestEditorsDoNotMoveThePage pins a property the interface was rebuilt for
// twice: opening something must not scroll or shift what is under it. Editors
// are drawers that overlay the page and scroll inside themselves; if
// scrollIntoView reappears, a layout that needs chasing has crept back in.
func TestEditorsDoNotMoveThePage(t *testing.T) {
	html := spaSource()
	mustNot(t, html, ".scrollIntoView(", "page-scrolling hack")
	must(t, html, `.drawer{position:fixed`, "the drawer overlays the page")
	must(t, html, `.db{flex:1;overflow-y:auto`, "the drawer body scrolls inside itself")
	must(t, html, `scrollbar-gutter:stable`, "no width jump when a page gets a scrollbar")
}

// TestNavDropdownHover — the reports dropdown button shares the hover and open-menu look of plain nav items, also in the brand scheme.
func TestNavDropdownHover(t *testing.T) {
	html := spaSource()
	must(t, html, ".nav a:hover,.navdrop-btn:hover,.navdrop-btn[aria-expanded=true]{background:var(--surface-2);color:var(--fg)", "dropdown hover shares the nav item rule")
	must(t, html, `:root[data-accent="brand"] .navdrop-btn:hover,:root[data-accent="brand"] .navdrop-btn[aria-expanded=true]`, "brand hover covers the dropdown button")
	must(t, html, ".nav a.active,.navdrop.active .navdrop-btn{color:var(--fg);background:var(--surface-2)}", "active dropdown button shares the active nav item rule")
	must(t, html, ".nav a.active::after,.navdrop.active .navdrop-btn:not(.navmore-btn)::after{", "active reports button gets the nav underline")
	must(t, html, `:root[data-accent="brand"] .nav a.active,:root[data-accent="brand"] .navdrop.active .navdrop-btn{`, "brand scheme paints the active dropdown like an active item")
	must(t, html, `:root[data-accent="brand"] .nav a.active::after,:root[data-accent="brand"] .navdrop.active .navdrop-btn::after{`, "brand scheme colours the dropdown underline")
	mustNot(t, html, ".navdrop.active .navdrop-btn{color:var(--fg)}", "no colour-only active rule for the dropdown")
	mustNot(t, html, ".navdrop-btn:not(.navmore-btn):hover", "no rule cancels the dropdown hover")
	must(t, html, `aria-haspopup="menu" aria-expanded="false">${icon('report')}`, "reports button reports its menu state")
	must(t, html, "anchor.setAttribute('aria-expanded','true')", "open menu marks its anchor expanded")
	must(t, html, "qa('#nav .navdrop').forEach(d=>d.classList.toggle('active',REPORT_IDS.includes(page)))", "reports group follows the open page")
}

// TestRefreshSpin — a refresh click turns the icon a full half-second and holds it until the reload returns.
func TestRefreshSpin(t *testing.T) {
	html := spaSource()
	must(t, html, ".spin svg{animation:spin .5s linear infinite", "refresh icon turns in half a second")
	must(t, html, "const SPIN_TURN=500", "spin is held for one full turn")
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	must(t, dash, "return dashboard(null, live, true)", "dashboard refresh waits for the reload")
	must(t, dash, "busy(q('#dash-refresh'), reloadDash)", "dashboard refresh spins")
	must(t, html, "busy(q('#flow-refresh')", "flow list refresh spins")
	must(t, html, "busy(ref,()=>Promise.resolve(onRefresh())).catch(e=>toast(e.message,true))", "column refresh spins and toasts errors")
}

func must(t *testing.T, html, needle, what string) {
	t.Helper()
	if !strings.Contains(html, needle) {
		t.Errorf("not found in SPA: %s (looked for %q)", what, needle)
	}
}

// TestSPAAIChosenModelsBlock checks that the allowed-models list shows the ticked models above the search with the default one
// marked, in every language, and that the parameters of a ticked model are a form block per model with labelled fields.
func TestSPAAIChosenModelsBlock(t *testing.T) {
	html := spaSource()
	picker := assistantFn(html, "aiModelPicker")
	must(t, picker, "chosen=aiEl('div','aimp-sel')", "the block of the ticked models")
	must(t, picker, "host.appendChild(chosen)}", "the block is added for the ticking list only")
	if strings.Index(picker, "host.appendChild(chosen)}") > strings.Index(picker, "host.appendChild(bar);") {
		t.Error("the block of the ticked models must sit above the search bar")
	}
	must(t, picker, "tr('ai.chat.model.default')", "the default mark reuses the list key")
	must(t, picker, "tr('ai.chat.model.sel.none')", "an empty selection has a caption")
	for _, lang := range []string{"'ai.chat.model.sel.none':'No models selected'", "'ai.chat.model.sel.none':'Модели не выбраны'", "'ai.chat.model.sel.none':'Моделі не обрано'"} {
		must(t, html, lang, "the caption in each language")
	}
	must(t, html, `<div class="aipr"><div class="aipr-h">`, "a form block per ticked model")
	must(t, html, `<label for="ai-pr-${k}-${i}">${tr(label)}</label>`, "a labelled price field")
	mustNot(t, html, `<th>${tr('ai.set.price.model')}</th>`, "no column headers in the price block")
	must(t, html, `<div class="aipr-row">${field('i','ai.set.price.in')}${field('o','ai.set.price.out')}</div>`, "input and output side by side")
	must(t, html, `.aipr-row{display:grid;grid-template-columns:repeat(2,minmax(0,1fr))`, "two columns at any width")
	panel := dictBetween(t, html, "async function aiPanel(host,live){", "\n}\n")
	if strings.Index(panel, `id="ai-limits"`) < strings.Index(panel, `id="ai-prices"`) {
		t.Error("the folded limits must sit below the prices")
	}
	mustNot(t, panel, `id="ai-limits" open`, "the limits are folded by default")

	for _, key := range []string{"'ai.chat.model.setdef':'Make default'", "'ai.chat.model.setdef':'Сделать моделью по умолчанию'", "'ai.chat.model.setdef':'Зробити моделлю за замовчуванням'",
		"'ai.chat.model.remove':'Remove from selected'", "'ai.chat.model.remove':'Убрать из выбранных'", "'ai.chat.model.remove':'Прибрати з обраних'"} {
		must(t, html, key, "the chip action labels in each language")
	}
	must(t, picker, "chipBtn('star','ai.chat.model.setdef'", "the star makes a model the default")
	must(t, picker, "chipBtn('x','ai.chat.model.remove'", "the cross takes a model off")
	must(t, html, ".aimp-sel .chip:hover .aimp-acts,.aimp-sel .chip:focus-within .aimp-acts{", "the chip actions show on hover and keyboard focus")
	funcs := []string{"aiEl", "aiVal", "aiUSD", "aiModelNorm", "aiModelsMerge", "aiModelFilter", "aiModelPickable", "aiModelDefault", "aiModelNext", "aiCtxText", "aiModelMeta", "aiModelPicker"}
	assistantNode(t, funcs, assistantFakeDOM+`const AI = {tick:0};`, `
const models = aiModelsMerge([{id:'a/one', name:'One', tools:true}, {id:'b/two', name:'Two', tools:true}], []);
const o = {models, multi:true, withDefault:true, sel:new Set(), defId:'', onChange(){}};
const host = new El('div');
aiModelPicker(host, o);
const box = () => host.all().find(e => e.className === 'aimp-sel');
const chips = () => box().all().filter(e => e.className.split(' ').includes('chip'));
eq(host.children[0] === box(), true, 'the block is the first thing in the picker');
eq([chips().length, box().textContent], [0, 'ai.chat.model.sel.none'], 'nothing selected: a short caption');
const rowOf = id => host.all().filter(e => e.className.split(' ').includes('aimp-r')).find(r => r.textContent.includes(id));
const first = rowOf('a/one').children[0].children[0];
first.checked = true; first.onchange();
eq([chips().length, chips()[0].textContent.includes('One'), chips()[0].className.includes('def')], [1, true, true], 'ticking shows the chip, the first one is the default');
const second = rowOf('b/two').children[0].children[0];
second.checked = true; second.onchange();
eq(chips().length, 2, 'both chips are shown');
eq(chips().filter(c => c.className.includes('def')).length, 1, 'one chip is the default');
const radio = rowOf('b/two').children[1].children[0];
radio.checked = true; radio.onchange();
const def = chips().find(c => c.className.includes('def'));
eq([def.textContent.includes('Two'), def.textContent.includes('ai.chat.model.default'), chips()[0] === def], [true, true, true], 'the default radio moves the mark and puts it first');
chips()[0].onclick && chips()[0].onclick();
eq([...o.sel].sort(), ['a/one', 'b/two'], 'a chip does not untick');
const again = rowOf('a/one').children[0].children[0];
again.checked = false; again.onchange();
second.checked = false; second.onchange();
eq([chips().length, box().textContent], [0, 'ai.chat.model.sel.none'], 'unticking removes the chips');

// Chip actions: the star sets the default, the cross unticks, taking off the default hands it to the next name in alphabetical order.
const ms = aiModelsMerge([{id:'m/c', name:'cherry', tools:true}, {id:'m/a', name:'Apple', tools:true}, {id:'m/b', name:'banana', tools:true}, {id:'m/n', name:'Plain', tools:false}], []);
const o2 = {models:ms, multi:true, withDefault:true, sel:new Set(['m/a', 'm/b', 'm/c']), defId:'m/a', onChange(){ changed++; }};
let changed = 0;
const host2 = new El('div');
aiModelPicker(host2, o2);
const box2 = () => host2.all().find(e => e.className === 'aimp-sel');
const chip2 = name => box2().all().filter(e => e.className.split(' ').includes('chip')).find(c => c.children[0].textContent === name);
const rowOf2 = id => host2.all().filter(e => e.className.split(' ').includes('aimp-r')).find(r => r.textContent.includes(id));
const act = (name, label) => chip2(name).all().find(b => b.tagName === 'BUTTON' && b.attrs['aria-label'] === label);
eq(chip2('Apple').all().filter(e => e.tagName === 'BUTTON').map(b => b.attrs['aria-label']), ['ai.chat.model.remove'], 'the default chip has only the cross');
eq(chip2('banana').all().filter(e => e.tagName === 'BUTTON').map(b => [b.attrs['aria-label'], b.title]), [['ai.chat.model.setdef', 'ai.chat.model.setdef'], ['ai.chat.model.remove', 'ai.chat.model.remove']], 'another chip has the star and the cross with labels');
act('banana', 'ai.chat.model.setdef').onclick();
eq([o2.defId, [...o2.sel].sort(), changed], ['m/b', ['m/a', 'm/b', 'm/c'], 1], 'the star sets the default and keeps the selection');
eq(document.activeElement && document.activeElement.attrs['aria-label'], 'ai.chat.model.remove', 'the focus stays on the chip after the star');
eq(rowOf2('m/b').children[1].children[0].checked, true, 'the default radio of the list follows the star');
act('cherry', 'ai.chat.model.remove').onclick();
eq([o2.defId, [...o2.sel].sort(), changed], ['m/b', ['m/a', 'm/b'], 2], 'the cross unticks a model and keeps the default');
eq(rowOf2('m/c').children[0].children[0].checked, false, 'the list checkbox follows the cross');
act('banana', 'ai.chat.model.remove').onclick();
eq([o2.defId, [...o2.sel].sort()], ['m/a', ['m/a']], 'taking off the default hands it to the next selected model');
o2.sel = new Set(['m/a', 'm/b', 'm/c']); o2.defId = 'm/c'; aiModelPicker(host2, o2);
act('cherry', 'ai.chat.model.remove').onclick();
eq(o2.defId, 'm/a', 'the last name in the alphabet hands the default to the first');
o2.sel = new Set(['m/a', 'm/b', 'm/c']); o2.defId = 'm/a'; aiModelPicker(host2, o2);
act('Apple', 'ai.chat.model.remove').onclick();
eq(o2.defId, 'm/b', 'the default goes by visible name without case, not by list order');
o2.sel = new Set(['m/a', 'm/n']); o2.defId = 'm/a'; aiModelPicker(host2, o2);
eq(chip2('Plain').all().filter(e => e.tagName === 'BUTTON').map(b => b.attrs['aria-label']), ['ai.chat.model.remove'], 'a model without tools has no star');
act('Apple', 'ai.chat.model.remove').onclick();
eq([o2.defId, [...o2.sel]], ['', ['m/n']], 'no selectable model left: no default');
act('Plain', 'ai.chat.model.remove').onclick();
eq([o2.sel.size, box2().textContent], [0, 'ai.chat.model.sel.none'], 'nothing left: the short caption');
`)
}

func mustNot(t *testing.T, html, needle, what string) {
	t.Helper()
	if strings.Contains(html, needle) {
		t.Errorf("must not be in the SPA: %s (found %q)", what, needle)
	}
}

func spaFunctionLine(html, name string) string {
	open := "function " + name + "("
	i := strings.Index(html, open)
	if i < 0 {
		return ""
	}
	rest := html[i:]
	end := strings.Index(rest, "\nfunction ")
	if a := strings.Index(rest, "\nasync function "); a >= 0 && (end < 0 || a < end) {
		end = a
	}
	if end < 0 {
		end = strings.Index(rest, "\nconst ")
	}
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// TestCalloutRowGap checks that a button row right under a callout with no bottom margin gets a gap,
// and that the postbacks popup puts its lead and sale buttons in such a row.
func TestCalloutRowGap(t *testing.T) {
	html := spaSource()
	must(t, html, ".callout.mt{margin:14px 0 0}", "the tail callout has no bottom margin")
	must(t, html, ".callout.mt+.row{margin-top:14px}", "a row under that callout is spaced")
	pop := spaBetween(html, "async function openPostbacks(", "let PB_DOCS_BACK")
	must(t, pop, "{cls:'mt'})}\n    <div class=\"row wrap\" style=\"gap:8px;margin-bottom:12px\"><button type=\"button\" id=\"pb-pop-lead\">", "lead and sale buttons follow the callout directly and keep a gap above the footer")
}

// TestSettingsGrid checks that settings cards fill columns of a bounded width: two in the container, more on a
// wide screen in the full width, one on a phone, and that the dashboard tiles stop stretching in the full width.
func TestSettingsGrid(t *testing.T) {
	html := spaSource()
	src := spaFunctionLine(html, "settings")
	if src == "" {
		t.Fatal("settings not found in SPA")
	}
	must(t, src, `class="bds setgrid"`, "settings wrapper is setgrid")
	mustNot(t, src, `grid-template-columns:repeat(auto-fit,minmax(340px,440px))`, "settings drops the stretched grid")
	must(t, html, `.bds.setgrid{grid-template-columns:repeat(auto-fill,minmax(min(100%,var(--card-w)),1fr));max-width:var(--set-w)}`, "settings columns of a bounded width")
	must(t, html, `--set-w:var(--read-w);--card-w:380px;`, "the container keeps two settings columns")
	must(t, html, `.bds{display:grid;grid-template-columns:repeat(auto-fit,minmax(max(210px,var(--tile-floor)),1fr));gap:12px}`, "board tiles have a floor")
	mustNot(t, html, `column-count:2`, "no fixed two-column masonry")
}

// TestCampaignMoneyAndROIDisplay — money and ROI cells show zeros, not em dashes.
func TestCampaignMoneyAndROIDisplay(t *testing.T) {
	html := spaSource()
	mt := spaFunctionLine(html, "moneyText")
	cm := spaFunctionLine(html, "campaignMoney")
	cr := spaFunctionLine(html, "metricROI")
	ratio := spaFunctionLine(html, "cellRatio")
	if mt == "" || cm == "" || cr == "" || ratio == "" {
		t.Fatal("moneyText, campaignMoney, metricROI or cellRatio not found in SPA")
	}
	must(t, cm, "moneyMap(by)", "campaignMoney uses moneyMap")
	must(t, mt, "Intl.NumberFormat('en-US'", "moneyText groups with en-US locale")
	must(t, cr, "return '0.00%'", "metricROI without a base is 0.00%")
	mustNot(t, cr, "return NA", "metricROI does not return NA")
	mustNot(t, cr, "'100%'", "metricROI is not 100% without spend")
	mustNot(t, mt, "return '—'", "moneyText must not return em dash")
	mustNot(t, cr, "return '—'", "metricROI must not return em dash")
	mustNot(t, ratio, "'—'", "cellRatio must not return em dash")
	mustNot(t, html, `{id:'bots'`, "bots removed from CH_SERIES")
	must(t, html, `.lg{display:inline-flex;align-items:center;gap:6px;font-size:12px;color:var(--muted);box-shadow:none}`, "legend without button shadow")
	must(t, html, "function stackedPrompt(", "stacked overlay prompt")
	must(t, html, "style=\"z-index:80\"", "stacked overlay z-index 80")
	must(t, html, "groupFieldBlock('dgroup'", "domain group field")
	cc := spaFunctionLine(html, "costCode")
	cpc := spaFunctionLine(html, "cpcCell")
	off := spaFunctionLine(html, "offerCells")
	if cc == "" || cpc == "" || off == "" {
		t.Fatal("costCode, cpcCell or offerCells not found in SPA")
	}
	must(t, cc, ">0", "costCode counts a currency only when its sum is above zero")
	mustNot(t, cpc, "moneyText(0,''", "cpcCell keeps the currency on a zero divisor")
	mustNot(t, off, "moneyText(0,''", "offerCells keeps the currency on a zero divisor")
	mustNot(t, html, "function singleCost(", "singleCost is replaced by costCode")
}

// TestFlowsLockedColumnsAndActions — number and name stay on and pinned, the actions cell follows the name.
func TestFlowsLockedColumnsAndActions(t *testing.T) {
	html := spaSource()
	must(t, html, "const FLOW_LOCKED=new Set(['num','name'])", "number and name stay locked")
	cols := spaFunctionLine(html, "loadCols")
	open := spaFunctionLine(html, "openCols")
	paint := spaFunctionLine(html, "paintFlowCards")
	if cols == "" || open == "" || paint == "" {
		t.Fatal("loadCols, openCols or paintFlowCards not found in SPA")
	}
	must(t, cols, "lockedCols(", "loadCols pins locked columns")
	must(t, open, "disabled", "locked column checkbox is disabled")
	must(t, open, "lockedCols(", "column dialog knows locked columns")
	must(t, paint, `c.id==='name'?'<th class="acts"></th>'`, "actions header follows the name")
	mustNot(t, paint, `<th class="acts"></th></tr></thead>`, "actions header is not trailing")
}

// TestFlowsFolderField — the campaign group is stored in folder, shown in the editor and the table, and is not a metric column.
func TestFlowsFolderField(t *testing.T) {
	html := spaSource()
	open := spaFunctionLine(html, "openFlowSettings")
	collect := spaFunctionLine(html, "collectFlow")
	cell := spaFunctionLine(html, "flowMetricCell")
	cat := spaFunctionLine(html, "colCatalog")
	bands := spaFunctionLine(html, "flowBands")
	if open == "" || collect == "" || cell == "" || cat == "" || bands == "" {
		t.Fatal("openFlowSettings, collectFlow, flowMetricCell, colCatalog or flowBands not found in SPA")
	}
	must(t, open, `groupFieldBlock('g_folder'`, "editor folder combobox field")
	must(t, html, "bindGroupCombo(gf", "editor folder combobox bind")
	must(t, html, "bindGroupInlineCreate(", "editor folder inline create bind")
	must(t, html, "'g_folder'", "editor folder field id")
	must(t, collect, "delete g.folder", "empty folder deletes the key")
	must(t, collect, "g.folder=", "non-empty folder is stored")
	must(t, cell, "id==='folder'", "folder column cell")
	must(t, cat, "['folder','off.group',0]", "flows catalog folder column")
	must(t, html, "'flows.state.any':'All states'", "english any-state label")
	must(t, html, "'flows.state.any':'Все состояния'", "russian any-state label")
	must(t, html, "'flows.state.any':'Усі стани'", "ukrainian any-state label")
	must(t, html, "'flows.nofolder':'No group'", "english no-group label")
	mustNot(t, bands, "'folder'", "flowBands does not treat folder as a metric")
	i := strings.Index(html, "const FLOW_METRIC=")
	if i < 0 {
		t.Fatal("FLOW_METRIC missing")
	}
	line := html[i:]
	if nl := strings.Index(line, "\n"); nl >= 0 {
		line = line[:nl]
	}
	mustNot(t, line, "'folder'", "FLOW_METRIC does not list folder")
}

// TestFlowsGroupsHeader checks that the campaigns header opens the shared group window on folder.
// html is the SPA source. The test fails when the button, its place, or the folder actions are missing.
func TestFlowsGroupsHeader(t *testing.T) {
	html := spaSource()
	body := pageFnBody(html, "flows")
	if body == "" {
		t.Fatal("flows page missing")
	}
	phStart := strings.Index(body, `<div class="ph">`)
	bar := strings.Index(body, `id="fbar"`)
	if phStart < 0 || bar < 0 || bar < phStart {
		t.Fatal("campaigns header missing")
	}
	ph := body[phStart:bar]
	must(t, ph, `groupHeadBtn('flow-groups')`, "campaign groups button sits in the header")
	must(t, body, `sectionAllowed(ME,'groups')`, "button follows the groups section")
	btn := strings.Index(ph, `groupHeadBtn('flow-groups')`)
	gear := strings.Index(ph, `data-col-gear="flows"`)
	if btn < 0 || gear < 0 || btn > gear {
		t.Fatal("groups button must sit left of the column gear")
	}
	must(t, body, "openGroupWindow({", "header opens the shared group window")
	must(t, body, "campaignFolders()", "window lists campaign folders")
	must(t, body, "clearCampaignFolder(", "delete clears the campaign folder")
	must(t, body, "FLOW_NEW_FOLDER", "create prefills a new campaign")
	must(t, spaFunctionLine(html, "groupHeadBtn"), `icon('folder')`, "groups control is a folder icon")
	must(t, spaFunctionLine(html, "groupHeadBtn"), `tr('dom.groups')`, "groups label goes through tr")
	clr := spaFunctionLine(html, "clearCampaignFolder")
	if clr == "" {
		t.Fatal("clearCampaignFolder missing")
	}
	must(t, clr, "for(", "clear is sequential")
	must(t, clr, "await", "clear awaits the save")
	mustNot(t, clr, "Promise.all", "clear must not use Promise.all")
	must(t, clr, "'/api/groups/'", "clear writes the campaign")
	must(t, clr, "delete saved.folder", "clear removes folder from the saved body")
	must(t, clr, "delete g.folder", "clear removes folder in memory")
	must(t, spaFunctionLine(html, "newFlow"), "g.folder=", "new campaign stores the group on folder")
	must(t, html, "'dom.groups':'Groups'", "english groups label")
	must(t, html, "'dom.groups':'Группы'", "russian groups label")
	must(t, html, "'dom.groups':'Групи'", "ukrainian groups label")
}

// TestGroupCreateButton checks that every group field shares one plus control in the same row as the field.
// html is the SPA source. The test fails when the plus, its label, or a translation is missing.
func TestGroupCreateButton(t *testing.T) {
	html := spaSource()
	block := spaFunctionLine(html, "groupFieldBlock")
	if block == "" {
		t.Fatal("groupFieldBlock not found in SPA")
	}
	must(t, block, `class="group-field-row"`, "plus sits in one row with the group field")
	must(t, block, `class="icon grp-add"`, "shared plus class")
	must(t, block, `data-grp-create=`, "plus still opens the create form")
	must(t, block, `icon('plus')`, "plus glyph")
	must(t, block, `title="${label}"`, "create-group title")
	must(t, block, `aria-label="${label}"`, "create-group accessible name")
	must(t, block, `tr('grp.create')`, "label goes through tr")
	mustNot(t, block, `class="link" data-grp-create`, "text Create button is gone")
	must(t, html, ".group-field-row{display:flex;align-items:stretch;gap:6px;min-width:0}", "row keeps the plus beside the field")
	must(t, html, ".group-field-row>.grp-add{flex:none;width:32px;height:auto;min-height:32px;padding:0;align-self:stretch}", "plus matches the field height")
	must(t, html, "'grp.create':'Create group'", "english create-group label")
	must(t, html, "'grp.create':'Создать группу'", "russian create-group label")
	must(t, html, "'grp.create':'Створити групу'", "ukrainian create-group label")
	for _, id := range []string{"g_folder", "ogroup", "lgroup", "ngroup", "sgroup"} {
		must(t, html, "groupFieldBlock('"+id+"'", id+" uses the shared group field")
	}
}

// TestFlowsEPCDefault — flows columns default to EPC, the summary chip is revenue per click, and the uEPC cell formula stays rev/uniq.
func TestFlowsEPCDefault(t *testing.T) {
	html := spaSource()
	cat := spaFunctionLine(html, "colCatalog")
	sum := spaFunctionLine(html, "paintMetricSum")
	cell := spaFunctionLine(html, "flowMetricCell")
	if cat == "" || sum == "" || cell == "" {
		t.Fatal("colCatalog, paintMetricSum or flowMetricCell not found in SPA")
	}
	must(t, cat, "['epc','col.epc',1]", "flows catalog turns EPC on")
	must(t, cat, "['uepc','col.uepc',0]", "flows catalog turns uEPC off")
	must(t, sum, "tr('col.epc')", "summary chip is EPC")
	must(t, sum, "acc.rev/acc.hits", "summary EPC is revenue per click")
	mustNot(t, sum, "tr('col.uepc')", "summary chip is not uEPC")
	must(t, cell, "cm.rev/cm.uniq", "uEPC cell formula stays revenue per unique")
	must(t, cell, "cm.rev/cm.hits", "EPC cell formula stays revenue per click")
	must(t, html, "function migrateFlowCols(", "saved flow columns migrate")
	cols := spaFunctionLine(html, "loadCols")
	if cols == "" {
		t.Fatal("loadCols not found in SPA")
	}
	must(t, html, "const FLOW_COLS_MIGRATED='emtds-flow-cols-epc'", "one-time migration flag")
	must(t, cols, "FLOW_COLS_MIGRATED", "migration runs once per browser")
}

// TestSpendShareByOverlap — tiles and tables take the share of a spend record that overlaps the period.
func TestSpendShareByOverlap(t *testing.T) {
	html := spaSource()
	must(t, html, "function spendShare(", "period overlap share of a spend record")
	mustNot(t, html, "function spendOpen(", "all-or-nothing spend window")
	share := spaFunctionLine(html, "spendShare")
	fold := spaFunctionLine(html, "foldCost")
	camp := spaFunctionLine(html, "campaignCostOnly")
	period := spaFunctionLine(html, "periodSpendBy")
	if share == "" || fold == "" || camp == "" || period == "" {
		t.Fatal("spendShare, foldCost, campaignCostOnly or periodSpendBy not found in SPA")
	}
	must(t, share, "overlapShare(", "spendShare uses overlapShare")
	must(t, fold, "spendShare(e)", "foldCost takes the overlap share")
	must(t, fold, "*share", "foldCost multiplies cents by the share")
	must(t, camp, "*share", "campaign cost multiplies cents by the share")
	must(t, period, "*share", "period spend multiplies cents by the share")
}

// TestClickCostInSpend — click prices join manual spend, and the new labels exist in every language.
func TestClickCostInSpend(t *testing.T) {
	html := spaSource()
	must(t, html, "function clickCostBy(", "click prices fold into spend")
	period := spaFunctionLine(html, "periodSpendBy")
	camp := spaFunctionLine(html, "campaignCostOnly")
	fold := spaFunctionLine(html, "foldCost")
	chart := spaFunctionLine(html, "annotateChart")
	if period == "" || camp == "" || fold == "" || chart == "" {
		t.Fatal("periodSpendBy, campaignCostOnly, foldCost or annotateChart not found")
	}
	must(t, period, "clickCostBy(", "period spend includes click prices")
	must(t, camp, "clickCostBy(", "campaign cost includes click prices")
	must(t, fold, "clickCostBy(", "folded cost includes click prices")
	must(t, chart, "p.cost", "chart buckets add point cost")
	must(t, html, "'money.nocost':'No cost recorded: set the price parameter on the campaign or add a cost entry'", "english no-cost hint")
	must(t, html, "'money.nocost':'Расход не записан: задайте параметр цены в кампании или внесите расход'", "russian no-cost hint")
	must(t, html, "'money.nocost':'Витрату не записано: задайте параметр ціни в кампанії або внесіть витрату'", "ukrainian no-cost hint")
	mustNot(t, html, "'money.nocur'", "there is no no-currency label")
	must(t, html, "'col.rej.hint':'Postbacks with status rejected'", "english rejected hint")
	must(t, html, "'col.rej.hint':'Постбеки со статусом rejected'", "russian rejected hint")
	must(t, html, "'col.rej.hint':'Постбеки зі статусом rejected'", "ukrainian rejected hint")
}

// TestCatalogNames — breakdowns, the report and conversions show catalog names, and an open campaign refetches its rows.
func TestCatalogNames(t *testing.T) {
	html := spaSource()
	bd := spaFunctionLine(html, "loadBreakdown")
	rep := spaFunctionLine(html, "paintReport")
	conv := spaFunctionLine(html, "conversions")
	pb := spaFunctionLine(html, "loadPostbacks")
	one := spaFunctionLine(html, "loadPerfFor")
	page := spaFunctionLine(html, "flowPage")
	if bd == "" || rep == "" || conv == "" || pb == "" || one == "" || page == "" {
		t.Fatal("loadBreakdown, paintReport, conversions, loadPostbacks, loadPerfFor or flowPage missing")
	}
	must(t, bd, "idLabel(", "breakdown names catalog ids")
	must(t, bd, "ensureCatalogs(", "breakdown loads catalogs")
	must(t, bd, "countryLabel(", "breakdown names a country")
	cell := spaFunctionLine(html, "reportCellText")
	must(t, cell, "idLabel('offer_id'", "report offer name")
	must(t, cell, "idLabel('source_id'", "report source name")
	must(t, rep, "reportCellText(", "report table and export share the cell text")
	must(t, rep, "loadCols('report')", "report export uses the visible columns")
	mustNot(t, rep, "r.group,r.stream,r.country", "report export does not dump raw ids")
	must(t, spaFunctionLine(html, "entryCampaignConds"), "f:'campaign_id'", "the campaign button filters by campaign id")
	mustNot(t, conv, "f:'campaign',", "conversion filter is not the campaign name")
	must(t, conv, "entryConversionConds(arg, hiddenFields(ME))", "the campaign address becomes a visible conversion filter")
	mustNot(t, spaFunctionLine(html, "pbCondsForQuery"), "campaign_id", "the conversion query adds no hidden campaign condition")
	mustNot(t, spaFunctionLine(html, "logCondsForQuery"), "campaign_id", "the click query adds no hidden campaign condition")
	must(t, pb, "dockTotal(r", "conversion counter uses the filtered count")
	must(t, pb, "typeof r.total==='number'", "missing profit total is not drawn as zero")
	pbPage := spaFunctionLine(html, "paintPbPage")
	must(t, pbPage, "paintServerPager(", "conversion page uses shared pager")
	must(t, pb, "paintPbDock(", "conversion dock totals")
	must(t, conv, "dockHTML('pb','pb-export')", "conversion dock markup")
	dock := spaFunctionLine(html, "dockHTML")
	must(t, dock, `id="${prefix}sum"`, "dock summary row id")
	must(t, dock, `id="${prefix}page"`, "dock pager id")
	mustNot(t, pb, "pb-more", "conversion log has no show-more button")
	must(t, conv, "has-pager", "conversion log pins the pager")
	must(t, conv, "dockHTML('pb','pb-export')", "conversion pager in dock helper")
	must(t, one, "campaign_id", "open campaign refetches its performance")
	must(t, page, "loadPerfFor(", "flow page loads that campaign before drawing")
	must(t, html, "#/flows/", "campaign address stays a slug path")
}

// TestHeaderSignals runs the header marks under node against fixtures of GET /api/system/signals: a newer commit paints a
// yellow dot on the avatar, a failed service a red one that wins, the failure window opens once per set of failed services
// in a tab and again after a recovery, a failed read keeps the last mark, and the poll goes on.
func TestHeaderSignals(t *testing.T) {
	html := spaSource()
	must(t, html, `<span class="avatar">${esc((user||'a')[0])}</span><span class="dot sigdot" id="sigdot" hidden></span>`, "the dot sits on the avatar")
	must(t, spaFunctionLine(html, "adminMenuEntries"), "...(t.id==='about'?sigMenuMark():{})", "the about entry reads the same mark")
	must(t, spaFunctionLine(html, "app"), "sigStart();", "the poll starts with the app")
	must(t, spaFunctionLine(html, "sigShow"), "go('settings/about')", "the window leads to About")
	consts := regexp.MustCompile(`(?m)^const SIG(?:_EVERY)?=.*$`).FindAllString(html, -1)
	if len(consts) != 2 {
		t.Fatalf("found %d signal constants, want 2", len(consts))
	}
	prelude := strings.Join(consts, "\n") + `
const tr = k => k, trFill = (k, m) => k + ':' + m.list;
const mk = () => { const el = {hidden:true, title:'', cls:{}, removeAttribute(k){ el[k] = ''; }}; el.classList = {toggle:(c, on) => { el.cls[c] = !!on; }}; return el; };
const dot = mk(), btn = mk();
const q = sel => ({'#sigdot':dot, '#userbtn':btn})[sel] || null;
const store = {};
var sessionStorage = {getItem: k => k in store ? store[k] : null, setItem: (k, v) => { store[k] = String(v); }, removeItem: k => { delete store[k]; }};
let answer = null, timers = [], shown = [];
const call = async () => { if (answer instanceof Error) throw answer; return answer; };
const setTimeout = (f, ms) => { timers.push(ms); return timers.length; };
function sigShow(down){ shown.push(down.slice()); }
`
	assistantNode(t, []string{"sigMark", "sigNames", "sigPaint", "sigShouldShow", "sigTick"}, prelude, `
const tick = async (a) => { answer = a; await sigTick(SIG.gen); };
await tick({update:false, down:[]});
eq([dot.hidden, shown.length, timers], [true, 0, [60000]], 'no marks: no dot, no window, the next read in a minute');
await tick({update:true, down:[]});
eq([dot.hidden, dot.cls.warn, dot.cls.bad, btn.title], [false, true, false, 'sig.update'], 'a newer commit: a yellow dot with the reason');
await tick({update:true, down:['redis', 'rogue']});
eq([dot.cls.bad, dot.cls.warn, SIG.data.down, shown, btn.title], [true, false, ['redis'], [['redis']], 'sig.down.text:sig.svc.redis'], 'a failure: a red dot and one window, unknown names dropped');
await tick({update:true, down:['redis']});
eq(shown.length, 1, 'the same failure does not open the window again');
await tick(new Error('offline'));
eq([dot.cls.bad, shown.length], [true, 1], 'a failed read keeps the last mark');
await tick({update:false, down:['engine', 'redis']});
eq(shown, [['redis'], ['engine', 'redis']], 'a new set of failed services opens the window');
await tick({update:false, down:[]});
eq([dot.hidden, btn.title, 'emtds-sig-down' in store], [true, '', false], 'a recovery clears the dot and forgets the failure');
await tick({update:false, down:['redis']});
eq(shown.length, 3, 'the next failure after a recovery is told again');
eq(timers.length, 8, 'every read schedules the next one');
`)
}

// TestUpdateBannerAndButton runs the "new version" banner and the update start under node: a hide until the next version
// lasts while the target stays and ends with a newer one, a hide for 24 hours ends with its time, the banner and the
// button of "Settings → About" start the same request, and a blocked update gives its reason without a dialog.
func TestUpdateBannerAndButton(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "sysNotice"), "updApplyStart(updOpenAbout)", "the banner starts the update")
	must(t, spaFunctionLine(html, "aboutPage"), "applyBtn.onclick=()=>updApplyStart(updOpenAbout)", "About starts the same update")
	must(t, spaFunctionLine(html, "aboutPage"), "updCardHTML('about-upd',tr('about.update'),btnsHtml,`${whyHtml}${progHtml}${agentHtml}${checkHtml}${stateHtml}`)", "the buttons stand in the title of the update block")
	consts := regexp.MustCompile(`(?m)^const UPD_(?:HIDE|TRACK)_KEY=.*$`).FindAllString(html, -1)
	if len(consts) != 2 {
		t.Fatalf("found %d banner and progress constants, want 2", len(consts))
	}
	prelude := strings.Join(consts, "\n") + `
const tr = k => k;
const esc = s => String(s);
const store = {}, sess = {};
var localStorage = {getItem: k => k in store ? store[k] : null, setItem: (k, v) => { store[k] = String(v); }};
var sessionStorage = {getItem: k => k in sess ? sess[k] : null, setItem: (k, v) => { sess[k] = String(v); }, removeItem: k => { delete sess[k]; }};
let upd = null, toasts = [], modals = [], posts = [], reply = {id: 'aaaa0000bbbb1111'};
const get = async () => upd;
const toast = (m, bad) => { toasts.push(m); };
const q = () => ({value: 'abcdef1'});
const call = async (m, p, b) => { posts.push([m, p, b]); if (reply instanceof Error) throw reply; return reply; };
const formModal = (title, html, ok) => { modals.push(ok); };
`
	assistantNode(t, []string{"updBannerHidden", "updBannerHide", "updErrText", "updTrackGet", "updTrackSet", "updApplyStart"}, prelude, `
const now = 1000;
eq(updBannerHidden('abc123def456', now), false, 'nothing hidden at first');
updBannerHide('abc123def456', 0);
eq([updBannerHidden('abc123def456', now + 9e12), updBannerHidden('fff000fff000', now)], [true, false], 'until the next version: hidden for the target, shown for a newer one');
updBannerHide('abc123def456', now + UPD_HIDE_DAY);
eq([updBannerHidden('abc123def456', now + UPD_HIDE_DAY - 1), updBannerHidden('abc123def456', now + UPD_HIDE_DAY)], [true, false], 'for 24 hours: shown again after its time');
store[UPD_HIDE_KEY] = '{bad';
eq(updBannerHidden('abc123def456', now), false, 'a broken record shows the banner');
upd = {can_apply: false, apply_block: 'keitaro_running'};
await updApplyStart(() => {});
eq([toasts, modals.length], [['upd.block.keitaro_running'], 0], 'a running import blocks with its reason and no dialog');
upd = {can_apply: true, state: {id: 'old0old0old0old0', result: 'ok'}, check: {target: {commit: 'abcdef1234567890abcdef1234567890abcdef12'}}};
let after = 0;
await updApplyStart(() => { after++; });
eq(modals.length, 1, 'an allowed update asks to confirm');
eq(await modals[0](), 'stay', 'the dialog stays on the page');
eq([posts, after], [[['POST', '/api/update/apply', {target: 'abcdef1234567890abcdef1234567890abcdef12', confirm: 'abcdef1'}]], 1], 'one request into the inbox, then the page refresh');
eq(updTrackGet(), {id: 'aaaa0000bbbb1111', prev: 'old0old0old0old0'}, 'the accepted request is followed by its id');
updTrackSet(null);
const busy = new Error('409'); busy.body = {error: 'request_pending'}; reply = busy;
await modals[0]();
eq([updTrackGet(), after], [{id: '', prev: 'old0old0old0old0'}, 2], 'a request that raced in is followed instead of an error');
updTrackSet(null); toasts = []; posts = [];
upd = {can_apply: false, apply_block: 'request_pending', request_pending: true, state: {id: 'old0old0old0old0', result: 'ok'}};
await updApplyStart(() => { after++; });
eq([toasts, posts.length, modals.length, after, updTrackGet()], [[], 0, 1, 3, {id: '', prev: 'old0old0old0old0'}], 'an open request shows its progress, not a toast');
upd = {can_apply: false, apply_block: 'running', state: {id: 'run0run0run0run0', result: 'running'}};
await updApplyStart(() => { after++; });
eq([toasts, after, updTrackGet()], [[], 4, {id: '', prev: ''}], 'a running update shows its progress');
`)
}

// TestUpdateProgressSteps runs the progress of an update request under node: waiting in the inbox, picked by the agent,
// running with the phases of state.json, done or failed with its reason, a refusal tied by the request id, and the
// restart notice while the admin panel does not answer.
func TestUpdateProgressSteps(t *testing.T) {
	prelude := `
const tr = k => k;
const trFill = (k, v) => k + JSON.stringify(v);
const esc = s => String(s).replace(/</g, '&lt;');
const yes = (ok, what) => { if (!ok) fail(what); };
`
	assistantNode(t, []string{"updProgress", "updProgressHTML"}, prelude, `
const track = {id: 'req0req0req0req0', prev: 'old0old0old0old0'};
const old = {id: 'old0old0old0old0', result: 'ok', finished_at: 'x'};
eq(updProgress({request_pending: true, state: old}, track).step, 'queued', 'a request in the inbox waits for the agent');
eq(updProgress({request_pending: false, state: old}, track).step, 'picked', 'the old report means the agent is still checking the channel');
const run = {id: 'run0run0run0run0', result: 'running', phase: 'build', phases: [{name: 'env', ok: true}, {name: 'backup', ok: true}]};
const p = updProgress({state: run}, track);
eq(p.step, 'running', 'a new report is the run of the request');
const h = updProgressHTML(p, false);
yes(h.includes('upd.prog.accepted') && h.includes('upd.prog.agent') && h.includes('upd.prog.apply'), 'the three steps are drawn');
yes(h.includes('<li class="upd-step active"><span class="dot warn"></span><span>build</span>') && h.includes('<span>backup</span>'), 'the phases with the current one');
yes(!h.includes('upd-prog-close'), 'nothing to close while the run goes');
const ok = updProgressHTML(updProgress({state: {id: 'run0run0run0run0', result: 'ok', finished_at: 'x', from: 'aaaaaaa111', to: 'bbbbbbb222', phases: []}}, track), false);
yes(ok.includes('upd.prog.done{"from":"aaaaaaa","to":"bbbbbbb"}') && ok.includes('upd-prog-close'), 'done with from and to');
const bad = updProgressHTML(updProgress({state: {id: 'run0run0run0run0', result: 'failed', reason: 'keitaro <x>', finished_at: 'x', phases: [{name: 'keitaro_gate', ok: false}]}}, track), false);
yes(bad.includes('upd.prog.fail{"result":"upd.result.failed"}: keitaro &lt;x>') && bad.includes('upd-step fail'), 'a failure with its reason');
eq(updProgress({state: {id: 'req0req0req0req0', result: 'refused', finished_at: 'x'}}, {id: 'req0req0req0req0', prev: 'req0req0req0req0'}).step, 'failed', 'a refusal carries the request id');
yes(updProgressHTML(p, true).includes('upd.prog.offline'), 'the restart is told while the panel does not answer');
`)
}

// TestPeriodButtonShowsLabel checks the period button of the header: the label is written on the button next to the calendar icon,
// it hides only on the nav-short step that fitNav takes after the nav labels, and the period stays in title and aria-label.
func TestPeriodButtonShowsLabel(t *testing.T) {
	html := spaSource()
	must(t, html, `<button class="ghost" id="period" type="button">${icon('calendar')}<span id="period-text"></span></button>`, "the label sits on the button")
	must(t, html, `.top.nav-short #period-text,`, "the label hides only on the nav-short step")
	mustNot(t, html, `.top.nav-icons #period-text`, "icon-only nav keeps the label")
	must(t, html, `#period-text{white-space:nowrap}`, "the label never wraps the header")
	must(t, spaFunctionLine(html, "paintChrome"), "pt.textContent=lab; b.title=lab; b.setAttribute('aria-label',lab);", "the label, title and aria-label are the period")
	fit := spaFunctionLine(html, "fitNav")
	must(t, fit, "['nav-icons','nav-short','nav-fold']", "the label goes after the nav labels and before the burger")
}

// TestNavExportAndCampaignTable — icon nav, HTML export, period label, campaign table.
func TestNavExportAndCampaignTable(t *testing.T) {
	html := spaSource()
	must(t, html, "nav-icons", "icon-first navigation")
	fit := spaFunctionLine(html, "fitNav")
	iIcons := strings.Index(fit, "nav-icons")
	iFold := strings.Index(fit, "nav-fold")
	if iIcons < 0 || iFold < 0 || iIcons >= iFold {
		t.Errorf("fitNav must apply nav-icons before nav-fold (icons=%d fold=%d)", iIcons, iFold)
	}
	must(t, html, "margin-left:15px", "submenu indent")
	mustNot(t, html, "margin-left:-15px", "submenu negative margin")
	must(t, html, `id="period-text"`, "period label span")
	must(t, html, "text/html", "html export")
	if !flowsTitleNearHTML(html) {
		t.Errorf("flows.title is not next to .html")
	}
	must(t, html, `.ftable input[type=checkbox]`, "campaign table checkbox")
	set := spaFunctionLine(html, "settings")
	must(t, set, "nav.card", "nav order card")
	must(t, set, "saveNav(", "settings saves the nav")
	if !strings.Contains(html, "IC.calendar") && !strings.Contains(html, "calendar:'") {
		t.Errorf("calendar icon missing")
	}
}

func flowsTitleNearHTML(html string) bool {
	start := 0
	for {
		i := strings.Index(html[start:], "flows.title")
		if i < 0 {
			return false
		}
		i += start
		lo := i - 40
		if lo < 0 {
			lo = 0
		}
		hi := i + len("flows.title") + 40
		if hi > len(html) {
			hi = len(html)
		}
		if strings.Contains(html[lo:hi], ".html") {
			return true
		}
		start = i + len("flows.title")
	}
}

// TestFlowsHeaderExportMenu checks that Create follows the column gear and the search field, filters sit in the bar, Export sits in the dock after the pager, and the menu can open above its anchor.
func TestFlowsHeaderExportMenu(t *testing.T) {
	html := spaSource()
	flows := spaFunctionLine(html, "flows")
	if flows == "" {
		t.Fatal("flows missing")
	}
	must(t, flows, "dockHTML('flow','flowexport')", "flows dock with export")
	must(t, flows, "exportFlows(false)", "CSV export action")
	must(t, flows, "exportFlows(true)", "HTML export action")
	must(t, flows, "createBtn('newflow')", "create button from the shared helper")
	createFn := spaFunctionLine(html, "createBtn")
	must(t, createFn, "tr('Create')", "create button label")
	must(t, createFn, `aria-label="${label}"`, "create button keeps its accessible name")
	must(t, createFn, `class="primary mk"`, "create button class")
	must(t, html, `@media(max-width:720px){button.mk{width:32px;padding:0}button.mk .mk-t{display:none}}`, "create shows only the plus on a narrow bar")
	for _, id := range []string{"land-new", "net-new", "src-new", "off-new", "dom-new"} {
		must(t, html, "createBtn('"+id+"')", "catalog create "+id)
	}
	must(t, flows, `id="fbar"`, "filter bar")
	must(t, flows, `id="fsource"`, "source filter")
	must(t, flows, `id="ffolder"`, "folder filter")
	must(t, flows, `id="fstate"`, "state filter")
	mustNot(t, flows, `id="flowcsv"`, "separate CSV export button")
	mustNot(t, flows, `id="flowhtml"`, "separate HTML export button")
	fbar := spaBetween(flows, `id="fbar"`, `id="fgrid"`)
	must(t, fbar, `createBtn('newflow')`, "create in filter bar")
	must(t, fbar, `id="fq"`, "search in filter bar")
	must(t, flows, `class="toolbar bar-fit" id="fbar"`, "filter bar sizes to the row")
	mustNot(t, fbar, `width:260px`, "search is not a fixed 260px")
	must(t, html, `.toolbar.bar-fit .rgt select{width:auto;max-width:min(220px,100%);field-sizing:content`, "filters follow the selected label")
	must(t, html, `.toolbar.bar-fit .lft{flex:1 1 auto;flex-wrap:nowrap;min-width:0}`, "search stays beside create")
	must(t, html, `.toolbar.bar-fit .lft{flex:1 1 100%}`, "search takes the row on a narrow bar")
	must(t, html, `.toolbar.bar-fit .rgt{flex:0 1 auto;flex-wrap:nowrap;min-width:0`, "filters and refresh stay on one row")
	must(t, html, `.toolbar.bar-fit .rgt>button{flex:none}`, "refresh keeps its size")
	must(t, html, `.toolbar.bar-fit .rgt>.tags.pk,.toolbar.bar-fit .rgt>select:not(.pk-native){flex:1 1 calc(50% - 25px)`, "narrow bar leaves room for refresh beside the last filter")
	must(t, html, `.toolbar.bar-fit{--bar-filter-w:140px}`, "one width token for the group and state filters")
	must(t, html, `.toolbar.bar-fit .rgt>.tags.pk,.toolbar.bar-fit .rgt>select:not(.pk-native){flex:0 1 auto;width:var(--bar-filter-w);min-width:0}`, "group and state filters share one width")
	must(t, html, `.toolbar.bar-fit .lft .sfld{flex:1 1 160px}`, "search takes the rest of the row and may shrink")
	mustNot(t, html, `.toolbar.bar-fit .rgt{flex:0 0 420px`, "filter slot is not a fixed width")
	mustNot(t, html, `.toolbar.bar-fit{flex-wrap:nowrap}`, "filters wrap as one group")
	cat := spaFunctionLine(html, "catalogBar")
	must(t, cat, `class="toolbar bar-fit"`, "catalog bar uses the same fit")
	mustNot(t, cat, `width:260px`, "catalog search is not a fixed 260px")
	nw := strings.Index(fbar, `createBtn('newflow')`)
	fq := strings.Index(fbar, `id="fq"`)
	if nw < 0 || fq < 0 || nw >= fq {
		t.Errorf("filter bar order create < search: new=%d search=%d", nw, fq)
	}
	ph := spaBetween(flows, `<div class="ph">`, `</div>
    <div class="toolbar bar-fit" id="fbar">`)
	mustNot(t, ph, `newflow`, "create not in page header")
	mustNot(t, ph, `id="fq"`, "search not in page header")
	dockFn := spaFunctionLine(html, "dockHTML")
	page := strings.Index(dockFn, `id="${prefix}page"`)
	exp := strings.Index(dockFn, `id="${exp}"`)
	if page < 0 || exp < 0 || exp <= page {
		t.Errorf("export must follow the pager in dockHTML: page=%d export=%d", page, exp)
	}
	cards := spaFunctionLine(html, "paintFlowCards")
	must(t, cards, `e.shiftKey`, "campaign checkbox reads Shift on click")
	must(t, cards, `pickSpan(FLOW_PICK, FLOW_PICK_MEM, page.map(g=>g.id)`, "range uses the visible page")
	mustNot(t, cards, `pickSpan(FLOW_PICK, FLOW_PICK_MEM, rows`, "range does not take every filtered row")
	must(t, cards, `id==='*'`, "header checkbox stays select-all")
	span := spaFunctionLine(html, "pickSpan")
	must(t, span, `list.slice(lo, hi+1)`, "span is the inclusive visible slice")
	must(t, span, `mem.span=on?next:[]`, "a later Shift drops ids that left the span")
	doms := spaFunctionLine(html, "domains")
	must(t, doms, `pickSpan(DOM_PICK, DOM_PICK_MEM, list.map(d=>String(d.id))`, "domain range uses the visible page")
	must(t, doms, `bulkAct('dbulk-check','check'`, "domain bulk check icon")
	must(t, doms, `bulkAct('dbulk-group','lists'`, "domain bulk group icon")
	must(t, cards, `bulkAct('bulk-on','play'`, "bulk enable icon")
	must(t, cards, `bulkAct('bulk-off','pause'`, "bulk disable icon")
	must(t, cards, `bulkAct('bulk-del','trash'`, "bulk delete icon")
	must(t, html, `.bulkbar button.bulk-act:active .bulk-txt`, "press shows the action label")
	mustNot(t, cards, `class="sm" id="bulk-`, "small class on a bulk action")
	mustNot(t, cards, `class="sm danger" id="bulk-del"`, "small danger class on bulk delete")
	must(t, html, `.ftable td.fname{font-size:11px}`, "name cell is 11px")
	cell := spaFunctionLine(html, "flowMetricCell")
	must(t, cell, `class="ell fname"`, "name cell class")
	menuPlaceFn := spaFunctionLine(html, "menuPlace")
	must(t, menuPlaceFn, "r.top-6-h", "menu can open above its anchor")
	must(t, menuPlaceFn, "includes('end')", "menu end alignment")
	must(t, menuPlaceFn, "popLeft(r, w, end)", "menu uses the window clamp")
	must(t, spaFunctionLine(html, "viewBox"), "clientWidth", "menu stays inside the visible window")
	must(t, spaFunctionLine(html, "popLeft"), "r.right-w", "menu at the right edge aligns to its button")
	mustNot(t, menuPlaceFn, `q('main')`, "menu is not tied to the content column")
	must(t, spaFunctionLine(html, "ensureMenuResize"), "closeMenu()", "scroll closes the menu")
	must(t, spaFunctionLine(html, "ensureMenuResize"), "document.addEventListener('scroll'", "scroll listener is capture on the document")
	must(t, html, ".menu.end{min-width:0}", "export menu css")
	must(t, flows, "menu(e.currentTarget,[{label:tr('Export CSV')", "export menu anchor")
	must(t, flows, "exportFlows(true)}], 'end')", "export menu end class")
}

func TestCloneStreams(t *testing.T) {
	html := spaSource()
	menu := spaFunctionLine(html, "streamMenu")
	must(t, menu, "openStreamDest(i,'clone')", "stream menu clones through a dialog")
	must(t, menu, "openStreamDest(i,'move')", "stream menu moves through a dialog")
	mustNot(t, menu, "others.map", "stream menu does not list every campaign")
	dlg := spaFunctionLine(html, "openStreamDest")
	must(t, dlg, "To another campaign", "clone dialog can target another campaign")
	must(t, dlg, "Choose a flow…", "campaign field placeholder")
	must(t, dlg, "copyStreamInto(s,target,null)", "clone into the chosen campaign appends a copy")
	must(t, dlg, "copyStreamInto(s,g,i)", "clone in place inserts under the stream")
	must(t, dlg, "g.streams.splice(i,1)", "move removes the stream from the current campaign")
	copyFn := spaFunctionLine(html, "copyStreamInto")
	must(t, copyFn, "clone(s)", "copy does not share the stream object")
	mustNot(t, copyFn, "splice(i,1)", "copy leaves the source stream")
	must(t, html, `'Clone streams':'Клонировать потоки'`, "russian clone title")
	must(t, html, `'To another campaign':'В другую кампанию'`, "russian other-campaign checkbox")
}

// TestSpaPureAndConvMarkers checks sort helpers, conversion labels, dates, and country data markers.
func TestSpaPureAndConvMarkers(t *testing.T) {
	html := spaSource()
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	must(t, pure, "function nameSortKey(", "nameSortKey in pure block")
	must(t, pure, "function capFirst(", "capFirst in pure block")
	rows := spaFunctionLine(html, "campaignRows")
	must(t, pure, "function compareNames(", "compareNames in pure block")
	must(t, pure, "function compareNameKeys(", "compareNameKeys in pure block")
	must(t, pure, "function nameKey(", "nameKey in pure block")
	must(t, rows, "nameKey(campaignLabel(g))", "campaign name keys are computed once per row")
	must(t, rows, "compareNameKeys(keys.get(a),keys.get(b),dir)", "campaign name sort uses the ready keys")
	must(t, spaFunctionLine(html, "applySort"), "compareNameKeys(nameKeys.get(a),nameKeys.get(b),dir)", "table name sort uses the ready keys")
	must(t, spaFunctionLine(html, "when"), "year:'numeric'", "when includes year")
	conv := spaFunctionLine(html, "convCell")
	must(t, conv, "tr('conv.lead')", "lead pill label")
	must(t, conv, "tr('conv.sale')", "sale pill label")
	must(t, html, "'zm|", "zambia in country pack")
	must(t, html, "'ci|", "ivory coast in country pack")
	must(t, html, "'ph|", "philippines in country pack")
	must(t, html, "/assets/flags/", "country flags are embedded svg files")
	mustNot(t, html, "FLAG_SPEC", "inline flag spec is gone")
	must(t, spaFunctionLine(html, "syncSaveBar"), "capFirst(tr('flows'))", "save bar capitalizes flows")
}

// TestFlowsFilterBar checks that campaign rows filter by source and folder and that the filter helpers exist.
func TestFlowsFilterBar(t *testing.T) {
	html := spaSource()
	rows := spaFunctionLine(html, "campaignRows")
	must(t, rows, "FLOW_SOURCE", "source predicate")
	must(t, rows, "FLOW_FOLDER", "folder predicate")
	must(t, rows, "g.folder", "folder text search")
	must(t, html, "function fillFlowFilters(", "filter option builder")
	must(t, html, "function campaignFolders(", "folder list")
	cards := spaFunctionLine(html, "paintFlowCards")
	must(t, cards, "const filtered=", "empty state follows the active filters")
	must(t, cards, "fillFlowFilters()", "repaint refreshes the filter bar")
}

// TestKeitaroPage — the user menu offers Keitaro import between postbacks and
// settings, the page calls the import endpoints, and it polls the background job.
// Losses are grouped, and the redirect type list includes show_html_404.
func TestKeitaroPage(t *testing.T) {
	html := spaSource()
	menuLine := spaFunctionLine(html, "userMenuItems")
	if menuLine == "" {
		t.Fatal("user menu missing")
	}
	must(t, menuLine, "adminMenuEntries()", "user menu takes its entries from the settings list")
	list := spaBetween(html, "const SETTINGS_TABS=[", "\n];")
	setAt := strings.Index(list, "id:'profile'")
	usersAt := strings.Index(list, "id:'users'")
	intAt := strings.Index(list, "id:'integrations'")
	keysAt := strings.Index(list, "id:'keys'")
	boardsAt := strings.Index(list, "id:'boards'")
	pbAt := strings.Index(list, "id:'postbacks'")
	aboutAt := strings.Index(list, "id:'about'")
	if setAt < 0 || setAt >= usersAt || usersAt >= intAt || intAt >= keysAt || keysAt >= boardsAt || boardsAt >= pbAt || pbAt >= aboutAt {
		t.Errorf("user menu order: settings, users, integrations, API keys, section settings, postbacks, about")
	}
	if got := strings.Count(list[boardsAt:aboutAt], "'-',"); got != 2 {
		t.Errorf("want a separator before postbacks and before about, got %d", got)
	}
	if !strings.Contains(list[boardsAt:pbAt], "'-',") || !strings.Contains(list[pbAt:aboutAt], "'-',") {
		t.Errorf("separators must sit before postbacks and before about")
	}
	mustNot(t, menuLine, `tr('menu.keitaro')`, "user menu uses integrations, not keitaro")
	src := spaFunctionLine(html, "keitaroPanel")
	if src == "" {
		t.Fatal("keitaro panel missing")
	}
	mustIntegration(t, html, "keitaro", "keitaroPanel")
	must(t, html, "keitaro:integrations", "the old #/keitaro route opens the integrations page")
	for _, needle := range []string{
		"/api/keitaro/status",
		"/api/keitaro/check",
		"/api/keitaro/import",
		"/api/keitaro/purge",
		"/api/keitaro/job",
		`id="kt-progress"`,
		`id="kt-import" hidden`,
		`data-kttab="kt-import"`,
		"ktJobPaint(box, job, true)",
		`id="kt-campaigns"`,
		`id="kt-conversions"`,
		`id="kt-from"`,
		"kt.col.count",
		`id:'kt-api'`,
		`tr('kt.api')`,
	} {
		must(t, src, needle, "keitaro page")
	}
	mustNot(t, src, "Admin API v1", "the api hint is only the translation")
	mustNot(t, src, "textContent='kt.api'", "the translation key is not the hint text")
	typesAt := strings.Index(html, "const TYPES=")
	if typesAt < 0 {
		t.Fatal("TYPES missing")
	}
	typesEnd := strings.Index(html[typesAt:], "\nconst typeLabel=")
	if typesEnd < 0 {
		t.Fatal("TYPES block has no end")
	}
	must(t, html[typesAt:typesAt+typesEnd], "show_html_404", "TYPES includes 404 HTML")
	pagesAt := strings.Index(html, "const PAGES=")
	if pagesAt < 0 {
		t.Fatal("PAGES missing")
	}
	pagesEnd := strings.Index(html[pagesAt:], "\n")
	if pagesEnd < 0 {
		t.Fatal("PAGES line has no newline")
	}
	must(t, html[pagesAt:pagesAt+pagesEnd], "keitaro:integrations", "PAGES aliases keitaro to integrations")
	must(t, html[pagesAt:pagesAt+pagesEnd], "integrations", "PAGES registers integrations")
	for _, anchor := range []string{
		`'menu.settings':'Settings'`,
		`'menu.settings':'Настройки'`,
		`'menu.settings':'Налаштування'`,
	} {
		i := strings.Index(html, anchor)
		if i < 0 {
			t.Errorf("dictionary anchor missing: %s", anchor)
			continue
		}
		lineEnd := strings.Index(html[i:], "\n")
		if lineEnd < 0 {
			t.Errorf("dictionary line has no newline: %s", anchor)
			continue
		}
		if !strings.Contains(html[i:i+lineEnd], `'kt.title'`) {
			t.Errorf("dictionary missing kt.title next to %s", anchor)
		}
		if !strings.Contains(html[i:i+lineEnd], `'kt.stage.streams'`) {
			t.Errorf("dictionary missing kt.stage.streams next to %s", anchor)
		}
		if !strings.Contains(html[i:i+lineEnd], `'kt.c.pages'`) {
			t.Errorf("dictionary missing kt.c.pages next to %s", anchor)
		}
		if !strings.Contains(html[i:i+lineEnd], `'kt.api'`) {
			t.Errorf("dictionary missing kt.api next to %s", anchor)
		}
	}
}

// TestKeitaroProgressShared checks that the progress tab of the integrations page and the Keitaro window
// draw the run with one card function, follow one poll of GET /api/keitaro/job and keep no old progress markup.
func TestKeitaroProgressShared(t *testing.T) {
	html := spaSource()
	page := spaFunctionLine(html, "keitaroPanel")
	if page == "" {
		t.Fatal("keitaro panel missing")
	}
	paint := spaFunctionLine(html, "ktJobPaint")
	must(t, paint, "ntcSwap(box, ktDockCard(", "shared paint builds the window card")
	must(t, paint, "ktDockPercent(j)", "shared paint shows the counted percent")
	must(t, spaFunctionLine(html, "ktDockPaint"), "ktJobPaint(el, j)", "window uses the shared paint")
	must(t, page, "ktJobPaint(box, job, true)", "progress tab uses the shared paint")
	must(t, page, `<div id="kt-progress" hidden></div>`, "progress tab is only the card host")
	must(t, spaFunctionLine(html, "ntcHTML"), "ntBarHTML(o.bar)", "notice card draws the shared edge")
	bar := spaFunctionLine(html, "ntBarHTML")
	must(t, bar, `<div class="nt-bar run" aria-hidden="true"><i></i></div>`, "edge of unknown length runs")
	must(t, bar, "Math.max(0,Math.min(100,+pct||0))", "counted edge is clamped")
	swap := spaFunctionLine(html, "ntcSwap")
	must(t, swap, "bar.style.width=from", "bar starts from the last width")
	must(t, swap, "void bar.offsetWidth", "bar fills with a transition")
	must(t, html, ".nt-bar i{display:block;height:100%;width:0;background:var(--nt);transition:width .4s ease-out}", "edge reads the tone token and fills smoothly")
	must(t, html, ".nt-bar i{transition:none}", "reduced motion stops the fill transition")
	card := spaFunctionLine(html, "ktDockCard")
	must(t, card, "close:!inline", "page card has no close button")
	must(t, card, "cls:inline?'flat':''", "page card lies flat in the panel")

	follow := spaFunctionLine(html, "ktDockFollow")
	must(t, follow, "KT_DOCK.listen=fn||null", "page registers on the window poll")
	must(t, follow, "ktDockRun()", "following reads the job at once")
	must(t, spaFunctionLine(html, "ktDockTick"), "KT_DOCK.listen(job)", "window poll feeds the page")
	must(t, spaFunctionLine(html, "ktDockStop"), "KT_DOCK.listen=null", "logout forgets the page listener")
	must(t, page, "following=ktDockFollow(pollJob)", "page follows the window poll")
	mustNot(t, page, "setInterval(", "page has no poll of its own")
	if n := strings.Count(page, "get('/api/keitaro/job'"); n != 1 {
		t.Errorf("page reads the job %d times, want only the first read on open", n)
	}

	must(t, page, "calloutSet(q('#kt-job-note'), msg, 'err')", "failed run is an error callout")
	must(t, page, "tr('kt.dock.done'):'', 'ok')", "finished run is an ok callout")
	for _, legacy := range []string{"<progress", "kt-progress-line", `id="kt-bar"`, "progressLine", "kt-job-err", "ktDockKick"} {
		mustNot(t, html, legacy, "old progress markup")
	}
	mustNot(t, html, "progress{width:100%", "old progress style")
}

// TestBreakdownLabelsAndKeitaroDock checks that wide breakdown cards keep their labels
// and that the Keitaro progress window polls on every page with a stored position.
func TestBreakdownLabelsAndKeitaroDock(t *testing.T) {
	html := spaSource()
	wide := spaBetween(html, "@media(min-width:1181px){.bars .k{", "}}")
	if wide == "" {
		t.Fatal("wide .bars rule missing")
	}
	mustNot(t, wide, "min-width:0}", "wide .bars .k must keep a label width")
	must(t, wide, "min-width:68px", "wide .bars .k lower bound")
	must(t, wide, ".bars .tr{flex:1 1 140px", "wide bar shrinks instead of the label")

	must(t, html, `class="ktdock"`, "dock element")
	must(t, html, "const KT_DOCK_KEY='emtds-ktdock'", "dock position storage key")
	must(t, html, "const KT_DOCK_COUNTED=['streams','clicks','conversions']", "percent stages")
	tick := spaFunctionLine(html, "ktDockTick")
	must(t, tick, "get('/api/keitaro/job'", "dock polls the job")
	mustNot(t, spaFunctionLine(html, "keitaroPanel"), "async function ktDockTick", "dock poll lives outside keitaroPanel")
	pct := spaFunctionLine(html, "ktDockPercent")
	must(t, pct, "KT_DOCK_COUNTED.includes", "percent only for counted stages")
	must(t, pct, "total<=0", "percent needs a total")
	must(t, spaFunctionLine(html, "ktDockSaved"), "localStorage.getItem(KT_DOCK_KEY)", "dock restores its position")
	must(t, spaFunctionLine(html, "ktDockDrag"), "localStorage.setItem(KT_DOCK_KEY", "dock stores its position")
	must(t, spaFunctionLine(html, "ktDockDrag"), "setPointerCapture", "dock drags with pointer capture")
	must(t, spaFunctionLine(html, "ktDockStart"), "pageAllowed(me,'integrations')", "dock gated like the integrations menu")
	must(t, spaFunctionLine(html, "app"), "ktDockStart(ME)", "dock starts after login")
	must(t, spaFunctionLine(html, "loginView"), "ktDockStop()", "dock stops on logout")
	paint := spaFunctionLine(html, "ktDockPaint")
	card := spaFunctionLine(html, "ktDockCard")
	must(t, card, "tr('kt.stage.'+stage)", "dock stage name")
	must(t, card, "esc(msg)", "dock escapes the server error")
	must(t, paint, "KT_DOCK.seen", "dock hides runs finished before load")
}

// TestNoticeCard checks that toasts, the Keitaro window and dialogs share one notice component,
// that the window shows every job state from GET /api/keitaro/job and never starts an import,
// and that the notices stay accessible: live roles, Esc, no focus theft, auto-dismiss only for success.
func TestNoticeCard(t *testing.T) {
	html := spaSource()
	must(t, html, ".ntc{--nt:var(--info);--nt-soft:var(--info-soft)", "notice card reads the theme tokens")
	must(t, html, "@media (prefers-reduced-motion:reduce){.ntc.busy", "notice motion respects reduced motion")
	must(t, html, ".toasts{position:fixed;right:20px;bottom:calc(var(--edge-b,20px) + var(--dock-lift,0px) + var(--ai-lift,0px))", "toasts stack above the window, the dock and the assistant button")

	toast := spaFunctionLine(html, "toast")
	must(t, toast, "ntcHTML({tone:err?'bad':'ok'", "toast uses the notice card")
	must(t, toast, "el.setAttribute('role',err?'alert':'status')", "toast live role")
	must(t, toast, "if(!err){", "only a success toast leaves by itself")
	must(t, toast, "e.key==='Escape'", "Esc closes a toast")
	must(t, toast, "open.length>TOAST_MAX", "toast stack is capped")
	mustNot(t, toast, ".focus()", "toast never takes focus")

	card := spaFunctionLine(html, "ktDockCard")
	for _, part := range []string{"tone:'busy'", "tone:'bad'", "tone:'ok'", "ktDockFacts(j.counts)", "ktDockLosses(j.losses", "j.campaign_id", "when(j.finished_at)", "data-ktact=\"report\"", "data-ktact=\"hide\""} {
		must(t, card, part, "dock card state part")
	}
	must(t, spaFunctionLine(html, "ktDockSpan"), "j.finished_at", "run time ends at finished_at")
	must(t, spaFunctionLine(html, "ktDockLosses"), "trFill('kt.dock.warn'", "warnings are counted")
	act := spaFunctionLine(html, "ktDockAct")
	must(t, act, "go('integrations')", "report opens the integrations page")
	mustNot(t, act, "call(", "dock buttons never post an import or a purge")
	el := spaFunctionLine(html, "ktDockEl")
	must(t, el, "e.key==='Escape'", "Esc hides the window")
	mustNot(t, el, ".focus()", "window never takes focus")
	say := spaFunctionLine(html, "ktDockSay")
	must(t, say, "live.setAttribute('role', msg?'alert':'status')", "failed run is an alert")
	must(t, say, "live.setAttribute('aria-live', msg?'assertive':'polite')", "failed run interrupts, a running one is polite")
	must(t, say, "if(live.textContent!==text)", "the live line is spoken again only when its text changes")
	mustNot(t, say, "j.done", "counted progress is not spoken")
	mustNot(t, say, "ktDockPercent", "percent is not spoken")
	must(t, el, `<section class="ktdock" aria-label=`, "the window itself is not a live region")
	mustNot(t, el, `class="ktdock" role=`, "the window carries no role")
	must(t, el, `class="sr-only" data-ktlive role="status" aria-live="polite" aria-atomic="true"`, "only the title and stage line is live")
	must(t, spaFunctionLine(html, "ktDockPaint"), "ktDockSay(el, j, stage, msg)", "paint speaks through the live line")
	must(t, spaFunctionLine(html, "ktDockPaint"), "ktJobPaint(el, j)", "dock paints through the shared job card")
	must(t, spaFunctionLine(html, "ntcSwap"), "qa(':scope>.ntc',box).forEach(n=>n.remove())", "paint swaps only the card, the live line stays")
	must(t, html, ".sr-only{position:absolute;width:1px;height:1px", "visually hidden helper")
	must(t, html, `<div class="nt-bar run" aria-hidden="true">`, "running progress edge is decorative")
	must(t, html, `<div class="nt-bar" aria-hidden="true">`, "counted progress edge is decorative")
	must(t, html, `<div class="toasts" id="toasts" aria-live="polite" aria-relevant="additions">`, "toast stack is a polite live region")
	must(t, toast, "el.addEventListener('focusin',hold)", "focus inside a toast pauses its timer")
	must(t, toast, "el.addEventListener('focusout'", "leaving focus restarts the timer")
	must(t, toast, "el.addEventListener('mouseenter',hold)", "hover pauses the toast timer")
	must(t, toast, "el.matches(':focus-within')", "the pointer leaving keeps the timer paused while focus is inside")
	must(t, spaFunctionLine(html, "ask"), "ntcHead(title,danger?'bad':'')", "confirm dialog heading")
	must(t, spaFunctionLine(html, "connShow"), "ntcHead(", "connected domains dialog heading")
	for _, key := range []string{"kt.dock.hide", "kt.dock.report", "kt.dock.retry", "kt.dock.warn", "kt.dock.took", "kt.kind.conflict"} {
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("%s is in %d dictionaries, want en, ru and uk", key, n)
		}
	}
}

// TestStreamThen checks the Then mode tabs, share rows and shared S2S templates.
func TestStreamThen(t *testing.T) {
	html := spaSource()
	must(t, html, `.share-row`, "share row grid")
	must(t, html, `max="100"`, "share percent ceiling")
	must(t, html, `class="icon ghost" data-delshare`, "remove button on a share row")
	for _, key := range []string{"then.mode.offers", "then.mode.url", "then.mode.action"} {
		must(t, html, key, "then mode label")
	}
	must(t, html, `/api/s2s`, "shared postback catalog")
	must(t, spaFunctionLine(html, "collectFlow"), `postback_ids`, "campaign stores chosen templates")
	mustNot(t, html, `+'/postbacks'`, "campaign postback route is not used by the SPA")
	must(t, html, `class="types"`, "then modes keep the type tabs")
	must(t, html, `data-then=`, "then mode switch")
}

// TestSPAFlowCompact checks compact flow canvas toggle, i18n, breakdown label tooltips.
func TestSPAFlowCompact(t *testing.T) {
	html := spaSource()
	must(t, html, "FLOW_COMPACT_KEY", "flow compact localStorage key")
	must(t, html, "canvas-toggle", "compact canvas toggle button")
	must(t, html, "'flow.compact'", "flow.compact i18n")
	must(t, html, "flowCompactClass(", "flowCompactClass helper")
	must(t, html, `class="k" title="${esc(label)}"`, "breakdown title shows label")
}

// TestSPAFlowTrafficOrder checks the canvas order toggle: off, traffic both ways, then revenue both ways.
// The order is a page-view setting, and switching it never marks the campaign dirty or reaches
// the saved stream order. Opening a campaign leaves the button unpressed unless a saved view says otherwise.
func TestSPAFlowTrafficOrder(t *testing.T) {
	html := spaSource()
	must(t, html, `id="ftraffic"`, "traffic order button")
	must(t, html, "canvas-traffic", "traffic order button style")
	must(t, html, "${flowTrafficButton()}", "traffic order button on the canvas")
	must(t, html, "tb.onclick=toggleFlowTraffic", "traffic order button wired")
	must(t, html, `const FLOW_TRAFFIC_MODES=['','asc','desc','rev-asc','rev-desc']`, "order cycle is off, traffic, then revenue")
	must(t, html, "let FLOW_TRAFFIC=''", "canvas order starts off")
	for _, key := range []string{"flow.traffic.off", "flow.traffic.desc", "flow.traffic.asc", "flow.traffic.lock", "flow.revenue.asc", "flow.revenue.desc"} {
		if n := strings.Count(html, "'"+key+"':"); n != 3 {
			t.Errorf("%s: %d dictionary entries, want one per language", key, n)
		}
	}
	btn := spaFunctionLine(html, "flowTrafficButton")
	must(t, btn, `aria-pressed="${mode?'true':'false'}"`, "the button is pressed only while an order is on")
	must(t, btn, `data-traffic="${key}"`, "the button names the current mode")
	mustNot(t, btn, `data-traffic="asc"`, "the button is not stuck on fewest first")
	must(t, btn, "aria-label=", "the accessible name is the current mode")
	toggle := spaFunctionLine(html, "toggleFlowTraffic")
	must(t, toggle, "touchPageViews()", "traffic order kept in page views")
	for _, bad := range []string{"markDirty", "syncSaveBar", "moveStream", ".streams"} {
		mustNot(t, toggle, bad, "traffic order toggle touches the campaign: "+bad)
	}
	for _, name := range []string{"collectFlow", "moveStream", "markDirty"} {
		mustNot(t, spaFunctionLine(html, name), "FLOW_TRAFFIC", name+" ignores the traffic order")
	}
	render := spaFunctionLine(html, "renderFlow")
	must(t, render, "flowTrafficOrder(", "rows drawn in the chosen order")
	must(t, render, "flowCanvasValue(", "rows compare the stream value for the mode")
	must(t, spaFunctionLine(html, "flowCanvasValue"), "sp.profit", "revenue order uses the stream profit")
	must(t, render, `class="grip lock"`, "grip locked while an order is on")
	must(t, render, `<span class="no">${i+1}</span>`, "stream number stays the stored position")
	must(t, html, "traffic:FLOW_TRAFFIC", "traffic order in the page-view snapshot")
}

// TestSPAFlowHeaderIcons checks the campaign header and the stream hint.
// The visitor check stays an icon button with the name in the tooltip.
// Settings shows the gear and the word. Click log and conversion log are one Logs menu.
// Conversions and revenue use c-rev only above zero.
func TestSPAFlowHeaderIcons(t *testing.T) {
	html := spaSource()
	render := spaFunctionLine(html, "renderFlow")
	if render == "" {
		t.Fatal("renderFlow missing")
	}
	must(t, render, `id="testbtn"`, "test button keeps its id")
	must(t, render, `class="icon${TESTER?' primary':''}" title="${tr('Test a visitor')}" aria-label="${tr('Test a visitor')}"`, "test button is an icon with a tooltip")
	must(t, render, `aria-pressed="${TESTER?'true':'false'}">${icon('target')}</button>`, "test button shows only the target icon")
	mustNot(t, render, `${icon('target')}${tr('Test a visitor')}`, "test button has no visible caption")
	must(t, render, `id="gset" title="${tr('Settings')}" aria-label="${tr('Settings')}">${icon('settings')}${tr('Settings')}</button>`, "settings button shows the gear and the word")
	mustNot(t, render, `id="gset" class="icon"`, "settings button is not icon-only")
	must(t, render, `id="glogs" aria-haspopup="menu" aria-expanded="false" aria-label="${tr('flows.logs')}"`, "logs button is one menu")
	must(t, render, `${icon('logs')}${tr('flows.logs')}${icon('down')}`, "logs button keeps the word")
	mustNot(t, render, `id="gclicks"`, "separate click-log button is gone")
	mustNot(t, render, `id="gconv"`, "separate conversion-log button is gone")
	must(t, render, `{label:tr('flows.log.clicks'),icon:'click',on:()=>go('logs/'+encodeURIComponent(g.id))}`, "click log keeps its route and the click icon")
	must(t, render, `{label:tr('flows.log.conv'),icon:'conversions',on:()=>go('conversions/'+encodeURIComponent(g.id))}`, "conversion log keeps its route and the sale icon")
	must(t, render, `q('#testbtn').onclick`, "test button still toggles")
	must(t, render, `gset.onclick=openFlowSettings`, "settings button still opens settings")
	hint := spaFunctionLine(html, "streamHint")
	if hint == "" {
		t.Fatal("streamHint missing")
	}
	must(t, hint, `conv>0?'<span class="c-rev">'+convText+'</span>':convText`, "conversions are bright only above zero")
	must(t, hint, `bots>0?botsBit(bots):''`, "bots show only above zero, as an icon and a number")
	mustNot(t, hint, `tr('bots')`, "the word bots is not written on the line")
	mustNot(t, hint, `c-rev">'+fmt(sp.bots)`, "bots are not painted as revenue")
	money := spaFunctionLine(html, "branchMoney")
	must(t, money, `profit>0?'<span class="c-rev">'+rev+'</span>':rev`, "revenue is bright only above zero")
	must(t, money, `tr('col.cost')`, "spend stays in the hint")
	mustNot(t, money, `c-rev">'+rev+'</span>'+' · '+tr('col.cost')`, "spend is not inside the revenue colour")
	chips := spaFunctionLine(html, "condChips")
	must(t, chips, `c.k==='bot'?icon('bot'):''`, "bot chip leads with the bot icon")
	must(t, chips, `c.k==='unique'?'':lc`, "the word bot stays on the chip")
	for _, line := range []string{
		`'flows.logs':'Logs'`,
		`'flows.logs':'Логи'`,
		`'flows.logs':'Журнали'`,
	} {
		if n := strings.Count(html, line); n != 1 {
			t.Errorf("%s: %d dictionary entries, want 1", line, n)
		}
	}
}

// TestSPAUsersPage checks the users tab, access helpers, Keitaro users import,
// and that prof.users appears in all three interface dictionaries.
func TestSPAUsersPage(t *testing.T) {
	html := spaSource()
	must(t, spaBetween(html, "const SETTINGS_TABS=[", "\n];"), "{id:'users', key:'prof.users', route:'settings/users'", "profile users tab")
	must(t, html, `id="kt-users"`, "keitaro users checkbox")
	must(t, html, "/api/users", "users API")
	must(t, html, "hiddenCols(", "hiddenCols helper")
	must(t, html, "pageAllowed(", "pageAllowed helper")
	must(t, html, "canEdit(", "canEdit helper")
	must(t, html, "userIsEnv(", "userIsEnv helper")
	must(t, html, "'users.src.env'", "users.src.env i18n")
	must(t, html, "'users.src.owner'", "env owner caption")
	must(t, html, "userTypeLabel(", "user type caption")
	setFn := spaFunctionLine(html, "settings")
	mustNot(t, setFn, "prof.account.login", "profile has no account card")
	src := spaFunctionLine(html, "userSourceLabel")
	must(t, src, "users.src.owner", "env row names the owner")
	mustNot(t, src, "return src", "source column does not show the raw access_source")
	must(t, html, "'prof.account'", "prof.account i18n")
	for _, anchor := range []string{
		`'prof.users':'Users'`,
		`'prof.users':'Пользователи'`,
		`'prof.users':'Користувачі'`,
	} {
		i := strings.Index(html, anchor)
		if i < 0 {
			t.Errorf("dictionary anchor missing: %s", anchor)
			continue
		}
		lineEnd := strings.Index(html[i:], "\n")
		if lineEnd < 0 {
			t.Errorf("dictionary line has no newline: %s", anchor)
			continue
		}
		line := html[i : i+lineEnd]
		if !strings.Contains(line, `'users.new'`) {
			t.Errorf("dictionary missing users.new next to %s", anchor)
		}
		if !strings.Contains(line, `'acc.title'`) {
			t.Errorf("dictionary missing acc.title next to %s", anchor)
		}
		if !strings.Contains(line, `'kt.c.users'`) {
			t.Errorf("dictionary missing kt.c.users next to %s", anchor)
		}
	}
}

// TestStreamPageEditor — a stream output of @page:<name>.html opens that file
// in a modal. The field on the stream stays the reference.
func TestStreamPageEditor(t *testing.T) {
	html := spaSource()
	for _, needle := range []string{"/api/pages/", "tr('page.open')", "@page:"} {
		must(t, html, needle, "stream page editor")
	}
}

// TestCodeEditorAndFilepick — Ace loads dynamically, landing editor sits above the scrim, file inputs use filepick.
func TestCodeEditorAndFilepick(t *testing.T) {
	html := spaSource()
	must(t, html, "/assets/ace/ace.js", "ace asset path")
	must(t, html, "function codeEditor", "codeEditor helper")
	must(t, html, `class="filepick"`, "filepick wrapper")
	must(t, html, ".fld input:not([type=checkbox]):not([type=file]),.fld select{width:100%}", "hidden file input inside a field keeps its 1px width")
	must(t, html, "ed.errors", "editor syntax error i18n")
	mustNot(t, html, "<script src=", "ace scripts are injected only")
	land := spaBetween(html, "async function openLandingFile", "\nasync function landings")
	must(t, land, "z-index:81", "landing file modal z-index")
}

// TestLandingTextAndDeleteConfirm checks LAND_TEXT matches server landingTextExt,
// landingText has no .htaccess shortcut, and delete asks with name and id.
func TestLandingTextAndDeleteConfirm(t *testing.T) {
	html := spaSource()
	must(t, html, "const LAND_TEXT=new Set(['html','htm','php','css','js','txt','json','xml','svg'])", "LAND_TEXT matches server landingTextExt")
	lt := spaBetween(html, "function landingText(path)", "\n// landingFileURL")
	mustNot(t, lt, ".htaccess", "landingText must not treat .htaccess as text")
	land := spaBetween(html, "async function landings(", "let NET_SORT")
	must(t, land, "(ID '+id+')?", "landings delete confirm shows name and id")
}

// TestLandingModalRereadsFiles checks that the landing modal reads the file list from
// the server after an archive upload, after an editor save and on the reload button,
// and that its labels are translated.
func TestLandingModalRereadsFiles(t *testing.T) {
	html := spaSource()
	land := spaBetween(html, "const openLand=(id)=>{", "const landingRows=()=>")
	if land == "" {
		t.Fatal("openLand not found")
	}
	must(t, land, "call('GET','/api/landings/'+encodeURIComponent(lid)+'/files')", "file list is read from the server")
	must(t, land, "openLandingFile(lid, row.dataset.lpath, loadFiles)", "an editor save reads the list again")
	must(t, land, "await loadFiles();", "an upload reads the list again")
	must(t, land, "q('#lfiles-reload',m).onclick=()=>busy(q('#lfiles-reload',m), loadFiles)", "reload button")
	must(t, land, "uploadRaw('/api/landings/'+encodeURIComponent(lid)+'/archive', file, 'application/zip', setProgress)", "upload reports progress")
	must(t, land, "bindFilePick(q('#ldrop',m))", "archive drop zone")
	must(t, land, "bindGroupInlineCreate(m, 'lgroup', rows)", "group create link")
	must(t, land, "q('#lopen',m).onclick", "open button")
	mustNot(t, land, "Array.isArray(res.files)", "the archive answer is a count, not a list")
	mustNot(t, land, "label:'Size'", "untranslated size header")
	editor := spaBetween(html, "async function openLandingFile", "\nasync function landings")
	must(t, editor, "if(typeof onSaved==='function') onSaved();", "editor reports a save")
	for _, k := range []string{"land.new", "land.sec.main", "land.sec.source", "land.on", "land.drop", "land.drop.hint", "land.uploading",
		"land.unpacking", "land.size", "land.modified", "land.entry.mark", "land.entry.auto", "land.reload", "land.nofiles", "land.edit",
		"land.zip.only", "land.unit.b", "land.unit.kb", "land.unit.mb", "land.unit.gb"} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("dictionary key %q is defined %d time(s), want one per language", k, n)
		}
	}
}

// TestSPASettingsPickers — country and timezone search boxes and the settings
// postback card are gone. The lead URL in the postback popup has no payout,
// the timezone letter buffer resets after 3 seconds, and the theme-pick
// handler sets data-theme without calling toggleTheme.
func TestSPASettingsPickers(t *testing.T) {
	html := spaSource()
	mustNot(t, html, `id="cfind"`, "country search field")
	mustNot(t, html, `id="tzfind"`, "timezone search field")
	mustNot(t, html, `id="sp-find"`, "spend timezone search")
	mustNot(t, html, `id="pb-card"`, "postback card in settings")
	ep := spaFunctionLine(html, "netTplPostback")
	must(t, ep, "split('{origin}').join(postbackOrigin())", "template postback takes the public origin")
	pb := spaFunctionLine(html, "openPostbacks")
	if pb == "" {
		t.Fatal("openPostbacks missing")
	}
	must(t, pb, "postbackOrigin()", "popup postback uses the public origin")
	must(t, html, "function postbackBase(", "postback base helper")
	must(t, pb, "postbackStatusURL(o,'lead')", "lead postback in the popup")
	must(t, pb, "postbackStatusURL(o,'sale')", "sale postback in the popup")
	must(t, spaFunctionLine(html, "postbackStatusURL"), "&payout={payout}", "sale postback keeps payout")
	menuLine := spaFunctionLine(html, "userMenuItems")
	if menuLine == "" {
		t.Fatal("user menu missing")
	}
	must(t, menuLine, "adminMenuEntries()", "user menu takes its entries from the settings list")
	list := spaBetween(html, "const SETTINGS_TABS=[", "\n];")
	must(t, list, "{id:'postbacks', key:'pb.title', icon:'send', tab:false, open:()=>openPostbacks(), allow:()=>pageAllowed(ME,'conversions')}", "postbacks entry of the menu")
	if pbAt, aboutAt := strings.Index(list, "id:'postbacks'"), strings.Index(list, "id:'about'"); pbAt < 0 || aboutAt < pbAt {
		t.Errorf("postbacks must sit above about in the user menu")
	}
	tz := spaFunctionLine(html, "bindTzPick")
	if tz == "" {
		t.Fatal("bindTzPick missing")
	}
	must(t, tz, "setTimeout(()=>{buf=''; if(open) paint()}, 3000)", "timezone buffer resets after 3 seconds")
	pickAt := strings.Index(html, `data-theme-pick]').forEach`)
	if pickAt < 0 {
		t.Fatal("theme pick handler missing")
	}
	frag := html[pickAt:]
	if len(frag) > 300 {
		frag = frag[:300]
	}
	must(t, frag, "setAttribute('data-theme'", "theme pick sets data-theme")
	mustNot(t, frag, "toggleTheme", "theme pick must not call toggleTheme")
}

// TestSPAPostbackPopup — the Postbacks popup shows a read-only URL with REPLACE
// placeholders, a copy button, a host selector and a Documentation button that
// opens the in-app page; the page and every new i18n key exist in en, ru and uk.
func TestSPAPostbackPopup(t *testing.T) {
	html := spaSource()
	pb := spaFunctionLine(html, "openPostbacks")
	if pb == "" {
		t.Fatal("openPostbacks missing")
	}
	must(t, pb, `id="pb-url"`, "postback URL field")
	must(t, pb, "readonly", "postback URL field is read-only")
	must(t, pb, `id="pb-copy"`, "copy button for the URL")
	must(t, pb, `id="pb-docs"`, "documentation button")
	must(t, pb, `id="pb-host"`, "host selector")
	must(t, pb, "postbackHosts(postbackOrigin(), linkDomains())", "hosts come from the page and link domains")
	must(t, pb, "postbackDefaultHost(DOMAINS, location.hostname, known)", "default host comes from the admin-panel domain or the page")
	must(t, pb, "h===def?' selected':''", "default host is selected in the list, not only shown as a placeholder")
	must(t, pb, `id="pb-pop-sale">${tr('pb.sale')}</button><button type="button" id="pb-pop-rejected">${tr('pb.rejected')}</button>`, "rejected button sits right of sale")
	must(t, pb, "postbackStatusURL(o,'rejected')", "rejected button uses the canonical status")
	must(t, pb, "q('#pb-pop-rejected',m).onclick", "rejected button copies its URL")
	must(t, pb, "'\\nrejected: '+rejected(o)", "copy all includes rejected")
	must(t, pb, "go('settings/pbdocs')", "documentation opens the in-app page")
	must(t, html, "function postbackURL(", "URL builder")
	must(t, html, "/postback?subid=REPLACE&status=REPLACE&payout=REPLACE", "URL template")
	must(t, html, "function postbackDocs(", "documentation page")
	must(t, html, "postbackDocs(); return;", "profile routes settings/pbdocs to the page")
	docs := spaFunctionLine(html, "postbackDocs")
	for _, k := range []string{"pbd.fmt.t", "pbd.par.t", "pbd.st.t", "pbd.money.t", "pbd.cid.t", "pbd.ex.t", "pbd.res.t", "pbd.tr.t"} {
		must(t, docs, "'"+k+"'", "documentation card "+k)
	}
	mustNot(t, pb+docs, "?pb=", "the old pixel is not advertised")
	keys := []string{"pb.docs", "pb.url", "pb.host", "pb.copy.url", "pb.lead", "pb.sale", "pb.rejected", "pb.nokey", "pb.local", "pbd.title", "pbd.back",
		"pbd.c.p", "pbd.c.a", "pbd.c.m", "pbd.fmt.t", "pbd.fmt.b", "pbd.fmt.b2", "pbd.par.t", "pbd.p.subid", "pbd.p.status",
		"pbd.p.payout", "pbd.p.depsum", "pbd.p.tid", "pbd.st.t", "pbd.st.b", "pbd.money.t", "pbd.money.b",
		"pbd.cid.t", "pbd.cid.b", "pbd.cid.b2", "pbd.ex.t", "pbd.ex.b", "pbd.res.t", "pbd.res.b", "pbd.tr.t", "pbd.tr.b"}
	for _, k := range keys {
		if n := strings.Count(html, "'"+k+"':'"); n != 3 {
			t.Errorf("i18n key %s defined %d times, want 3 (en, ru, uk)", k, n)
		}
	}
}

// TestNavIconTooltips checks nav tooltips only while icon mode hides labels.
func TestNavIconTooltips(t *testing.T) {
	html := spaSource()
	fit := spaFunctionLine(html, "fitNav")
	titles := spaFunctionLine(html, "navTitles")
	paint := spaFunctionLine(html, "paintNav")
	menu := spaFunctionLine(html, "menu")
	chrome := spaFunctionLine(html, "paintChrome")
	if fit == "" || titles == "" || paint == "" || menu == "" || chrome == "" {
		t.Fatal("fitNav, navTitles, paintNav, menu or paintChrome missing")
	}
	must(t, fit, "navTitles(", "fitNav applies nav tooltips after overflow")
	must(t, titles, "removeAttribute('title')", "navTitles clears title when labels show")
	must(t, titles, "tr('nav.'+", "navTitles uses the nav translation key")
	must(t, titles, "nav.reports", "navTitles titles the reports button")
	mustNot(t, paint, "title=", "paintNav must not hard-code title")
	must(t, menu, "it.title", "menu renders an optional item title")
	must(t, chrome, "title:tr('nav.'+r)", "burger report items carry a title")
	must(t, chrome, "title:tr('nav.'+id)", "burger top-level items carry a title")
}

// TestOfferFormAndColumns checks offer editor fields and column catalogs.
// Campaign ids stay hits, uniq and conv. The report catalog lists cost and no ROI, profit or uEPC.
// The URL builder stops at sub_id_30 and does not offer status.
func TestOfferFormAndColumns(t *testing.T) {
	html := spaSource()
	flows := catalogBranch(html, "flows")
	must(t, flows, "['hits'", "campaign column id hits")
	must(t, flows, "['uniq'", "campaign column id uniq")
	must(t, flows, "['conv'", "campaign column id conv")
	must(t, html, "emtds-offer-cols", "offer column storage key")
	must(t, html, "emtds-net-cols", "network column storage key")
	rep := catalogBranch(html, "report")
	must(t, rep, "['cost','col.cost'", "report catalog lists the cost column")
	for _, id := range []string{"['roi'", "['uepc'", "['profit','col.profit'"} {
		mustNot(t, rep, id, "report catalog has no money derived from cost")
	}
	macros := offerMacroSource(html)
	must(t, macros, "{subid}", "offer URL macro subid")
	must(t, macros, "{root_subid}", "offer URL macro root_subid")
	must(t, html, "'[SUBID]','{root_subid}','{origin}'", "stream output macro picker lists root_subid")
	if got := strings.Count(html, "{payout} {root_subid} {sub_id_1}"); got != 3 {
		t.Fatalf("outbound postback macro hint root_subid count = %d, want 3", got)
	}
	must(t, html, "const SUBID_MAX=30", "Sub ID ceiling is 30")
	must(t, macros, "length:SUBID_MAX", "offer URL macros run through the ceiling")
	must(t, macros, "'{sub_id_'+(i+1)+'}'", "offer URL macro sub_id_N")
	mustNot(t, macros, "{sub_id_31}", "offer URL builder has no sub_id_31")
	mustNot(t, macros, "{status}", "offer URL builder has no status macro")
	off := spaBetween(html, "async function offers(", "async function domains(")
	must(t, off, "payout_model", "offer save sends payout_model")
	must(t, off, "countries", "offer save sends countries")
	saveAt := strings.Index(off, "async(box)=>{")
	if saveAt < 0 {
		t.Fatal("offer save handler missing")
	}
	endRel := strings.Index(off[saveAt:], "});")
	if endRel < 0 {
		t.Fatal("offer save handler does not close")
	}
	save := off[saveAt : saveAt+endRel]
	mustNot(t, save, "openOffer(", "add another must not call openOffer before closeForm")
	must(t, save, "OFFER_AGAIN", "add another only stores a flag")
	painted := strings.Index(off, "view().innerHTML")
	if painted < 0 || !strings.Contains(off[painted:], "if(OFFER_AGAIN)") {
		t.Error("add another must open the next offer after the table is painted")
	}
}

func catalogBranch(html, kind string) string {
	fn := spaFunctionLine(html, "colCatalog")
	if kind == "report" {
		i := strings.LastIndex(fn, "return [")
		if i < 0 {
			return ""
		}
		return fn[i:]
	}
	needle := "kind==='" + kind + "'"
	i := strings.Index(fn, needle)
	if i < 0 {
		return ""
	}
	rest := fn[i:]
	j := strings.Index(rest, "return [")
	if j < 0 {
		return ""
	}
	rest = rest[j:]
	k := strings.Index(rest, "];")
	if k < 0 {
		return rest
	}
	return rest[:k+2]
}

func offerMacroSource(html string) string {
	i := strings.Index(html, "const OFFER_MACROS=")
	if i < 0 {
		return ""
	}
	rest := html[i:]
	j := strings.Index(rest, "];")
	if j < 0 {
		return rest
	}
	return rest[:j+2]
}

func spaBetween(html, start, end string) string {
	i := strings.Index(html, start)
	if i < 0 {
		return ""
	}
	rest := html[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// pageFnBody returns one top-level async page handler up to the next handler declaration.
func pageFnBody(html, fn string) string {
	start := "async function " + fn + "("
	i := strings.Index(html, start)
	if i < 0 {
		return ""
	}
	tail := html[i+len(start):]
	j := strings.Index(tail, "\nasync function ")
	if j < 0 {
		return html[i:]
	}
	return html[i : i+len(start)+j]
}

// TestCatalogSearchAndGroups checks shared catalog filters, group window, and save bodies.
func TestCatalogSearchAndGroups(t *testing.T) {
	html := spaSource()
	for _, kind := range []string{"offers", "landings", "networks", "sources", "domains"} {
		fn := spaFunctionLine(html, kind)
		if fn == "" {
			t.Fatalf("SPA function %q missing", kind)
		}
		must(t, fn, "catalogBar('"+kind+"'", kind+" catalogBar call")
		must(t, fn, "bindCatalogBar('"+kind+"'", kind+" bindCatalogBar call")
		must(t, fn, "clearGroupLabel(", kind+" uses clearGroupLabel")
	}
	bar := spaFunctionLine(html, "catalogBar")
	must(t, bar, `id="${kind}-q"`, "catalogBar search id template")
	must(t, bar, `id="${kind}-group"`, "catalogBar group id template")
	must(t, bar, `class="lft"`, "catalogBar left cluster")
	must(t, bar, `class="rgt"`, "catalogBar right cluster")
	must(t, bar, `-refresh"`, "catalogBar refresh id template")
	mustNot(t, bar, `${groups}`, "groups button is not in the filter bar")
	must(t, spaFunctionLine(html, "groupHeadBtn"), `icon('folder')`, "groups control is a folder icon")
	must(t, spaFunctionLine(html, "offers"), `groupHeadBtn('off-groups')`, "offers groups sit in the header")
	must(t, spaFunctionLine(html, "landings"), `groupHeadBtn('land-groups')`, "landings groups sit in the header")
	must(t, spaFunctionLine(html, "networks"), `groupHeadBtn('net-groups')`, "networks groups sit in the header")
	must(t, spaFunctionLine(html, "sources"), `groupHeadBtn('src-groups')`, "sources groups sit in the header")
	must(t, spaFunctionLine(html, "domains"), `groupHeadBtn('dom-groups')`, "domains groups sit in the header")
	must(t, html, "function groupLabels(", "groupLabels helper")
	must(t, html, "function fillGroupSelect(", "fillGroupSelect helper")
	must(t, html, "function openGroupWindow(", "openGroupWindow helper")
	must(t, html, "function clearGroupLabel(", "clearGroupLabel helper")
	cgl := spaFunctionLine(html, "clearGroupLabel")
	mustNot(t, cgl, "Promise.all", "clearGroupLabel must not use Promise.all")
	must(t, cgl, "for(", "clearGroupLabel uses sequential for")
	must(t, cgl, "await", "clearGroupLabel awaits PUT")
	land := spaBetween(html, "async function landings(", "let NET_SORT")
	net := spaBetween(html, "async function networks(", "const SRC_KEYS")
	src := spaBetween(html, "async function sources(", "const OFFER_MACROS")
	must(t, land, "group:", "landing save sends group")
	must(t, land, "'/api/landings/'", "landing PUT path")
	must(t, net, "group:", "network save sends group")
	must(t, net, "'/api/affiliate-networks/'", "network PUT path")
	must(t, src, "group:", "source save sends group")
	must(t, src, "'/api/traffic-sources/'", "source PUT path")
	ogw := spaFunctionLine(html, "openGroupWindow")
	mustNot(t, ogw, "Promise.all", "openGroupWindow must not use Promise.all")
	must(t, ogw, "await opts.onClear", "openGroupWindow awaits onClear")
	landCat := catalogBranch(html, "landings")
	netCat := catalogBranch(html, "networks")
	must(t, landCat, "['group','off.group',1]", "landings catalog group column")
	must(t, netCat, "['group','off.group',1]", "networks catalog group column")
}

// TestSearchClearButton checks that the campaign and catalog search fields share the clear button.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestSearchClearButton(t *testing.T) {
	html := spaSource()
	field := spaFunctionLine(html, "searchField")
	must(t, field, `class="s-clr icon sm ghost"`, "clear button uses the icon button style")
	must(t, field, `hidden`, "clear button starts hidden")
	must(t, field, `aria-label="${esc(tr('Clear'))}"`, "clear button label goes through tr")
	must(t, field, `icon('x')`, "clear button uses the cross icon")
	bind := spaFunctionLine(html, "bindSearchClear")
	must(t, bind, `clr.hidden=!inp.value`, "button shows only while the input has text")
	must(t, bind, `inp.focus()`, "focus returns to the input")
	must(t, bind, `new Event('input'`, "clearing fires the page input handler")
	must(t, spaFunctionLine(html, "catalogBar"), "searchField(", "catalog bar renders the field through the shared component")
	must(t, spaFunctionLine(html, "bindCatalogBar"), "bindSearchClear(qi)", "catalog bar binds the clear button")
	must(t, html, "${searchField(`<input id=\"fq\"", "campaign search uses the shared field")
	must(t, html, "bindSearchClear(q('#fq'))", "campaign search binds the clear button")
	must(t, html, `.sfld .s-clr:focus-visible{box-shadow:0 0 0 3px var(--focus)}`, "keyboard focus ring on the clear button")
	must(t, html, `.sfld .s-clr[hidden]{display:none}`, "hidden attribute wins over the flex display")
}

// TestHeaderPattern — shared page header: catalog toolbar layout and refresh/export controls.
func TestHeaderPattern(t *testing.T) {
	html := spaSource()
	tools := spaBetween(html, "function colTools(", "\n}")
	mustNot(t, tools, "data-col-refresh", "colTools without refresh button")
	must(t, tools, "data-col-gear", "colTools keeps column gear")
	for _, fn := range []string{"offers", "landings", "networks", "sources", "domains"} {
		body := pageFnBody(html, fn)
		if body == "" {
			t.Fatalf("SPA function %q missing", fn)
		}
		phStart := strings.Index(body, `<div class="ph">`)
		if phStart < 0 {
			t.Fatalf("%s: ph missing", fn)
		}
		phEnd := strings.Index(body[phStart:], "</div>\n")
		if phEnd < 0 {
			t.Fatalf("%s: ph end missing", fn)
		}
		ph := body[phStart : phStart+phEnd]
		mustNot(t, ph, `-new"`, fn+" ph without create button")
		mustNot(t, ph, `-groups"`, fn+" ph without groups button")
	}
	logs := spaBetween(html, "async function logs(", "async function loadLogs(")
	must(t, logs, `id="log-refresh"`, "logs refresh control")
	must(t, logs, `data-col-refresh="logs"`, "logs refresh kind")
	if n := strings.Count(logs, "data-col-refresh="); n != 1 {
		t.Errorf("logs must have exactly one data-col-refresh, got %d", n)
	}
	must(t, logs, `q('#log-export')`, "logs export menu")
	must(t, logs, "dockHTML('log','log-export')", "logs export in dock")
	must(t, logs, "paintLogDock(", "logs dock pager")
	mustNot(t, logs, `id="export"`, "logs without standalone csv button")
	mustNot(t, logs, `id="exporthtml"`, "logs without standalone html button")
	rep := spaBetween(html, "function paintReport(", "async function integrations(")
	must(t, rep, `id="rep-refresh"`, "report refresh control")
	must(t, rep, `data-col-refresh="report"`, "report refresh kind")
	if n := strings.Count(rep, "data-col-refresh="); n != 1 {
		t.Errorf("paintReport must have exactly one data-col-refresh, got %d", n)
	}
	must(t, rep, `id="rep-export"`, "report export menu")
	mustNot(t, rep, `id="rcsv"`, "report without rcsv button")
	mustNot(t, rep, `id="rhtml"`, "report without rhtml button")
	conv := spaBetween(html, "async function conversions(", "function paintPbPage(")
	must(t, conv, `id="pb-refresh"`, "conversions refresh control")
	must(t, conv, `data-col-refresh="conv"`, "conversions refresh kind")
	if n := strings.Count(conv, "data-col-refresh="); n != 1 {
		t.Errorf("conversions must have exactly one data-col-refresh, got %d", n)
	}
}

// TestSPALogFilters checks the journal filter builder, column ids and the conversion page size.
func TestSPALogFilters(t *testing.T) {
	html := spaSource()
	must(t, html, "function filterBuilder(", "filter builder")
	must(t, html, "/api/log-fields", "log field catalog")
	must(t, html, "/api/log-values", "log field values")
	must(t, html, "function condsQS(", "condition query")
	must(t, html, "function ensureCatalogs(", "catalog loader")
	must(t, html, "function logDetail(", "visit detail")
	mustNot(t, html, `class="msel"`, "old multi-select")
	mustNot(t, html, `#pb-status`, "old conversion status select")
	conv := catalogBranch(html, "conv")
	must(t, conv, "'sale_period'", "conversion sale period column")
	must(t, conv, "'original_status'", "conversion original status column")
	logs := catalogBranch(html, "logs")
	must(t, logs, "length:SUBID_MAX", "click log sub id ceiling")
	must(t, logs, "subIDCol(i+1,0)", "click log sub id columns")
	convCols := catalogBranch(html, "conv")
	must(t, convCols, "subIDCol(i+3,0)", "conversion sub id columns after the first two")
	cols := spaFunctionLine(html, "loadCols")
	must(t, cols, "group:'campaign'", "saved campaign column id")
	must(t, cols, "ua:'useragent'", "saved user-agent column id")
	must(t, cols, "sub_id_", "saved sub id column migration")
	pb := spaFunctionLine(html, "pbQuery")
	must(t, pb, "offset", "conversion query offset")
	must(t, html, "'fld.sale_period'", "sale period label")
	must(t, html, "'flt.apply'", "apply label")
	must(t, html, "function fltFieldSuggest(", "filter field classifier")
	must(t, html, "function bindFltCombo(", "filter combobox")
	must(t, html, "function bindPickCombo(", "shared combobox")
	must(t, html, "function fetchExport(", "export fetch helper")
	must(t, html, `role="combobox"`, "filter combobox a11y")
	mustNot(t, html, "<datalist", "no native datalist in SPA")
	must(t, html, "'flt.ungrouped'", "ungrouped filter i18n")
	must(t, html, "'export.truncated'", "export truncation i18n")
	must(t, spaConstLangBody(html, "I18N", "ru"), "'ROI':'ROI'", "ROI in I18N ru")
	must(t, spaConstLangBody(html, "I18N", "uk"), "'ROI':'ROI'", "ROI in I18N uk")
	rep := catalogBranch(html, "report")
	must(t, rep, "['group','conv.campaign',1]", "report catalog stays the last return")
	must(t, html, "groupFieldBlock('dgroup'", "domain group field")
}

// TestFilterBuilderFields checks the filter window lists every column of its table.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestFilterBuilderFields(t *testing.T) {
	html := spaSource()
	filt := spaFunctionLine(html, "fieldsForFilter")
	if filt == "" {
		t.Fatal("fieldsForFilter missing")
	}
	mustNot(t, filt, "fieldReady", "filter catalog ignores wave readiness")
	must(t, filt, "f.conv:f.events", "filter catalog keeps the table column")
	cols := spaFunctionLine(html, "fieldsFor")
	must(t, cols, "fieldReady", "table columns stay behind a ready wave")
	menu := spaFunctionLine(html, "fltMenuSplit")
	must(t, menu, "fieldsForFilter(table)", "menu split uses the filter catalog")
	must(t, menu, "FLT_GROUPS", "menu split reads the groups")
	must(t, menu, "rest:have.filter", "names outside the groups stay in the menu")
	b := spaFunctionLine(html, "filterBuilder")
	if b == "" {
		t.Fatal("filterBuilder missing")
	}
	must(t, b, "fltMenuSplit(table)", "builder groups parameters")
	must(t, b, "tr('flt.ungrouped')", "fields outside groups stay visible")
	must(t, b, "fltCanAdd(fieldsForFilter(table)", "add button stops at the row cap")
	must(t, html, "const FLT_MAX=20", "filter row cap matches the server")
	apply := spaFunctionLine(html, "fltApplyDraft")
	must(t, apply, "slice(0, FLT_MAX)", "apply keeps at most FLT_MAX conditions")
	mustNot(t, b, "fieldsFor(table)", "builder does not hide unready fields")
	qs := spaFunctionLine(html, "condsQS")
	must(t, qs, "qs.append('f'", "each condition value is its own f parameter")
	must(t, html, "condsQS(logCondsForQuery())", "click log sends every condition")
	must(t, html, "condsQS(pbCondsForQuery())", "conversions send every condition")
	must(t, html, "condsQS(REP.conds)", "report sends every condition")
	must(t, html, "filterBuilder('events', LOGF.conds", "clicks use the events table")
	must(t, html, "filterBuilder('conversions', PB.conds", "conversions use the conversions table")
	must(t, html, "filterBuilder('events', REP.conds", "report uses the events table")
	sug := spaFunctionLine(html, "fltSuggestItems")
	if sug == "" {
		t.Fatal("fltSuggestItems missing")
	}
	must(t, sug, "fltCatalogBase(field)", "suggestions start from the catalog")
	must(t, sug, "!loaded.length", "an empty cache does not hide the catalog")
	must(t, b, "fltSuggestItems(row.f, opts[row.f])", "the value list reads the live catalog")
	must(t, b, "el.dispatchEvent(new FocusEvent('focus'))", "choosing a field opens its value list")
	pick := spaFunctionLine(html, "bindPickCombo")
	must(t, pick, "if(PICK_COMBO&&PICK_COMBO.list) place(PICK_COMBO.list)", "scrolling keeps the value list beside the field")
	must(t, pick, "box&&box.contains(e.target)", "a click in the value field does not close its list")
	must(t, html, ".chits button{display:flex;width:100%;text-align:left;justify-content:flex-start", "suggestion rows keep the start of the name")
	cat := spaFunctionLine(html, "fltCatalogItems")
	must(t, cat, "f==='device'", "device offers the known types")
	must(t, cat, "'phone','computer','tablet'", "device types are phone, computer, tablet")
}

// TestColumnResize checks every header can be dragged from its right edge.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestColumnResize(t *testing.T) {
	html := spaSource()
	head := spaFunctionLine(html, "sortableHead")
	if head == "" {
		t.Fatal("sortableHead missing")
	}
	must(t, head, "data-col=", "journal headers keep a stable column id")
	must(t, html, `data-col="${esc(k)}"`, "campaign headers keep a stable column id")
	must(t, html, ".col-grip{position:absolute", "the header edge is a resize grip")
	must(t, html, "table.colw{table-layout:fixed}", "a dragged table locks column widths")
	grip := spaFunctionLine(html, "colGrips")
	if grip == "" {
		t.Fatal("colGrips missing")
	}
	must(t, grip, "col-grip", "each header receives a grip")
	mustNot(t, grip, "data-sort", "the grip is not a sort button")
	bind := spaFunctionLine(html, "colBindResize")
	if bind == "" {
		t.Fatal("colBindResize missing")
	}
	must(t, bind, "closest('.col-grip')", "only the grip starts a resize")
	must(t, bind, "stopPropagation", "a grip press does not sort or open a row menu")
	must(t, bind, "colResetColumn", "a double click clears one column")
	store := spaFunctionLine(html, "colWidthRead")
	must(t, store, "COLW_KEY", "widths are read from the browser store")
	must(t, html, "const COLW_KEY='emtds-colw'", "widths stay in emtds-colw")
	write := spaFunctionLine(html, "colWidthWrite")
	mustNot(t, write, "/api/ui", "column widths are not saved to the shared UI")
	must(t, write, "touchPageViews()", "column widths are saved with the account")
	apply := spaFunctionLine(html, "colApplySaved")
	must(t, apply, "colWidthRead()", "a redraw puts the stored width back")
	watch := spaFunctionLine(html, "colWatch")
	must(t, watch, "MutationObserver", "new tables receive grips without a per-page hook")
	must(t, html, "colResizeStart()", "resize starts with the page")
}

var jsSetRe = regexp.MustCompile(`'((?:[^'\\]|\\.)*)'`)

// jsSetLiterals reads string keys from a `const Name=new Set([...]);` block in the SPA.
func jsSetLiterals(html, constName string) map[string]bool {
	re := regexp.MustCompile(`const ` + constName + `=new Set\(\[(.*?)\]\);`)
	m := re.FindStringSubmatch(html)
	out := map[string]bool{}
	if len(m) < 2 {
		return out
	}
	for _, k := range jsSetRe.FindAllStringSubmatch(m[1], -1) {
		out[k[1]] = true
	}
	return out
}

// fltGroupsFields lists every journal field name declared in FLT_GROUPS.
func fltGroupsFields(html string) []string {
	i := strings.Index(html, "const FLT_GROUPS=")
	if i < 0 {
		return nil
	}
	rest := html[i+len("const FLT_GROUPS="):]
	if !strings.HasPrefix(rest, "[") {
		return nil
	}
	depth, j := 0, 0
	for j < len(rest) {
		switch rest[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				var out []string
				for _, k := range jsSetRe.FindAllStringSubmatch(rest[:j+1], -1) {
					out = append(out, k[1])
				}
				return out
			}
		}
		j++
	}
	return nil
}

// TestFLTGroupsFieldClassification ensures every filter field in FLT_GROUPS is strict, suggest, or free by design.
func TestFLTGroupsFieldClassification(t *testing.T) {
	html := spaSource()
	fields := fltGroupsFields(html)
	if len(fields) < 20 {
		t.Fatalf("FLT_GROUPS fields: got %d", len(fields))
	}
	catalog := jsSetLiterals(html, "FLT_CATALOG")
	suggest := jsSetLiterals(html, "FLT_SUGGEST")
	strict := map[string]bool{}
	for k := range catalog {
		strict[k] = true
	}
	for _, k := range []string{"country", "status", "original_status", "prev_status"} {
		strict[k] = true
	}
	for _, f := range fields {
		if strict[f] && suggest[f] {
			t.Errorf("field %q is both strict and suggest", f)
		}
	}
	for _, f := range []string{"site", "ad_campaign_id", "creative_id", "external_id", "out", "keyword", "referer", "cid"} {
		if strict[f] || suggest[f] {
			t.Errorf("field %q must be free text", f)
		}
	}
	for _, f := range []string{"campaign", "offer_id", "country", "status"} {
		if !strict[f] {
			t.Errorf("field %q must be strict", f)
		}
	}
	for _, f := range []string{"stream", "device", "city", "xrw"} {
		if f == "xrw" {
			if strict[f] {
				t.Errorf("field %q must not be strict", f)
			}
			continue
		}
		if !suggest[f] {
			t.Errorf("field %q must be suggest", f)
		}
	}
}

// TestSPAProfileAndReportFilters checks the profile page, API keys and report filters.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestSPAProfileAndReportFilters(t *testing.T) {
	html := spaSource()
	must(t, html, "function profile(", "profile page")
	must(t, html, "/api/api-keys", "api key routes")
	must(t, html, "#/settings/docs", "docs hash")
	must(t, spaBetween(html, "const SETTINGS_TABS=[", "\n];"), "route:'settings/keys'", "keys route")
	must(t, html, "Api-Key", "api key header")
	must(t, html, "REP.conds", "report conditions")
	rep := spaBetween(html, "async function report(", "async function keitaro(")
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	if rep == "" || dash == "" {
		t.Fatal("report or dashboard missing")
	}
	must(t, rep, "filterBuilder('events'", "report filter builder")
	mustNot(t, dash, "filterBuilder('events'", "dashboard filter builder was removed with the Filters button")
	must(t, html, "emtds-dash-dim", "dashboard custom dimension memory")
	must(t, dash, "bd-custom", "one extra breakdown card")
	must(t, html, "settings:profile", "PAGES routes settings to profile")
	pagesAt := strings.Index(html, "const PAGES=")
	if pagesAt < 0 {
		t.Fatal("PAGES missing")
	}
	pagesEnd := strings.Index(html[pagesAt:], "\n")
	if pagesEnd < 0 {
		t.Fatal("PAGES line has no newline")
	}
	pages := html[pagesAt : pagesAt+pagesEnd]
	must(t, pages, "settings:profile", "PAGES keeps settings on profile")
	must(t, pages, "keitaro", "PAGES still registers keitaro")
	for _, anchor := range []string{
		`'menu.settings':'Settings'`,
		`'menu.settings':'Настройки'`,
		`'menu.settings':'Налаштування'`,
	} {
		if !strings.Contains(html, anchor) {
			t.Errorf("menu label missing: %s", anchor)
		}
	}
}

// TestAppearanceAccents checks accent tokens, the swatch picker and row hover.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestAppearanceAccents(t *testing.T) {
	html := spaSource()
	must(t, html, `:root[data-accent="blue"]`, "blue accent tokens")
	must(t, html, `:root[data-accent="green"]`, "green accent tokens")
	must(t, html, `:root[data-accent="graphite"]`, "graphite accent tokens")
	must(t, html, `:root[data-accent="brand"]{`, "brand accent tokens")
	must(t, html, `const ACCENTS=['violet','blue','green','graphite','yellow','fire','brand']`, "every accent in the list")
	must(t, html, `:root[data-accent="yellow"]{`, "yellow accent tokens")
	must(t, html, `:root[data-accent="fire"]{`, "fire accent tokens")
	must(t, html, `--accent-fill:#facc15;--accent-fill-h:#eab308;--accent-fill-fg:#12151c}`, "yellow fills carry dark text")
	must(t, html, `button.primary,.btn.primary{background:var(--accent-fill);border-color:var(--accent-fill);color:var(--accent-fill-fg)}`, "primary buttons use the fill tokens")
	must(t, html, `:root[data-accent="brand"] button.primary`, "brand primary button gradient")
	must(t, html, `.swatch[data-accent-pick="brand"]`, "brand swatch")
	for _, key := range []string{"accent.brand':'Brand'", "accent.brand':'Фирменная'", "accent.brand':'Фірмова'"} {
		must(t, html, key, "brand accent label "+key)
	}
	must(t, html, `localStorage.getItem('emtds-accent')`, "remembered accent")
	must(t, html, `data-accent-pick`, "accent swatch")
	must(t, html, `function applyAccent(`, "applyAccent")
	must(t, html, `function emtdsPaintAccent(`, "custom accent paints the same triples")
	must(t, html, `id="accent-own"`, "custom colour swatch under the presets")
	must(t, html, `function applyCustomAccent(`, "custom colour uses the accent key")
	must(t, html, `class="set-slot"`, "interface and statistics share a slot")
	must(t, html, `class="set-stats-h"`, "statistics heading")
	for _, key := range []string{"accent.custom':'Your colour'", "accent.custom':'Свой цвет'", "accent.custom':'Свій колір'"} {
		must(t, html, key, "custom accent label "+key)
	}
	must(t, html, `rgb(var(--acc-l))`, "light accent from the RGB triple")
	must(t, html, `tbody tr:hover td{background:var(--accent-soft)}`, "row hover uses the accent")
	mustNot(t, html, `tbody tr:hover td{background:var(--surface-2)}`, "surface row hover")
}

// TestNavMoreMenu checks the overflow menu for hidden nav items.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestNavMoreMenu(t *testing.T) {
	html := spaSource()
	must(t, html, `function navHidden(`, "navHidden")
	must(t, html, `navmore-btn`, "more button")
	must(t, html, `'nav.more':'Прочее'`, "ru nav.more")
	must(t, html, `id="navmore"`, "more slot outside the scrolling nav")
	must(t, html, `#navmore:empty{display:none}`, "empty more slot hidden")
	chrome := spaFunctionLine(html, "paintChrome")
	if chrome == "" {
		t.Fatal("paintChrome missing")
	}
	must(t, chrome, `cap:tr('nav.more')`, "burger more section")
}

// TestTimezoneKyiv checks the Kiev alias and the canonical timezone name.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestTimezoneKyiv(t *testing.T) {
	html := spaSource()
	must(t, html, `'Europe/Kiev':'Europe/Kyiv'`, "Kiev alias")
	must(t, html, `function tzCanon(`, "tzCanon")
}

// TestFilePickAndCrop checks logo crop, file drop and the header logo box.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestFilePickAndCrop(t *testing.T) {
	html := spaSource()
	must(t, html, `function cropImage(`, "cropImage")
	must(t, html, `function bindFilePick(`, "bindFilePick")
	must(t, html, `upload:'<path`, "upload icon")
	must(t, html, `.logo{height:26px;width:auto`, "logo keeps its ratio")
	mustNot(t, html, `.logo{width:26px;height:26px`, "square logo rule")
}

// TestButtonSizes checks icon and tag button sizes.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestButtonSizes(t *testing.T) {
	html := spaSource()
	must(t, html, `button.icon.sm{height:26px;width:26px`, "small icon button")
	must(t, html, `button.tagbtn{`, "tag button")
	mustNot(t, html, `button.sm,.btn.sm{`, "shared small text button rule")
	mustNot(t, html, `id="go" style="width:100%;height:36px"`, "sign-in button height")
	mustNot(t, html, `href="#/flows" style="height:34px"`, "back link height")
	must(t, html, `href="#/flows" style="height:32px"`, "back link height")
}

// TestDomainsCatalogAndPager checks domain columns, the pager and the row menu.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestDomainsCatalogAndPager(t *testing.T) {
	html := spaSource()
	must(t, html, `if(kind==='domains') return [['id','flows.col.num',1],['host','dom.host',1]`, "domains column set")
	must(t, html, `const DOM_LOCKED=new Set(['id','host'])`, "locked domain columns")
	must(t, html, `domains:'emtds-dom-cols'`, "domains column storage key")
	must(t, html, `data-col-gear="domains"`, "domains column gear")
	must(t, html, `const DOM_PAGE_KEY='emtds-domains-page'`, "domains page storage key")
	must(t, html, `function domainStatusText(`, "domainStatusText")
	must(t, html, `function domainCell(`, "domainCell")
	must(t, html, `function paintDomPage(`, "paintDomPage")
	must(t, html, `'dom.why.dns':'DNS-запись не найдена'`, "ru dns miss")
	must(t, html, `'dom.status':'Статус'`, "ru domain status")
	must(t, html, `'dom.campaigns':'Кампании'`, "ru domain campaigns")
	src := spaFunctionLine(html, "domains")
	if src == "" {
		src = html
	}
	must(t, src, `<th class="acts"></th>`, "actions header on domains table")
	must(t, src, `data-more="${esc(d.id)}"`, "domain row more menu")
	mustNot(t, html, `data-dcheck=`, "inline domain check")
	mustNot(t, html, `data-ddel=`, "inline domain delete")
	must(t, html, "let DOM_PICK=new Set()", "domain pick set")
	must(t, src, `data-dpick="*"`, "domain select all")
	must(t, src, `bulkAct('dbulk-check'`, "bulk check")
	must(t, src, `bulkAct('dbulk-group'`, "bulk group")
	must(t, src, `bulkAct('dbulk-del'`, "bulk delete")
	must(t, src, "openDomainEdit(", "edit from row menu")
	must(t, html, "function domainPayload(", "domainPayload")
	must(t, html, "function saveDomain(", "saveDomain")
	must(t, html, "function runPool(", "runPool")
	mustNot(t, html, "function domainStatus(", "old status cell helper")
	cell := spaFunctionLine(html, "domainCell")
	must(t, cell, `<span class="dot ${domainDot(d)}" title="${esc(domainStatusText(d))}"></span> ${esc(d.host)}`, "dot before host")
	must(t, cell, "if(id==='status') return `<td>${domainStateChip(d)}${domainPending(d)?'':`<span class=\"dom-verdict\">${esc(domainStatusText(d))}</span>`}</td>`", "state chip first, then the check verdict; a pending purchase has no verdict")
	must(t, spaFunctionLine(html, "domainStateChip"), "tr('dom.state.pending')", "pending purchase chip")
	ld := spaFunctionLine(html, "linkDomains")
	must(t, ld, "disabled", "linkDomains hides disabled")
	must(t, html, "return {id:n.id, host:n.host, group:n.group||'', role:n.role||'links', primary:!!n.primary, disabled:!!n.disabled, created_by:n.created_by||''};", "payload has writable fields only")
	must(t, html, `'dom.edit':'Править домен'`, "ru edit domain")
	must(t, html, `'dom.off.hint':'Выключенный домен`, "ru disabled hint")
}

// TestApiKeysMenu checks rotate, edit, remove and the key table.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestApiKeysMenu(t *testing.T) {
	html := spaSource()
	must(t, html, `/rotate'`, "rotate path")
	must(t, html, `'?hard=1'`, "hard delete query")
	must(t, html, `function rotateKey(`, "rotateKey")
	must(t, html, `function editKey(`, "editKey")
	must(t, html, `function removeKey(`, "removeKey")
	must(t, html, `function readKeyForm(`, "readKeyForm")
	must(t, html, `data-kcopy=`, "copy key button")
	must(t, html, `<span class="dot ${dead?'bad':'on'}"></span>`, "key status dot")
	must(t, html, `'key.scope.engine':'Движок'`, "ru engine scope")
	must(t, html, `'key.key':'API ключ'`, "ru API key column")
	must(t, html, `'key.table.hint':`, "key table hint")
	must(t, html, `'key.status':`, "key status label")
	must(t, html, `'key.prefix':`, "key prefix label")
	mustNot(t, html, `'key.scope.engine':'Движок ?api='`, "engine scope query hint")
	mustNot(t, html, `data-revoke=`, "inline revoke button")
}

// TestKeyOnceCopy checks the dialog that shows a new key once: the key and the button share one
// copy path, the key is a focusable full-width field, and the button reads «Copied» after a copy.
// t is the test handle. It returns nothing. A missing marker or dictionary entry fails the test.
func TestKeyOnceCopy(t *testing.T) {
	html := spaSource()
	fn := spaBetween(html, "function showKeyOnce(", "\nconst USER_ACCESS_SECTIONS")
	must(t, fn, `id="key-once" class="key-once" role="button" tabindex="0"`, "key is a focusable button-like field")
	must(t, fn, `title="${esc(tr('key.copy'))}" aria-label="${esc(tr('key.copy'))}"`, "hint and label go through tr")
	must(t, fn, `copyBtn.onclick=copyKey;`, "button uses the shared copy function")
	must(t, fn, `keyEl.onclick=copyKey;`, "click on the key uses the same copy function")
	must(t, fn, `e.key==='Enter'||e.key===' '`, "Enter and Space copy the key")
	must(t, fn, `copyBtn.textContent=tr('cf.ns.copied')`, "button reads Copied after copying")
	must(t, fn, `copyBtn.textContent=tr('key.copy')`, "button label returns after a pause")
	must(t, fn, `copyBtn.isConnected`, "label reset skips a closed dialog")
	mustNot(t, fn, `navigator.clipboard`, "no second clipboard implementation")
	must(t, spaFunctionLine(html, "copyText"), `if(after) after();`, "copyText reports success to the caller")
	must(t, html, `.modal .key-once{width:100%;`, "key field spans the dialog")
	must(t, html, `.upd-page .cli-row code,.key-once{display:block;min-width:0;`, "key field is a code block")
	must(t, html, `.modal .key-once{width:100%;cursor:pointer;word-break:break-all}`, "long key wraps inside the field")
	must(t, html, `.modal .key-once:focus-visible{`, "keyboard focus ring on the key")
	for _, lang := range []string{"en", "ru", "uk"} {
		if !trKeyDefined(html, lang, "cf.ns.copied") {
			t.Errorf("key cf.ns.copied missing for lang %q", lang)
		}
	}
}

// TestApiKeyDialogAccess checks the key dialog: type, role, the shared access editor, the
// dangerous actions block, the presets, the list caption and the OpenAPI and MCP docs card.
// t is the test handle. It returns nothing. A missing marker or dictionary entry fails the test.
func TestApiKeyDialogAccess(t *testing.T) {
	html := spaSource()
	must(t, html, `const KEY_ACTIONS=[['keitaro_import',false],['keitaro_purge',false],['logs_delete',false],['groups_replace',false],['cron',true],['boards',true],['landing_files',false],['lists_read',false]]`, "action list with the administrator-only flags")
	must(t, html, `id="key-type"`, "key type selector")
	must(t, html, `id="key-role"`, "key role selector")
	must(t, html, `accessEditorHTML(acc,{catalog:o.catalog})`, "key dialog reuses the user access editor")
	must(t, html, `readAccessEditor(box, false)`, "key form reads the shared access editor")
	must(t, html, `bindAccessEditor(el);`, "key dialog binds the shared access editor")
	must(t, html, `data-kact="${id}"`, "dangerous action checkboxes")
	must(t, html, `data-kpreset="read"`, "read only preset")
	must(t, html, `data-kpreset="all"`, "everything preset")
	must(t, html, `data-show-if="#key-type option[value=ADMIN]:checked"`, "cron and boards shown only for an ADMIN key")
	must(t, html, `if(el.checked&&(type==='ADMIN'||el.dataset.kadmin!=='1')) actions.push(el.dataset.kact)`, "cron and boards are not sent for a USER key")
	must(t, html, `if(type==='USER'&&!roleId) body.access=readAccessEditor(box, false);`, "own access only without a role")
	must(t, html, `function loadAccessCatalog(`, "catalog loader shared with the user access dialog")
	must(t, html, `function keyKindText(`, "key list caption")
	must(t, html, `${kind}${revoked}`, "caption under the key name")
	must(t, html, `href="/api/openapi.json" target="_blank" rel="noopener"`, "OpenAPI link opens in a new tab")
	must(t, html, `mcpServers:{emtds:{command:'emtds-mcp',env:{EMTDS_URL:origin,EMTDS_API_KEY:'em_...',EMTDS_MCP_WRITE:'0'}}}`, "MCP client config sample")
	must(t, html, `{id:'mcp', title:tr('key.docs.mcp')`, "OpenAPI and MCP card")
	keys := []string{
		"key.type", "key.type.hint", "key.role.none", "key.access.hint", "key.actions.title", "key.actions.hint",
		"key.act.adminonly", "key.preset.read", "key.preset.all", "key.kind.actions", "key.docs.mcp",
		"key.docs.b.mcp", "key.docs.b.mcp2", "key.docs.b.denied",
	}
	for _, a := range []string{"keitaro_import", "keitaro_purge", "logs_delete", "groups_replace", "cron", "boards", "landing_files", "lists_read"} {
		keys = append(keys, "key.act."+a, "key.act."+a+".hint")
	}
	for _, k := range keys {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, k) {
				t.Errorf("key %q missing for lang %q", k, lang)
			}
		}
	}
	must(t, html, `'key.preset.read':'Только чтение'`, "ru read only preset")
	must(t, html, `'key.actions.title':'Опасные действия'`, "ru dangerous actions title")
}

// TestProfileTabsSections checks profile tabs, settings sections and the tab title.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestProfileTabsSections(t *testing.T) {
	html := spaSource()
	must(t, html, `class="vseg" role="tablist"`, "profile tab list")
	must(t, html, `role="tab" data-ptab="${t.id}"`, "profile tab")
	must(t, spaBetween(html, "const SETTINGS_TABS=[", "\n];"), "{id:'profile', key:'menu.settings', route:'settings'", "the first tab is the settings tab")
	must(t, html, `<section class="setsec">`, "settings section")
	must(t, html, `'set.sec.access':'Компания и доступ'`, "ru access section")
	must(t, html, `.setsec h2{`, "section heading style")
	must(t, html, `<title></title>`, "empty document title in markup")
	mustNot(t, html, `<title>TDS</title>`, "TDS placeholder title")
	must(t, html, `a.hidden=!name&&!logo`, "brand hidden without name and logo")
	mustNot(t, html, `CustomTDS`, "CustomTDS branding")
}

// TestBrowserTabTitle checks tab title helpers and wiring in the SPA script.
func TestBrowserTabTitle(t *testing.T) {
	html := spaSource()
	script := spaBetween(html, "<script>\n", "</script>")
	mustNot(t, script, `'TDS'`, "TDS string literal in script")
	mustNot(t, script, `"TDS"`, "TDS string literal in script")
	pt := spaFunctionLine(html, "pageTitle")
	if pt == "" {
		t.Fatal("pageTitle missing")
	}
	must(t, pt, "BRAND_KNOWN", "pageTitle waits for brand")
	must(t, pt, "menu.settings", "pageTitle settings label")
	must(t, pt, "menu.integrations", "pageTitle integrations label")
	must(t, pt, "titleText(", "pageTitle builds via titleText")
	render := spaFunctionLine(html, "render")
	if render == "" {
		t.Fatal("render missing")
	}
	must(t, render, "pageTitle(page)", "render sets title before page load")
	chrome := spaFunctionLine(html, "paintChrome")
	if chrome == "" {
		t.Fatal("paintChrome missing")
	}
	must(t, chrome, "pageTitle()", "paintChrome refreshes title")
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	must(t, pure, "function titleText(", "titleText in pure block")
	must(t, pure, "n+' - '+l", "title joins name and page")
}

// TestStreamProxyAndOrConditions checks the stream editor offers proxy and IP
// version, an any-of switch, and collects every tri flag from COND.
func TestStreamProxyAndOrConditions(t *testing.T) {
	html := spaSource()
	must(t, html, `k:'proxy'`, "proxy condition")
	must(t, html, `k:'ip_version'`, "ip version condition")
	must(t, html, `id="s_filter_or"`, "and/or switch")
	must(t, html, `'any of these holds'`, "any-of label")
	body := spaFunctionLine(html, "collectStream")
	if body == "" {
		t.Fatal("collectStream not found")
	}
	mustNot(t, body, `['unique','yabrowser','has_referer','bot']`, "tri keys are not a fixed list")
	must(t, html, `'ch_proxy'`, "proxy bot check")
	must(t, html, `'ch_non_mobile'`, "non-mobile bot check")
}

// dictKeyRe matches one quoted dictionary key followed by a colon.
var dictKeyRe = regexp.MustCompile(`'((?:[^'\\]|\\.)*)'\s*:`)

var trStaticKeyRe = regexp.MustCompile(`\btr(?:Fill)?\('((?:[^'\\]|\\.)+)'\)`)

// idKeyRe matches dictionary identifier keys such as 'ed.order' (lowercase, dotted, no spaces),
// as opposed to English phrases that ru and uk translate by their own text.
var idKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// trKeyExceptions lists tr keys that stay untranslated in every language on purpose.
var trKeyExceptions = map[string]struct{}{
	"CR": {}, "CSV": {}, "HTML": {}, "ROI": {}, "TID": {}, "ASN": {}, "IP": {}, "OS": {}, "URL": {},
	"API": {}, "CDN": {}, "DNS": {}, "NS": {}, "Sub ID": {}, "User-Agent": {}, "JavaScript": {},
	"Telegram": {}, "Iframe": {}, "HTTP redirect": {}, "Meta refresh": {}, "JS redirect": {},
	"Click ID": {}, "Api-Key": {}, "HTTP": {}, "JSON": {}, "gzip": {}, "base64": {}, "RE2": {},
	"IPv4": {}, "IPv6": {}, "PTR lookup": {}, "MaxMind": {}, "GeoLite2": {}, "DB-IP Lite": {},
	"VirusTotal": {}, "Namecheap": {}, "Keitaro": {}, "ClickHouse": {}, "Postman": {},
	"Google IPs": {}, "Yandex IPs": {}, "Bing IPs": {}, "Baidu IPs": {}, "Mail.ru IPs": {},
	"Yahoo IPs": {}, "Other bot IPs": {}, "Yandex Browser": {}, "Generic S2S": {},
	"Ad Campaign ID": {}, "External ID": {}, "Creative ID": {}, "X-Requested-With": {},
	"Subid": {}, "OK": {}, "404 Not found": {}, "403 Forbidden": {}, "500 Server error": {},
}

// spaConstLangBody returns the inner source of one language object in a const map.
func spaConstLangBody(html, constName, lang string) string {
	prefix := "const " + constName + "={"
	i := strings.Index(html, prefix)
	if i < 0 {
		return ""
	}
	rest := html[i+len(prefix):]
	marker := "\n" + lang + ":{"
	j := strings.Index(rest, marker)
	if j < 0 {
		return ""
	}
	sub := rest[j+len(marker)-1:]
	if len(sub) == 0 || sub[0] != '{' {
		return ""
	}
	depth := 0
	for k := 0; k < len(sub); k++ {
		switch sub[k] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return sub[1:k]
			}
		}
	}
	return ""
}

// spaDictKeys collects dictionary keys from one language object body.
func spaDictKeys(body string) map[string]struct{} {
	if body == "" {
		return nil
	}
	return dictKeys("{" + body + "}")
}

// spaDictCache keeps the key sets of one SPA source by dictionary and language.
type spaDictCache struct {
	html string
	sets map[[2]string]map[string]struct{}
}

var (
	spaDictMu  sync.Mutex
	spaDictMem *spaDictCache
)

// spaDictKeysCached returns spaDictKeys(spaConstLangBody(html, dict, lang)).
// html is the SPA source; dict is I18N, PHRASE or PHRASE_EXTRA; lang is en, ru or uk.
// Each pair is parsed once for the same html; another html resets the cache.
// The returned set is shared and must not be changed. It is nil when the object is missing.
func spaDictKeysCached(html, dict, lang string) map[string]struct{} {
	spaDictMu.Lock()
	defer spaDictMu.Unlock()
	if spaDictMem == nil || spaDictMem.html != html {
		spaDictMem = &spaDictCache{html: html, sets: map[[2]string]map[string]struct{}{}}
	}
	k := [2]string{dict, lang}
	set, ok := spaDictMem.sets[k]
	if !ok {
		set = spaDictKeys(spaConstLangBody(html, dict, lang))
		spaDictMem.sets[k] = set
	}
	return set
}

// trKeyDefined reports whether tr() can resolve a key for lang without falling back to the raw key.
func trKeyDefined(html, lang, key string) bool {
	if _, ok := trKeyExceptions[key]; ok {
		return true
	}
	check := func(dict, l string) bool {
		_, ok := spaDictKeysCached(html, dict, l)[key]
		return ok
	}
	if check("I18N", lang) {
		return true
	}
	if lang != "en" {
		if check("PHRASE", lang) || check("PHRASE_EXTRA", lang) {
			return true
		}
		if check("I18N", "en") {
			return true
		}
		return false
	}
	if check("I18N", "en") {
		return true
	}
	// An identifier key such as 'ed.order' has no English text of its own: tr() would show the key itself.
	if idKeyRe.MatchString(key) {
		return check("PHRASE", "en") || check("PHRASE_EXTRA", "en")
	}
	// English UI keys often live only in PHRASE (ru/uk translate the English key text).
	return check("PHRASE", "ru") || check("PHRASE", "uk") || check("PHRASE_EXTRA", "ru") || check("PHRASE_EXTRA", "uk")
}

// spaTrStaticKeys lists literal keys passed to tr() / trFill() in the SPA script.
func spaTrStaticKeys(html string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range trStaticKeyRe.FindAllStringSubmatch(html, -1) {
		k := m[1]
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// TestTrStaticKeys ensures every literal tr('…') key resolves in en, ru and uk.
func TestTrStaticKeys(t *testing.T) {
	html := spaSource()
	keys := spaTrStaticKeys(html)
	if len(keys) < 100 {
		t.Fatalf("too few tr keys: %d", len(keys))
	}
	for _, lang := range []string{"en", "ru", "uk"} {
		for _, key := range keys {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("tr(%q) missing for lang %q", key, lang)
			}
		}
	}
}

// TestTrIdentifierKeysInEnglish checks that the identifier keys the editor and the tag lists read
// (some of them through a condition, so no literal tr('…') call shows them) have English text,
// not only ru and uk: tr() would otherwise show the bare key.
func TestTrIdentifierKeysInEnglish(t *testing.T) {
	html := spaSource()
	for _, key := range []string{"ed.order", "ed.full", "ed.full.off", "tags.less", "tags.more"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("tr(%q) missing for lang %q", key, lang)
			}
		}
		if n := strings.Count(html, "'"+key+"':"); n < 3 {
			t.Errorf("%s is defined %d times, want en, ru and uk", key, n)
		}
	}
}

// dictBetween returns the SPA source from start up to end.
// html is the embedded page. start and end are unique markers; end is excluded.
// A missing marker fails the test.
func dictBetween(t *testing.T, html, start, end string) string {
	t.Helper()
	i := strings.Index(html, start)
	if i < 0 {
		t.Fatalf("missing %s", start)
	}
	rest := html[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("missing end after %s", start)
	}
	return rest[:j]
}

// dictDupes fails when one object repeats a quoted key.
// body is the object source. name is the dictionary in the failure.
func dictDupes(t *testing.T, body, name string) {
	t.Helper()
	seen := map[string]int{}
	for _, m := range dictKeyRe.FindAllStringSubmatch(body, -1) {
		seen[m[1]]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("duplicate key %q (%d) in %s", k, n, name)
		}
	}
}

// TestTerms checks campaign wording, breakdown captions and unique dictionary keys.
func TestTerms(t *testing.T) {
	html := spaSource()
	dicts := dictBetween(t, html, "const PHRASE_EXTRA={", "const TYPES=")
	mustNot(t, dicts, "Ветк", "russian branch word")
	mustNot(t, dicts, "Гілк", "ukrainian branch word")
	mustNot(t, dicts, "'№'", "number sign used as an id heading")
	must(t, html, "'Flow':'Campaign'", "english flow key reads campaign")
	must(t, html, "domain:'Domains',org:'Providers'", "breakdown captions")
	mustNot(t, html, "domain:'Sources'", "domain breakdown is not sources")
	mustNot(t, html, "org:'Networks'", "provider breakdown is not networks")
	i18n := dictBetween(t, html, "const I18N={", "const PHRASE={")
	phrase := dictBetween(t, html, "const PHRASE={", "const TYPES=")
	extra := dictBetween(t, html, "const PHRASE_EXTRA={", "function tr(")
	dictDupes(t, dictBetween(t, i18n, "en:{", "\n},\nru:{"), "I18N.en")
	dictDupes(t, dictBetween(t, i18n, "\nru:{", "\n},\nuk:{"), "I18N.ru")
	dictDupes(t, dictBetween(t, i18n, "\nuk:{", "\n};"), "I18N.uk")
	dictDupes(t, dictBetween(t, phrase, "\nru:{", "\n},\nuk:{"), "PHRASE.ru")
	dictDupes(t, dictBetween(t, phrase, "\nuk:{", "\n};"), "PHRASE.uk")
	dictDupes(t, dictBetween(t, extra, "\nru:{", "\n},\nuk:{"), "PHRASE_EXTRA.ru")
	dictDupes(t, dictBetween(t, extra, "\nuk:{", "\n},\nen:{"), "PHRASE_EXTRA.uk")
	dictDupes(t, dictBetween(t, extra, "\nen:{", "\n};"), "PHRASE_EXTRA.en")
	sameDictKeys(t, dictKeys(dictBetween(t, i18n, "en:{", "\n},\nru:{")), dictKeys(dictBetween(t, i18n, "\nru:{", "\n},\nuk:{")), "I18N.en", "I18N.ru")
	sameDictKeys(t, dictKeys(dictBetween(t, i18n, "en:{", "\n},\nru:{")), dictKeys(dictBetween(t, i18n, "\nuk:{", "\n};")), "I18N.en", "I18N.uk")
	sameDictKeys(t, dictKeys(dictBetween(t, phrase, "\nru:{", "\n},\nuk:{")), dictKeys(dictBetween(t, phrase, "\nuk:{", "\n};")), "PHRASE.ru", "PHRASE.uk")
}

// dictKeys collects quoted keys from one dictionary object.
// body is the object source without the surrounding name. It returns the key set.
func dictKeys(body string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range dictKeyRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = struct{}{}
	}
	return out
}

// sameDictKeys fails when two dictionaries do not list the same keys.
// a and b are key sets. an and bn name them in the failure. It returns nothing.
func sameDictKeys(t *testing.T, a, b map[string]struct{}, an, bn string) {
	t.Helper()
	for k := range a {
		if _, ok := b[k]; !ok {
			t.Errorf("key %q in %s missing from %s", k, an, bn)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			t.Errorf("key %q in %s missing from %s", k, bn, an)
		}
	}
}

// TestSPALiterals checks that known visible sentences go through tr.
// t is the test handle. It returns nothing. A leftover placeholder fails the test.
// TestFlowFooter checks the campaign canvas strip labels revenue and reads cost.
// t is the test handle. It returns nothing and fails when the footer markup regresses.
func TestFlowFooter(t *testing.T) {
	html := spaSource()
	must(t, html, `tr('col.revenue')`, "campaign footer labels revenue")
	must(t, html, `campaignCostOnly`, "campaign footer reads click and manual cost")
	mustNot(t, html, `${moneyText(p.profit,'USD')}</b>${tr('profit')}`, "footer no longer calls revenue profit")
}

func TestSPALiterals(t *testing.T) {
	html := spaSource()
	must(t, html, `function serviceBadge(`, "service badge helper")
	must(t, html, `CRONST.telegram_configured`, "telegram readiness from the panel")
	must(t, html, `value="uk"`, "ukrainian language option")
	must(t, html, `j.error`, "job error is read")
	for _, lit := range []string{
		`placeholder="Choose a flow and a day"`,
		`Nothing collected for this flow on this day`,
		`Turn collection on in a flow's settings`,
		`>No lists in the data directory<`,
		`placeholder="IP address, v4 or v6"`,
		`>service running<`,
		`>service not running<`,
		`Has not run yet`,
		`>Runs every<`,
		`>Enter an IP address to test<`,
		`>No conversions in this period<`,
		`>Conversions are not available — is ClickHouse connected?<`,
		`>Where alerts go. Create a bot`,
		`>Not in any .dat list<`,
		`Look up</button>`,
		`placeholder='Empty list'`,
		`CRON.telegram.token&&CRON.telegram.chat_id`,
		`buck||'USD'`,
	} {
		mustNot(t, html, lit, lit)
	}
}

// TestSPASaveBarEntities checks that the save bar names changed campaigns as #number name,
// opens them through the slug route and that opening an editor does not mark a campaign changed.
func TestSPASaveBarEntities(t *testing.T) {
	html := spaSource()
	bar := spaFunctionLine(html, "syncSaveBar")
	must(t, bar, "dirtyEntries([...DIRTY]", "the bar lists the changed campaigns")
	must(t, bar, `id="dchips"`, "item container")
	must(t, bar, "go(b.dataset.dgo)", "a click opens the item through the router")
	must(t, bar, "capFirst(tr('flows'))", "fallback text without items")

	chips := spaFunctionLine(html, "dirtyChipsHTML")
	must(t, chips, "data-dgo=", "item carries its router path")
	must(t, chips, "pageAllowed(ME,e.route.split('/')[0])", "no link to a page the session may not open")
	mustNot(t, chips, "e.ref", "the number is only text, never part of the route")

	ent := spaFunctionLine(html, "dirtyEntries")
	must(t, ent, "'flows/'+encodeURIComponent(id)", "campaign route is the slug")
	mustNot(t, ent, "'flows/'+num", "a number is never the campaign address")

	settle := spaFunctionLine(html, "settleGroups")
	must(t, settle, "SNAP.set(x,", "an untouched campaign adopts the normalised snapshot")
	must(t, settle, "!DIRTY.has(x)", "a changed campaign keeps its snapshot")

	paint := spaFunctionLine(html, "openFlowSettings")
	must(t, paint, "settleGroups(collectFlow())", "settings drawer settles after filling the form")
	must(t, paint, "paintLink(true)", "the first link paint is not an edit")
	mustNot(t, paint, "if(touched) markDirty(touched)", "painting a link does not mark the campaign")

	stream := spaFunctionLine(html, "openStream")
	must(t, stream, "collectStream(); settleGroups(g)", "stream drawer settles after opening")

	for _, k := range []string{"dirty.title", "dirty.flow", "dirty.removed", "dirty.all", "dirty.less"} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("%s is defined %d times, want 3 (en, ru, uk)", k, n)
		}
	}
}

// TestKeepUnknownPick checks that every catalog select keeps a saved id its catalog lacks:
// the campaign domain and source, the stream offer and landing shares, and the offer's network.
func TestKeepUnknownPick(t *testing.T) {
	html := spaSource()
	must(t, spaFunctionLine(html, "domainOpts"), "missingOpt(list, g.domain_id, 'pick.missing.dom')", "the domain select keeps an unknown domain")
	must(t, spaFunctionLine(html, "offerSelect"), "missingOpt(OFFERS,id,'pick.missing.offer')", "the offer select keeps an unknown offer")
	must(t, spaFunctionLine(html, "landingSelect"), "missingOpt(LANDINGS,id,'pick.missing.landing')", "the landing select keeps an unknown landing")
	must(t, html, "missingOpt(sources, g.source_id, 'pick.missing.src')", "the source select keeps an unknown source")
	must(t, html, "missingOpt(nets,cur?cur.network_id:'','pick.missing.net')", "the network select keeps an unknown network")
	must(t, spaFunctionLine(html, "missingOpt"), "unknownPick(rows, value)", "one helper decides what is unknown")
	must(t, spaFunctionLine(html, "flowPickOpts"), "missingOpt(rows,cur,'pick.missing.flow')", "the campaign picker keeps an unknown campaign")
	must(t, html, "sel('g_group','<option value=\"\">'+tr('— choose a flow —')+'</option>'+flowPickOpts(others,g.out),'data-pick')", "the campaign's send-to select keeps an unknown campaign")
	must(t, spaFunctionLine(html, "thenActionField"), "others.length||unknownPick(others,(o.out||'').trim())?sel('s_group','<option value=\"\">'+tr('— choose a flow —')+'</option>'+flowPickOpts(others,o.out),'data-pick')", "the stream's send-to select keeps an unknown campaign, even with no other campaign")
	must(t, html, "'pick.missing.flow':'Кампания #{id} (не найдена)'", "russian label of an unknown campaign")
	must(t, html, "'pick.missing.flow':'Кампанія #{id} (не знайдено)'", "ukrainian label of an unknown campaign")
	must(t, html, "option.miss,select:has(option.miss:checked){color:var(--muted)}", "the kept option is muted")
	for _, k := range []string{"pick.missing.dom", "pick.missing.offer", "pick.missing.landing", "pick.missing.net", "pick.missing.src", "pick.missing.flow"} {
		if n := strings.Count(html, "'"+k+"':"); n != 3 {
			t.Errorf("%s is defined %d times, want 3 (en, ru, uk)", k, n)
		}
	}
	must(t, html, "'pick.missing.dom':'Домен #{id} (не найден)'", "russian label of an unknown domain")
	must(t, html, "'pick.missing.dom':'Домен #{id} (не знайдено)'", "ukrainian label of an unknown domain")
}

// TestEnsureNumsSource checks that list numbering is a baseline for existing campaigns,
// and that creating or copying a campaign saves the numbers it builds on.
func TestEnsureNumsSource(t *testing.T) {
	html := spaSource()
	nums := spaFunctionLine(html, "ensureNums")
	must(t, nums, "if(persist) markDirty(changed); else settleGroups(changed)", "list numbering is a baseline, a create saves it")
	must(t, spaFunctionLine(html, "newFlow"), "ensureNums(true)", "a new campaign saves the numbers it builds on")
	must(t, spaFunctionLine(html, "duplicateFlow"), "ensureNums(true)", "a copy saves the numbers it builds on")
}
