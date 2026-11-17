package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// keywordRig returns a rig with seven campaigns in every state the fill tells apart. The
// integration stays off, so any request to the fake checker is a failure.
func keywordRig(t *testing.T) *checkerRig {
	t.Helper()
	groups := []config.Group{
		{ID: "mine", Num: 1, Name: "❌ Other - Title - com.o.o"},
		{ID: "linked", Num: 2, Name: "❌ Linked - Title - com.l.l"},
		{ID: "sending", Num: 3, Name: "❌ Sending - Title - com.s.s"},
		{ID: "queued", Num: 4, Name: "❌ Queued - Title - com.q.q"},
		{ID: "errored", Num: 5, Name: "❌ Errored - Title - com.e.e"},
		{ID: "fresh", Num: 6, Name: "❌ Fresh - Title - com.f.f"},
		{ID: "nokey", Num: 7, Name: "❄️ Win"},
	}
	r := newCheckerRig(t, rigSpec{groups: groups})
	r.seedRecord("mine", checkerApp{State: checkerStDraft, Keyword: "Mine"})
	r.seedRecord("linked", checkerApp{State: checkerStLinked, AppID: "app-1"})
	r.seedRecord("sending", checkerApp{State: checkerStSending})
	r.seedRecord("queued", checkerApp{State: checkerStQueued})
	r.seedRecord("errored", checkerApp{State: checkerStError, LastError: "boom"})
	return r
}

func TestCheckerKeywordsPreviewChangesNothing(t *testing.T) {
	r := keywordRig(t)
	saves := r.s.checker.saves.Load()
	code, raw := r.do(http.MethodGet, "/api/checker/keywords/preview", nil)
	var v checkerPreviewView
	if err := json.Unmarshal(raw, &v); err != nil || code != http.StatusOK {
		t.Fatalf("preview: %d %s", code, raw)
	}
	if v.Campaigns != 7 || v.Fill != 3 || v.Keep != 3 || v.NoKey != 1 || len(v.Keys) != 3 || len(v.Sample) != 3 {
		t.Fatalf("preview %+v", v)
	}
	if v.Keys[0].Keyword != "Errored" || v.Keys[0].Campaigns != 1 {
		t.Fatalf("keys are sorted by count, then by name: %+v", v.Keys)
	}
	if r.s.checker.saves.Load() != saves || r.fc.total() != 0 {
		t.Fatal("the preview wrote a file or went to the network")
	}
	if _, has := r.rec("fresh"); has {
		t.Fatal("the preview made a record")
	}
}

func TestCheckerKeywordsFillNeedsConfirmation(t *testing.T) {
	r := keywordRig(t)
	saves := r.s.checker.saves.Load()
	for _, body := range []any{map[string]any{}, map[string]any{"confirm": false}, nil} {
		if code, _ := r.do(http.MethodPost, "/api/checker/keywords/fill", body); code != http.StatusBadRequest {
			t.Errorf("fill without confirmation: %d", code)
		}
	}
	if r.s.checker.saves.Load() != saves {
		t.Fatal("a refused fill wrote the file")
	}
	if _, has := r.rec("fresh"); has {
		t.Fatal("a refused fill made a record")
	}
}

func TestCheckerKeywordsFillKeepsWhatIsSet(t *testing.T) {
	r := keywordRig(t)
	saves := r.s.checker.saves.Load()
	code, raw := r.do(http.MethodPost, "/api/checker/keywords/fill", map[string]any{"confirm": true})
	var got map[string]int
	_ = json.Unmarshal(raw, &got)
	if code != http.StatusOK || got["filled"] != 3 || got["kept"] != 3 || got["no_key"] != 1 {
		t.Fatalf("fill: %d %s", code, raw)
	}
	if n := r.s.checker.saves.Load() - saves; n != 1 {
		t.Fatalf("the fill wrote the file %d times, want once", n)
	}
	if r.fc.total() != 0 {
		t.Fatal("the fill went to the network")
	}
	want := map[string]struct{ state, keyword string }{
		"mine": {checkerStDraft, "Mine"}, "linked": {checkerStLinked, ""}, "sending": {checkerStSending, ""},
		"queued": {checkerStQueued, "Queued"}, "errored": {checkerStError, "Errored"}, "fresh": {checkerStDraft, "Fresh"},
	}
	for id, w := range want {
		rec, has := r.rec(id)
		if !has || rec.State != w.state || rec.Keyword != w.keyword {
			t.Errorf("%s: %+v, want state %s keyword %q", id, rec, w.state, w.keyword)
		}
	}
	if rec, _ := r.rec("fresh"); rec.Name != "6 - Fresh" {
		t.Errorf("the new draft: %+v", rec)
	}
	if rec, _ := r.rec("errored"); rec.LastError != "boom" {
		t.Errorf("the fill touched the error text: %q", rec.LastError)
	}
	if _, has := r.rec("nokey"); has {
		t.Error("a campaign without a key got a record")
	}
	if rec, _ := r.rec("linked"); rec.AppID != "app-1" {
		t.Errorf("a linked record changed: %+v", rec)
	}
	if code, raw = r.do(http.MethodPost, "/api/checker/keywords/fill", map[string]any{"confirm": true}); code != http.StatusOK {
		t.Fatalf("second fill: %d", code)
	}
	if n := r.s.checker.saves.Load() - saves; n != 1 {
		t.Fatalf("a fill with nothing to do wrote the file: %d writes", n)
	}
}
