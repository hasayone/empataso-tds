package admin

import (
	"strings"
	"testing"
)

// TestKeitaroJournalPanel checks that the "What to move" tab hosts the import journal, paints it on
// every job read and binds Continue to a resume request, and that every journal label has three translations.
func TestKeitaroJournalPanel(t *testing.T) {
	html := spaSource()
	page := spaFunctionLine(html, "keitaroPanel")
	if page == "" {
		t.Fatal("keitaro panel missing")
	}
	must(t, page, `<div id="kt-journal" hidden></div>`, "the import tab hosts the journal")
	must(t, page, "startImport(btn, {resume:true})", "Continue sends only resume")
	must(t, page, "'kt-resume'", "Continue is locked with the other import buttons")
	if n := strings.Count(page, "paintJournal(job)"); n != 2 {
		t.Errorf("paintJournal(job) appears %d times, want 2 (running and finished)", n)
	}
	must(t, page, "if(job0.resumable) ktSelect('kt-import')", "a resumable run opens the import tab")
	must(t, page, "if(job&&job.resumable) ktSelect('kt-import')", "a run that stops while the page is open also opens the import tab")
	journal := spaFunctionLine(html, "ktJournalHTML")
	must(t, journal, "if(!j||!j.started_at) return ''", "no journal before the first import")
	must(t, journal, "!running&&j.resumable?", "Continue only for a stopped resumable run")
	must(t, spaFunctionLine(html, "ktJobError"), "j.interrupted?tr('kt.journal.interrupted')", "a restart cut reads as such")
	for _, k := range []string{"kt.journal", "kt.journal.started", "kt.journal.updated", "kt.journal.ended", "kt.journal.interrupted", "kt.resume", "kt.resume.hint"} {
		if n := strings.Count(html, "'"+k+"':'"); n != 3 {
			t.Errorf("%s has %d translations, want 3 (en, ru, uk)", k, n)
		}
	}
}
