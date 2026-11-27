package admin

import (
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestServeAssetsDatepicker — вендорный air-datepicker отдаётся из /assets/air-datepicker/ без сессии,
// с верным content-type и кешем; лицензия лежит рядом со скриптом.
func TestServeAssetsDatepicker(t *testing.T) {
	srv, _, _ := fullServer(t, nil)

	cases := []struct{ path, ctype string }{
		{"/assets/air-datepicker/air-datepicker.js", "javascript"},
		{"/assets/air-datepicker/air-datepicker.css", "css"},
		{"/assets/air-datepicker/LICENSE", ""},
		{"/assets/air-datepicker/VERSION", ""},
	}
	for _, c := range cases {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s → 200, got %d", c.path, resp.StatusCode)
		}
		if c.ctype != "" && !strings.Contains(resp.Header.Get("Content-Type"), c.ctype) {
			t.Errorf("%s: Content-Type = %q, expected %s", c.path, resp.Header.Get("Content-Type"), c.ctype)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=86400" {
			t.Errorf("%s: Cache-Control = %q, expected private, max-age=86400", c.path, cc)
		}
		if len(body) == 0 {
			t.Errorf("%s: empty body", c.path)
		}
		if strings.HasSuffix(c.path, "LICENSE") && !strings.Contains(string(body), "MIT License") {
			t.Errorf("%s: MIT license text expected", c.path)
		}
	}
}

// TestIndexHTMLWiresDatepicker — SPA подключает вендорный пикер, держит нативный fallback,
// не читает даты иначе чем из input.value и не тянет библиотеку с CDN.
func TestIndexHTMLWiresDatepicker(t *testing.T) {
	html := spaSource()
	for _, want := range []string{
		`/assets/air-datepicker/air-datepicker.css`,
		`/assets/air-datepicker/air-datepicker.js`,
		`id="range-cal"`,
		`dpBindRange(id)`,
		`DP_SEL='input[type=date],input[type=datetime-local]'`,
		`dateFormat:timepicker?dpIso:'yyyy-MM-dd'`,
		`function dpIso(d)`,
		`dpHit(e,box)`,
		`.adp-app`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html must contain %q", want)
		}
	}
	for _, bad := range []string{"cdn.jsdelivr", "unpkg.com", "cdnjs.cloudflare"} {
		for _, tag := range []string{`src="https://` + bad, `href="https://` + bad} {
			if strings.Contains(html, tag) {
				t.Errorf("index.html must not load the picker from %s", bad)
			}
		}
	}
}

