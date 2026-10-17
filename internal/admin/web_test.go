package admin

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

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
	html := string(indexHTML)

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
	must(t, html, `localStorage.getItem('kuztds-theme')`, "remembered theme")

	// Dashboard: the performance table by flow and stream.
	must(t, html, `/api/stats/performance`, "performance request")
	must(t, html, `class="perft"`, "performance table")

	// Flows: table by default, cards on demand, then a canvas per flow.
	must(t, html, `id="fgrid"`, "flow list")
	must(t, html, `FLOW_VIEW='table'`, "flows table by default")
	must(t, html, `kuztds-flows-view`, "remembered flows layout")
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
	html := string(indexHTML)
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
	html := string(indexHTML)
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
	html := string(indexHTML)
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
	appLine := ""
	if i := strings.Index(html, "function app(user)"); i >= 0 {
		end := strings.Index(html[i:], "\nfunction ")
		if end < 0 {
			end = strings.Index(html[i:], "\nasync function ")
		}
		if end > 0 {
			appLine = html[i : i+end]
		}
	}
	must(t, appLine, `tr('menu.integrations')`, "app menu integrations")
	mustNot(t, appLine, `tr('menu.keitaro')`, "app menu without keitaro")
	pagesAt := strings.Index(html, "const PAGES=")
	pagesEnd := strings.Index(html[pagesAt:], "\n")
	must(t, html[pagesAt:pagesAt+pagesEnd], "keitaro:integrations", "keitaro route alias")
	set := spaFunctionLine(html, "settings")
	must(t, set, `id="company-card"`, "company settings card")
	must(t, html, "defaultCompany", "default company helper")
	must(t, html, `let BRAND={name:''`, "BRAND defaults to an empty name")
}

