package admin

import (
	"strings"
	"testing"
)

// TestReportDateColumn checks the report catalog lists the date column first and on by default,
// so "Restore defaults" brings it back; a saved list without it gets it in front while one that
// turned it off keeps it off; the request asks for rows cut by day only when the column is on,
// the cell and the sort read the day, and the label exists in every language.
// t is the test handle. A missing marker fails the test.
func TestReportDateColumn(t *testing.T) {
	html := spaSource()
	rep := catalogBranch(html, "report")
	must(t, rep, "['day','col.day',1],['group','conv.campaign',1]", "date is the first report column and on by default")

	cols := spaFunctionLine(html, "loadCols")
	must(t, cols, "dayIsNew=kind==='report'&&by.has('day')", "only a saved list that never had the column gets it added")
	must(t, cols, "Object.assign({},d,{on:!!s.on})", "a saved list keeps its own on/off")
	must(t, spaFunctionLine(html, "openCols"), "colCatalog(kind).map(colSpec)", "restore reads the catalog defaults")

	qs := spaFunctionLine(html, "reportQS")
	must(t, qs, "qs.set('day','1')", "the report asks for rows cut by day")
	must(t, qs, "qs.set('tz',uiTz())", "days are cut in the interface time zone")
	must(t, spaFunctionLine(html, "reportDayOn"), "c.id==='day'&&c.on", "days are asked only while the column is on")
	must(t, spaFunctionLine(html, "reportCellText"), "if(id==='day') return r.day||''", "the cell shows the row day")
	must(t, spaFunctionLine(html, "paintReport"), "date:['day']", "the date column sorts newest first")

	if n := strings.Count(html, "'col.day':'"); n != 3 {
		t.Fatalf("the date label must be in en, ru and uk: %d definitions", n)
	}
}