// hexLuminance returns the relative luminance of a #rrggbb colour (WCAG 2).
// hex is the colour with the leading #. It returns 0 for a value that is not a colour.
func hexLuminance(hex string) float64 {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || len(hex) != 7 {
		return 0
	}
	lin := func(c uint64) float64 {
		s := float64(c) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(v>>16&0xff) + 0.7152*lin(v>>8&0xff) + 0.0722*lin(v&0xff)
}

// contrastRatio is the WCAG contrast of two #rrggbb colours, from 1 to 21.
func contrastRatio(a, b string) float64 {
	la, lb := hexLuminance(a), hexLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestDatepickerStyles checks the picker skin: one rule sets the picker variables with no variable
// twice, each slider thumb is styled once, the Start/End row of the range popup keeps its grid and
// both inputs, and the picker buttons stay readable in both dark token blocks.
func TestDatepickerStyles(t *testing.T) {
	html := spaSource()

	if n := strings.Count(html, ".air-datepicker.adp-app{"); n != 1 {
		t.Fatalf(".air-datepicker.adp-app has %d rules, want 1", n)
	}
	vars := map[string]int{}
	for _, m := range regexp.MustCompile(`(--adp-[a-z-]+):`).FindAllStringSubmatch(spaBetween(html, ".air-datepicker.adp-app{", "}\n"), -1) {
		vars[m[1]]++
	}
	for name, n := range vars {
		if n != 1 {
			t.Errorf("%s is set %d times in the picker rule", name, n)
		}
	}
	for _, name := range []string{"--adp-background-color-hover", "--adp-btn-color", "--adp-btn-background-color-hover", "--adp-btn-background-color-active", "--adp-time-track-height", "--adp-cell-background-color-hover", "--adp-width"} {
		if vars[name] != 1 {
			t.Errorf("%s must be set once in the picker rule", name)
		}
	}
	for _, thumb := range []string{
		".adp-app .air-datepicker-time--row input[type=range]::-webkit-slider-thumb{background:var(--surface-3);border-color:var(--muted)}",
		".adp-app .air-datepicker-time--row input[type=range]::-moz-range-thumb{background:var(--surface-3);border-color:var(--muted)}",
	} {
		if n := strings.Count(html, thumb); n != 1 {
			t.Errorf("slider thumb rule appears %d times, want 1: %s", n, thumb)
		}
	}
	for _, sel := range []string{"input[type=range]::-webkit-slider-thumb{", "input[type=range]::-moz-range-thumb{"} {
		if n := strings.Count(html, sel); n != 1 {
			t.Errorf("%s is styled in %d rules, want 1", sel, n)
		}
	}

	must(t, html, ".rangepop .rangerow{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:8px}", "Start/End row is a wrapping grid")
	must(t, html, ".rangepop input{width:100%;min-width:0}", "Start/End inputs fill their cell")
	row := spaBetween(html, `<div class="rangerow">`, `<div class="row">`)
	for _, want := range []string{`tr('spend.from')`, `id="range-a"`, `tr('spend.to')`, `id="range-b"`} {
		must(t, row, want, "Start/End row of the range popup")
	}
	if strings.Count(row, `<div class="fld">`) != 2 {
		t.Error("the range row must hold exactly the Start and End fields")
	}

	button := spaBetween(html, ".adp-app .air-datepicker-button{", "}")
	for _, want := range []string{"color:var(--fg)", "background:var(--surface-2)", "border:1px solid var(--border-strong)"} {
		must(t, button, want, "picker button reads theme tokens")
	}
	mustNot(t, button, "#", "picker button has no fixed colour")
	must(t, html, ".adp-app .air-datepicker-button:hover,.adp-app .air-datepicker-button:focus{color:var(--fg);background:var(--surface-3)}", "button hover keeps the text colour")

	hexOf := regexp.MustCompile(`--([a-z0-9-]+):(#[0-9a-fA-F]{6})`)
	for name, block := range map[string]string{
		"dark theme":        spaBetween(html, `:root[data-theme="dark"]{`, "}"),
		"system dark theme": spaBetween(html, `:root:not([data-theme="light"]){`, "}"),
	} {
		tok := map[string]string{}
		for _, m := range hexOf.FindAllStringSubmatch(block, -1) {
			tok[m[1]] = m[2]
		}
		for _, k := range []string{"fg", "surface-2", "surface-3", "border-strong"} {
			if tok[k] == "" {
				t.Fatalf("%s: token --%s missing", name, k)
			}
		}
		for _, bg := range []string{"surface-2", "surface-3"} {
			if r := contrastRatio(tok["fg"], tok[bg]); r < 4.5 {
				t.Errorf("%s: picker button text on --%s has contrast %.2f, want at least 4.5", name, bg, r)
			}
		}
	}
}

// TestPeriodRangeAndSpendGrid checks the header period and the cost dialog.
// Today is not a calendar button: the library only moves the visible month and does not select a day.
// Clear stays because it drops the selected dates. A custom time range uses hour and minute fields, not the slider clock.
// The cost dialog keeps start/end, amount/currency and the saved rows in a column so Delete cannot cover Cancel and Save.
func TestPeriodRangeAndSpendGrid(t *testing.T) {
	html := spaSource()
	must(t, html, "buttons:['clear']", "Clear stays: it drops the selected dates")
	mustNot(t, html, "buttons:['today','clear']", "Today only moves the visible month and is not a period")
	mustNot(t, html, "if(times){dpBind(a); dpBind(b); return}", "a time range does not open the slider clock")
	must(t, spaFunctionLine(html, "rangeSeed"), "return periodSpan(id, now||new Date(), tz);", "today and the other presets become that span, from 00:00")
	must(t, spaFunctionLine(html, "rangeSeed"), "prev==='dates'||prev==='times'", "a saved custom range is kept")
	must(t, html, `id="period-tabs"`, "header period tabs")
	must(t, html, `data-period="${id}"`, "tabs use the existing period ids")
	for _, id := range []string{"range-ah", "range-am", "range-bh", "range-bm"} {
		must(t, html, `id="`+id+`"`, "clock field "+id)
	}
	must(t, html, `.spend-when{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:0 14px;min-width:0}`, "start and end share one row")
	must(t, html, `@media(max-width:560px){.spend-when{grid-template-columns:minmax(0,1fr)}}`, "the row wraps when the dialog is narrow")
	must(t, html, `.modal.spend-dlg{display:flex;flex-direction:column;`, "the dialog is a column")
	must(t, html, `.spend-foot{flex:none;display:flex;`, "Cancel and Save are the dialog footer")
	dlg := spaFunctionLine(html, "spendDialog")
	rows := spaFunctionLine(html, "spendRowsHTML")
	for _, want := range []string{
		`class="spend-when"`, `class="spend-amt"`, `class="spend-foot"`,
		`id="sp-zone">${esc(tzLabel(cur))}`, "data-sp-period=",
	} {
		must(t, dlg, want, "cost dialog grid")
	}
	must(t, rows, `class="spend-saved"`, "saved costs have their own block")
	must(t, rows, `class="spend-row"`, "a saved cost is its own row")
	mustNot(t, dlg, `type="datetime-local"`, "the cost dialog does not open the slider calendar")
	must(t, dlg, "spendPointOpen(", "clicking a cost date opens the shared calendar")
	must(t, dlg, `id="sp-from" type="text" readonly`, "the start field shows a readable date and time")
	mustNot(t, dlg, "sp-from-h", "the hour is not a box beside the date")
	point := spaFunctionLine(html, "spendPointOpen")
	must(t, point, "dpInline(", "the cost point uses the one inline calendar")
	must(t, point, `class="sp-h"`, "the hour is a field")
	must(t, point, `class="sp-m"`, "the minute is a field")
	must(t, spaFunctionLine(html, "dpInline"), "new AirDatepicker(cal,", "one inline calendar")
	must(t, spaFunctionLine(html, "dpInline"), "dpOptions(false)", "the cost calendar has no time slider")
	must(t, spaFunctionLine(html, "spendFace"), "getMinutes()", "the field shows the clock")
	if saved, foot := strings.Index(dlg, "spendRowsHTML"), strings.Index(dlg, `class="spend-foot"`); saved < 0 || foot < 0 || saved > foot {
		t.Error("saved costs must sit above Cancel and Save")
	}
}