// TestDashboardPerfControls — performance table defaults, pure helpers, export scope, KPI tiles, and fold nav.
func TestDashboardPerfControls(t *testing.T) {
	html := string(indexHTML)
	must(t, html, `PSORT={k:'conv'`, "default perf sort by conversions")
	must(t, html, `let PERF_TOP=true, PERF_FOLD=true`, "perf fold and top flags")
	must(t, html, `const PERF_TOP_N=10`, "perf top limit")
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	must(t, pure, "function perfTop(", "perfTop in pure block")
	must(t, pure, "function perfCmp(", "perfCmp in pure block")
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	must(t, dash, `id="perftop"`, "perf top button")
	must(t, dash, `id="perffold"`, "perf fold button")
	sorted := spaFunctionLine(html, "perfSorted")
	must(t, sorted, "perfCmp(", "perfSorted uses perfCmp")
	render := spaFunctionLine(html, "renderPerf")
	must(t, render, "PERF_FOLD", "renderPerf respects fold flag")
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

// TestDashboardChartAxes — chart axes come from shared scales, and percent series are stroked.
func TestDashboardChartAxes(t *testing.T) {
	html := string(indexHTML)
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
	mustNot(t, series, "fact:true", "fact flag on a series")
	mustNot(t, series, "money:true", "money flag on a series")
	tip := spaFunctionLine(html, "chartTip")
	must(t, tip, "sw-cr", "tooltip marks conversion rate")
	must(t, tip, "periodROI()", "tooltip uses period ROI")
	roi := spaFunctionLine(html, "metricROI")
	must(t, roi, "roiPct(", "ROI metric uses roiPct")
}

// TestEditorsDoNotMoveThePage pins a property the interface was rebuilt for
// twice: opening something must not scroll or shift what is under it. Editors
// are drawers that overlay the page and scroll inside themselves; if
// scrollIntoView reappears, a layout that needs chasing has crept back in.
func TestEditorsDoNotMoveThePage(t *testing.T) {
	html := string(indexHTML)
	mustNot(t, html, ".scrollIntoView(", "page-scrolling hack")
	must(t, html, `.drawer{position:fixed`, "the drawer overlays the page")
	must(t, html, `.db{flex:1;overflow-y:auto`, "the drawer body scrolls inside itself")
	must(t, html, `scrollbar-gutter:stable`, "no width jump when a page gets a scrollbar")
}

func must(t *testing.T, html, needle, what string) {
	t.Helper()
	if !strings.Contains(html, needle) {
		t.Errorf("not found in SPA: %s (looked for %q)", what, needle)
	}
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
	if end < 0 {
		end = strings.Index(rest, "\nconst ")
	}
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// TestSettingsMasonry — settings cards keep content height in two columns.
func TestSettingsMasonry(t *testing.T) {
	html := string(indexHTML)
	src := spaFunctionLine(html, "settings")
	if src == "" {
		t.Fatal("settings not found in SPA")
	}
	must(t, src, `class="bds setgrid"`, "settings wrapper is setgrid")
	mustNot(t, src, `grid-template-columns:repeat(auto-fit,minmax(340px,440px))`, "settings drops the stretched grid")
	must(t, html, `.bds.setgrid{display:block;column-count:2`, "two-column masonry")
	must(t, html, `break-inside:avoid`, "card does not split across columns")
	must(t, html, `@media (max-width:760px){.bds.setgrid{column-count:1}}`, "one column below 760px")
	must(t, html, `.bds{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:12px}`, "default board grid stays")
}

// TestCampaignMoneyAndROIDisplay — money and ROI cells show zeros, not em dashes.
func TestCampaignMoneyAndROIDisplay(t *testing.T) {
	html := string(indexHTML)
	mt := spaFunctionLine(html, "moneyText")
	cm := spaFunctionLine(html, "campaignMoney")
	cr := spaFunctionLine(html, "metricROI")
	ratio := spaFunctionLine(html, "cellRatio")
	if mt == "" || cm == "" || cr == "" || ratio == "" {
		t.Fatal("moneyText, campaignMoney, metricROI or cellRatio not found in SPA")
	}
	must(t, cm, "moneyMap(by,4)", "campaignMoney uses moneyMap with 4 digits")
	must(t, mt, ".toFixed(d)+' '+suf", "moneyText formats amount and currency suffix")
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
	must(t, html, `<select id="dgroup"`, "domain group select")
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
	html := string(indexHTML)
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
	html := string(indexHTML)
	open := spaFunctionLine(html, "openFlowSettings")
	collect := spaFunctionLine(html, "collectFlow")
	cell := spaFunctionLine(html, "flowMetricCell")
	cat := spaFunctionLine(html, "colCatalog")
	bands := spaFunctionLine(html, "flowBands")
	if open == "" || collect == "" || cell == "" || cat == "" || bands == "" {
		t.Fatal("openFlowSettings, collectFlow, flowMetricCell, colCatalog or flowBands not found in SPA")
	}
	must(t, open, `id="g_folder"`, "editor folder input")
	must(t, open, `id="g_folders"`, "editor folder datalist")
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

// TestFlowsEPCDefault — flows columns default to EPC, the summary chip is revenue per click, and the uEPC cell formula stays rev/uniq.
func TestFlowsEPCDefault(t *testing.T) {
	html := string(indexHTML)
	cat := spaFunctionLine(html, "colCatalog")
	sum := spaFunctionLine(html, "paintFlowSum")
	cell := spaFunctionLine(html, "flowMetricCell")
	if cat == "" || sum == "" || cell == "" {
		t.Fatal("colCatalog, paintFlowSum or flowMetricCell not found in SPA")
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
	must(t, html, "const FLOW_COLS_MIGRATED='kuztds-flow-cols-epc'", "one-time migration flag")
	must(t, cols, "FLOW_COLS_MIGRATED", "migration runs once per browser")
}

// TestSpendShareByOverlap — tiles and tables take the share of a spend record that overlaps the period.
func TestSpendShareByOverlap(t *testing.T) {
	html := string(indexHTML)
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
	html := string(indexHTML)
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
	must(t, html, "'money.nocur':'no currency'", "english no-currency label")
	must(t, html, "'money.nocur':'без валюты'", "russian no-currency label")
	must(t, html, "'money.nocur':'без валюти'", "ukrainian no-currency label")
	must(t, html, "'col.rej.hint':'Postbacks with status rejected'", "english rejected hint")
	must(t, html, "'col.rej.hint':'Постбеки со статусом rejected'", "russian rejected hint")
	must(t, html, "'col.rej.hint':'Постбеки зі статусом rejected'", "ukrainian rejected hint")
}

// TestCatalogNames — breakdowns, the report and conversions show catalog names, and an open campaign refetches its rows.
func TestCatalogNames(t *testing.T) {
	html := string(indexHTML)
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
	must(t, conv, "f:'campaign_id'", "conversion filter uses campaign id")
	mustNot(t, conv, "f:'campaign',", "conversion filter is not the campaign name")
	must(t, conv, "nameOf(GROUPS, PB.campaignId)", "conversion title is the campaign name")
	must(t, pb, "r.count", "conversion counter uses the filtered count")
	must(t, pb, "typeof r.total==='number'", "missing profit total is not drawn as zero")
	must(t, one, "campaign_id", "open campaign refetches its performance")
	must(t, page, "loadPerfFor(", "flow page loads that campaign before drawing")
	must(t, html, "#/flows/", "campaign address stays a slug path")
}

// TestNavExportAndCampaignTable — icon nav, HTML export, period label, campaign table.
func TestNavExportAndCampaignTable(t *testing.T) {
	html := string(indexHTML)
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
	html := string(indexHTML)
	flows := spaFunctionLine(html, "flows")
	if flows == "" {
		t.Fatal("flows missing")
	}
	must(t, flows, `id="flowexport"`, "export menu button")
	must(t, flows, "exportFlows(false)", "CSV export action")
	must(t, flows, "exportFlows(true)", "HTML export action")
	must(t, flows, "tr('Create')", "create button label")
	must(t, flows, `id="fbar"`, "filter bar")
	must(t, flows, `id="fsource"`, "source filter")
	must(t, flows, `id="ffolder"`, "folder filter")
	must(t, flows, `id="fstate"`, "state filter")
	mustNot(t, flows, `id="flowcsv"`, "separate CSV export button")
	mustNot(t, flows, `id="flowhtml"`, "separate HTML export button")
	fbar := spaBetween(flows, `id="fbar"`, `id="fgrid"`)
	must(t, fbar, `id="newflow"`, "create in filter bar")
	must(t, fbar, `id="fq"`, "search in filter bar")
	nw := strings.Index(fbar, `id="newflow"`)
	fq := strings.Index(fbar, `id="fq"`)
	if nw < 0 || fq < 0 || nw >= fq {
		t.Errorf("filter bar order create < search: new=%d search=%d", nw, fq)
	}
	ph := spaBetween(flows, `<div class="ph">`, `</div>
    <div class="toolbar" id="fbar">`)
	mustNot(t, ph, `id="newflow"`, "create not in page header")
	mustNot(t, ph, `id="fq"`, "search not in page header")
	page := strings.Index(flows, `id="flowpage"`)
	exp := strings.Index(flows, `id="flowexport"`)
	if page < 0 || exp < 0 || exp <= page {
		t.Errorf("export must follow the pager: page=%d export=%d", page, exp)
	}
	cards := spaFunctionLine(html, "paintFlowCards")
	must(t, cards, `id="bulk-on"`, "bulk enable")
	must(t, cards, `id="bulk-off"`, "bulk disable")
	must(t, cards, `id="bulk-del"`, "bulk delete")
	mustNot(t, cards, `class="sm" id="bulk-`, "small class on a bulk action")
	mustNot(t, cards, `class="sm danger" id="bulk-del"`, "small danger class on bulk delete")
	must(t, html, `.ftable td.fname{font-size:11px}`, "name cell is 11px")
	cell := spaFunctionLine(html, "flowMetricCell")
	must(t, cell, `class="ell fname"`, "name cell class")
	menuFn := spaFunctionLine(html, "menu")
	must(t, menuFn, "r.top-6-h", "menu can open above its anchor")
	must(t, menuFn, "includes('end')", "menu end alignment")
	must(t, html, ".menu.end{min-width:0}", "export menu css")
	must(t, flows, "menu(e.currentTarget,[{label:tr('Export CSV')", "export menu anchor")
	must(t, flows, "exportFlows(true)}], 'end')", "export menu end class")
}

// TestSpaPureAndConvMarkers checks sort helpers, conversion labels, dates, and country data markers.
func TestSpaPureAndConvMarkers(t *testing.T) {
	html := string(indexHTML)
	pure := spaBetween(html, "// ---- pure: begin ----", "// ---- pure: end ----")
	must(t, pure, "function nameSortKey(", "nameSortKey in pure block")
	must(t, pure, "function capFirst(", "capFirst in pure block")
	rows := spaFunctionLine(html, "campaignRows")
	must(t, rows, "nameSortKey(campaignLabel", "name sort uses nameSortKey")
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
	html := string(indexHTML)
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
	html := string(indexHTML)
	menuAt := strings.Index(html, `q('#userbtn',root).onclick`)
	if menuAt < 0 {
		t.Fatal("user menu missing")
	}
	menuEnd := strings.Index(html[menuAt:], "\n")
	if menuEnd < 0 {
		t.Fatal("user menu line has no newline")
	}
	menuLine := html[menuAt : menuAt+menuEnd]
	pbAt := strings.Index(menuLine, `tr('pb.title')`)
	intAt := strings.Index(menuLine, `tr('menu.integrations')`)
	setAt := strings.Index(menuLine, `tr('menu.settings')`)
	if pbAt < 0 || intAt < 0 || setAt < 0 || pbAt >= intAt || intAt >= setAt {
		t.Errorf("integrations must sit between postbacks and settings in the user menu")
	}
	mustNot(t, menuLine, `tr('menu.keitaro')`, "user menu uses integrations, not keitaro")
	src := spaFunctionLine(html, "keitaroPanel")
	if src == "" {
		t.Fatal("keitaro panel missing")
	}
	must(t, spaFunctionLine(html, "integrations"), `id="int-keitaro"`, "integrations page")
	for _, needle := range []string{
		"/api/keitaro/status",
		"/api/keitaro/check",
		"/api/keitaro/import",
		"/api/keitaro/purge",
		"/api/keitaro/job",
		`id="kt-progress"`,
		"<progress",
		`id="kt-campaigns"`,
		`id="kt-conversions"`,
		`id="kt-from"`,
		"kt.col.count",
	} {
		must(t, src, needle, "keitaro page")
	}
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
		`'menu.settings':'Profile'`,
		`'menu.settings':'Профиль'`,
		`'menu.settings':'Профіль'`,
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
	}
}

// TestStreamThen checks the Then mode tabs, share rows and shared S2S templates.
func TestStreamThen(t *testing.T) {
	html := string(indexHTML)
	must(t, html, `.share-row`, "share row grid")
	must(t, html, `max="100"`, "share percent ceiling")
	must(t, html, `class="icon ghost" data-delshare`, "remove button on a share row")
	for _, key := range []string{"then.mode.offers", "then.mode.url", "then.mode.action"} {
		must(t, html, key, "then mode label")
	}
	must(t, html, `/api/s2s`, "shared postback catalog")
	must(t, spaFunctionLine(html, "collectFlow"), `postback_ids`, "campaign stores chosen templates")
	mustNot(t, html, `/api/campaigns/`, "campaign postback route is not used by the SPA")
	must(t, html, `class="types"`, "then modes keep the type tabs")
	must(t, html, `data-then=`, "then mode switch")
}

// TestSPAFlowCompact checks compact flow canvas toggle, i18n, breakdown label tooltips.
func TestSPAFlowCompact(t *testing.T) {
	html := string(indexHTML)
	must(t, html, "FLOW_COMPACT_KEY", "flow compact localStorage key")
	must(t, html, "canvas-toggle", "compact canvas toggle button")
	must(t, html, "'flow.compact'", "flow.compact i18n")
	must(t, html, "flowCompactClass(", "flowCompactClass helper")
	must(t, html, `class="k" title="${esc(label)}"`, "breakdown title shows label")
}

// TestSPAUsersPage checks the users tab, access helpers, Keitaro users import,
// and that menu.users appears in all three interface dictionaries.
func TestSPAUsersPage(t *testing.T) {
	html := string(indexHTML)
	must(t, html, `data-ptab="users"`, "profile users tab")
	must(t, html, `id="kt-users"`, "keitaro users checkbox")
	must(t, html, "/api/users", "users API")
	must(t, html, "hiddenCols(", "hiddenCols helper")
	must(t, html, "pageAllowed(", "pageAllowed helper")
	must(t, html, "canEdit(", "canEdit helper")
	must(t, html, "userIsEnv(", "userIsEnv helper")
	must(t, html, "'users.src.env'", "users.src.env i18n")
	must(t, html, "'prof.account'", "prof.account i18n")
	for _, anchor := range []string{
		`'menu.users':'Users'`,
		`'menu.users':'Пользователи'`,
		`'menu.users':'Користувачі'`,
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
	html := string(indexHTML)
	for _, needle := range []string{"/api/pages/", "tr('page.open')", "@page:"} {
		must(t, html, needle, "stream page editor")
	}
}

// TestCodeEditorAndFilepick — Ace loads dynamically, landing editor sits above the scrim, file inputs use filepick.
func TestCodeEditorAndFilepick(t *testing.T) {
	html := string(indexHTML)
	must(t, html, "/assets/ace/ace.js", "ace asset path")
	must(t, html, "function codeEditor", "codeEditor helper")
	must(t, html, `class="filepick"`, "filepick wrapper")
	must(t, html, "ed.errors", "editor syntax error i18n")
	mustNot(t, html, "<script src=", "ace scripts are injected only")
	land := spaBetween(html, "async function openLandingFile", "\nasync function landings")
	must(t, land, "z-index:81", "landing file modal z-index")
}

// TestLandingTextAndDeleteConfirm checks LAND_TEXT matches server landingTextExt,
// landingText has no .htaccess shortcut, and delete asks with name and id.
func TestLandingTextAndDeleteConfirm(t *testing.T) {
	html := string(indexHTML)
	must(t, html, "const LAND_TEXT=new Set(['html','htm','php','css','js','txt','json','xml','svg'])", "LAND_TEXT matches server landingTextExt")
	lt := spaBetween(html, "function landingText(path)", "\n// landingFileURL")
	mustNot(t, lt, ".htaccess", "landingText must not treat .htaccess as text")
	land := spaBetween(html, "async function landings(", "let NET_SORT")
	must(t, land, "(ID '+id+')?", "landings delete confirm shows name and id")
}

// TestSPASettingsPickers — country and timezone search boxes and the settings
// postback card are gone. The lead URL in the postback popup has no payout,
// the timezone letter buffer resets after 3 seconds, and the theme-pick
// handler sets data-theme without calling toggleTheme.
func TestSPASettingsPickers(t *testing.T) {
	html := string(indexHTML)
	mustNot(t, html, `id="cfind"`, "country search field")
	mustNot(t, html, `id="tzfind"`, "timezone search field")
	mustNot(t, html, `id="sp-find"`, "spend timezone search")
	mustNot(t, html, `id="pb-card"`, "postback card in settings")
	ep := spaFunctionLine(html, "enginePostback")
	must(t, ep, "&payout={payout}", "engine postback keeps payout")
	pb := spaFunctionLine(html, "openPostbacks")
	if pb == "" {
		t.Fatal("openPostbacks missing")
	}
	must(t, pb, "status=lead", "lead postback in the popup")
	leadOK := false
	for _, line := range strings.Split(pb, "\n") {
		if !strings.Contains(line, "status=lead") {
			continue
		}
		leadOK = true
		if strings.Contains(line, "payout") {
			t.Errorf("lead url must not include payout: %s", line)
		}
	}
	if !leadOK {
		t.Error("lead url line not found")
	}
	must(t, pb, "status=sale&payout={payout}", "sale postback keeps payout")
	menuAt := strings.Index(html, `q('#userbtn'`)
	if menuAt < 0 {
		t.Fatal("user menu missing")
	}
	menuEnd := strings.Index(html[menuAt:], "\n")
	menuLine := html[menuAt : menuAt+menuEnd]
	pbAt := strings.Index(menuLine, `tr('pb.title')`)
	setAt := strings.Index(menuLine, `tr('menu.settings')`)
	if pbAt < 0 || setAt < 0 || pbAt > setAt {
		t.Errorf("postbacks must sit above settings in the user menu")
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

// TestNavIconTooltips checks nav tooltips only while icon mode hides labels.
func TestNavIconTooltips(t *testing.T) {
	html := string(indexHTML)
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
// Campaign ids stay hits, uniq and conv. The report catalog has no cost column.
// The URL builder stops at sub_id_15 and does not offer status.
func TestOfferFormAndColumns(t *testing.T) {
	html := string(indexHTML)
	flows := catalogBranch(html, "flows")
	must(t, flows, "['hits'", "campaign column id hits")
	must(t, flows, "['uniq'", "campaign column id uniq")
	must(t, flows, "['conv'", "campaign column id conv")
	must(t, html, "kuztds-offer-cols", "offer column storage key")
	must(t, html, "kuztds-net-cols", "network column storage key")
	rep := catalogBranch(html, "report")
	mustNot(t, rep, "['cost'", "report catalog must not list cost")
	macros := offerMacroSource(html)
	must(t, macros, "{subid}", "offer URL macro subid")
	must(t, macros, "{sub_id_15}", "offer URL macro sub_id_15")
	mustNot(t, macros, "{sub_id_16}", "offer URL builder has no sub_id_16")
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

// TestCatalogSearchAndGroups checks shared catalog filters, group window, and save bodies.
func TestCatalogSearchAndGroups(t *testing.T) {
	html := string(indexHTML)
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

// TestSPALogFilters checks the journal filter builder, column ids and the conversion page size.
func TestSPALogFilters(t *testing.T) {
	html := string(indexHTML)
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
	must(t, logs, "'sub_id_12'", "click log sub id column")
	cols := spaFunctionLine(html, "loadCols")
	must(t, cols, "group:'campaign'", "saved campaign column id")
	must(t, cols, "ua:'useragent'", "saved user-agent column id")
	must(t, cols, "sub_id_", "saved sub id column migration")
	pb := spaFunctionLine(html, "pbQuery")
	must(t, pb, "offset", "conversion query offset")
	must(t, html, "'fld.sale_period'", "sale period label")
	must(t, html, "'flt.apply'", "apply label")
	rep := catalogBranch(html, "report")
	must(t, rep, "['group','conv.campaign',1]", "report catalog stays the last return")
	must(t, html, `<select id="dgroup"`, "domain group select")
}

// TestSPAProfileAndReportFilters checks the profile page, API keys and report filters.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestSPAProfileAndReportFilters(t *testing.T) {
	html := string(indexHTML)
	must(t, html, "function profile(", "profile page")
	must(t, html, "/api/api-keys", "api key routes")
	must(t, html, "#/settings/docs", "docs hash")
	must(t, html, "#/settings/keys", "keys hash")
	must(t, html, "Api-Key", "api key header")
	must(t, html, "REP.conds", "report conditions")
	rep := spaBetween(html, "async function report(", "async function keitaro(")
	dash := spaBetween(html, "async function dashboard(", "async function loadBreakdown(")
	if rep == "" || dash == "" {
		t.Fatal("report or dashboard missing")
	}
	must(t, rep, "filterBuilder('events'", "report filter builder")
	must(t, dash, "filterBuilder('events'", "dashboard filter builder")
	must(t, html, "kuztds-dash-dim", "dashboard custom dimension memory")
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
		`'menu.settings':'Profile'`,
		`'menu.settings':'Профиль'`,
		`'menu.settings':'Профіль'`,
	} {
		if !strings.Contains(html, anchor) {
			t.Errorf("menu label missing: %s", anchor)
		}
	}
}

// TestAppearanceAccents checks accent tokens, the swatch picker and row hover.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestAppearanceAccents(t *testing.T) {
	html := string(indexHTML)
	must(t, html, `:root[data-accent="blue"]`, "blue accent tokens")
	must(t, html, `:root[data-accent="green"]`, "green accent tokens")
	must(t, html, `:root[data-accent="graphite"]`, "graphite accent tokens")
	must(t, html, `localStorage.getItem('kuztds-accent')`, "remembered accent")
	must(t, html, `data-accent-pick`, "accent swatch")
	must(t, html, `function applyAccent(`, "applyAccent")
	must(t, html, `rgb(var(--acc-l))`, "light accent from the RGB triple")
	must(t, html, `tbody tr:hover td{background:var(--accent-soft)}`, "row hover uses the accent")
	mustNot(t, html, `tbody tr:hover td{background:var(--surface-2)}`, "surface row hover")
}

// TestNavMoreMenu checks the overflow menu for hidden nav items.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestNavMoreMenu(t *testing.T) {
	html := string(indexHTML)
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
	html := string(indexHTML)
	must(t, html, `'Europe/Kiev':'Europe/Kyiv'`, "Kiev alias")
	must(t, html, `function tzCanon(`, "tzCanon")
}

// TestFilePickAndCrop checks logo crop, file drop and the header logo box.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestFilePickAndCrop(t *testing.T) {
	html := string(indexHTML)
	must(t, html, `function cropImage(`, "cropImage")
	must(t, html, `function bindFilePick(`, "bindFilePick")
	must(t, html, `upload:'<path`, "upload icon")
	must(t, html, `.logo{height:26px;width:auto`, "logo keeps its ratio")
	mustNot(t, html, `.logo{width:26px;height:26px`, "square logo rule")
}

// TestButtonSizes checks icon and tag button sizes.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestButtonSizes(t *testing.T) {
	html := string(indexHTML)
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
	html := string(indexHTML)
	must(t, html, `if(kind==='domains') return [['id','flows.col.num',1],['host','dom.host',1]`, "domains column set")
	must(t, html, `const DOM_LOCKED=new Set(['id','host'])`, "locked domain columns")
	must(t, html, `domains:'kuztds-dom-cols'`, "domains column storage key")
	must(t, html, `data-col-gear="domains"`, "domains column gear")
	must(t, html, `const DOM_PAGE_KEY='kuztds-domains-page'`, "domains page storage key")
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
	must(t, src, `c.id==='host'?'<th class="acts"></th>':''`, "actions header after host")
	must(t, src, `data-more="${esc(d.id)}"`, "domain row more menu")
	mustNot(t, html, `data-dcheck=`, "inline domain check")
	mustNot(t, html, `data-ddel=`, "inline domain delete")
}

// TestApiKeysMenu checks rotate, edit, remove and the key table.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestApiKeysMenu(t *testing.T) {
	html := string(indexHTML)
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

// TestProfileTabsSections checks profile tabs, settings sections and the TDS title.
// t is the test handle. It returns nothing. A missing marker fails the test.
func TestProfileTabsSections(t *testing.T) {
	html := string(indexHTML)
	must(t, html, `class="vseg" role="tablist"`, "profile tab list")
	must(t, html, `role="tab" data-ptab="profile"`, "profile tab")
	must(t, html, `<section class="setsec">`, "settings section")
	must(t, html, `'set.sec.access':'Компания и доступ'`, "ru access section")
	must(t, html, `.setsec h2{`, "section heading style")
	must(t, html, `<title>TDS</title>`, "document title")
	must(t, html, `document.title=name||'TDS'`, "title falls back to TDS")
	must(t, html, `a.hidden=!name&&!logo`, "brand hidden without name and logo")
	mustNot(t, html, `CustomTDS`, "CustomTDS branding")
}

// TestStreamProxyAndOrConditions checks the stream editor offers proxy and IP
// version, an any-of switch, and collects every tri flag from COND.
func TestStreamProxyAndOrConditions(t *testing.T) {
	html := string(indexHTML)
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
	re := regexp.MustCompile(`'((?:[^'\\]|\\.)*)'\s*:`)
	seen := map[string]int{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
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
	html := string(indexHTML)
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
	dictDupes(t, dictBetween(t, extra, "\nuk:{", "\n};"), "PHRASE_EXTRA.uk")
	sameDictKeys(t, dictKeys(dictBetween(t, i18n, "en:{", "\n},\nru:{")), dictKeys(dictBetween(t, i18n, "\nru:{", "\n},\nuk:{")), "I18N.en", "I18N.ru")
	sameDictKeys(t, dictKeys(dictBetween(t, i18n, "en:{", "\n},\nru:{")), dictKeys(dictBetween(t, i18n, "\nuk:{", "\n};")), "I18N.en", "I18N.uk")
	sameDictKeys(t, dictKeys(dictBetween(t, phrase, "\nru:{", "\n},\nuk:{")), dictKeys(dictBetween(t, phrase, "\nuk:{", "\n};")), "PHRASE.ru", "PHRASE.uk")
}

// dictKeys collects quoted keys from one dictionary object.
// body is the object source without the surrounding name. It returns the key set.
func dictKeys(body string) map[string]struct{} {
	re := regexp.MustCompile(`'((?:[^'\\]|\\.)*)'\s*:`)
	out := map[string]struct{}{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
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
	html := string(indexHTML)
	must(t, html, `tr('col.revenue')`, "campaign footer labels revenue")
	must(t, html, `campaignCostOnly`, "campaign footer reads click and manual cost")
	mustNot(t, html, `${periodMoney(p.profit)}</b>${tr('profit')}`, "footer no longer calls revenue profit")
}

func TestSPALiterals(t *testing.T) {
	html := string(indexHTML)
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
