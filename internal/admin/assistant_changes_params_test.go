package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hasayone/empataso-tds/internal/config"
)

// chgAnswer writes err the way a handler of the assistant answers it and returns the status and
// the decoded body.
func (e *chgEnv) chgAnswer(t *testing.T, err error) (int, map[string]any) {
	t.Helper()
	if err == nil {
		t.Fatal("no error to answer")
	}
	rec := httptest.NewRecorder()
	e.s.failWrite(rec, err)
	var body map[string]any
	if jerr := json.Unmarshal(rec.Body.Bytes(), &body); jerr != nil {
		t.Fatalf("answer %q: %v", rec.Body.String(), jerr)
	}
	return rec.Code, body
}

// wantAnswer fails unless the answer for err has the status, the code and exactly the params in
// want (nil: the answer has no "params" field). The params are compared as JSON.
func (e *chgEnv) wantAnswer(t *testing.T, label string, err error, status int, code string, want map[string]any) {
	t.Helper()
	got, body := e.chgAnswer(t, err)
	if got != status || body["code"] != code {
		t.Fatalf("%s: status %d code %v (%v), want %d %s", label, got, body["code"], body["error"], status, code)
	}
	params, has := body["params"]
	if want == nil {
		if has {
			t.Fatalf("%s: unexpected params %v", label, params)
		}
		return
	}
	have, _ := json.Marshal(params)
	exp, _ := json.Marshal(want)
	if string(have) != string(exp) {
		t.Fatalf("%s: params %s, want %s", label, have, exp)
	}
	if text, _ := body["error"].(string); text == "" {
		t.Fatalf("%s: no fallback text", label)
	}
}

// TestAIErrorParams checks the params of an aiError reach the answer, and that an error without
// params answers exactly as before.
func TestAIErrorParams(t *testing.T) {
	e := newChgEnv(t)
	e.wantAnswer(t, "with params", newAIErrorP(http.StatusBadRequest, aiErrBadLimit, "bad", map[string]any{"field": "max_steps", "min": 1, "max": 20}),
		http.StatusBadRequest, aiErrBadLimit, map[string]any{"field": "max_steps", "min": 1, "max": 20})
	e.wantAnswer(t, "none", newAIError(http.StatusConflict, aiErrHasLinks, "x"), http.StatusConflict, aiErrHasLinks, nil)
	e.wantAnswer(t, "empty", newAIErrorP(http.StatusConflict, aiErrHasLinks, "x", map[string]any{}), http.StatusConflict, aiErrHasLinks, nil)
	e.wantAnswer(t, "stale", errAIStale, http.StatusConflict, aiErrStale, nil)
	e.wantAnswer(t, "keitaro", errKeitaroRunning, http.StatusConflict, aiErrKeitaroRunning, nil)
	e.wantAnswer(t, "bare conflict", errAIConflict, http.StatusConflict, aiErrConflict, map[string]any{"reason": aiReasonChanged})
	var links *aiLinksError
	err := error(aiChgHasLinks(aiChangeRec{}, []aiLinkView{{Resource: "campaigns", ID: "g1", Name: "Demo"}}))
	if !errors.As(err, &links) {
		t.Fatal("not a links error")
	}
	e.wantAnswer(t, "links", err, http.StatusConflict, aiErrHasLinks,
		map[string]any{"links": []map[string]any{{"resource": "campaigns", "id": "g1", "name": "Demo"}}})
	if links.View.ErrorCode != aiErrHasLinks || links.View.ErrorParams == nil {
		t.Fatalf("view %+v", links.View)
	}
}

// TestWriteErrParams checks writeErr and writeErrParams: the old answer has no params field.
func TestWriteErrParams(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, http.StatusNotFound, "not found")
	if got := strings.TrimSpace(rec.Body.String()); got != `{"code":"not_found","error":"not found"}` {
		t.Fatalf("writeErr: %s", got)
	}
	rec = httptest.NewRecorder()
	writeErrParams(rec, http.StatusNotFound, "not found", nil)
	if got := strings.TrimSpace(rec.Body.String()); got != `{"code":"not_found","error":"not found"}` {
		t.Fatalf("no params: %s", got)
	}
	rec = httptest.NewRecorder()
	writeErrParams(rec, http.StatusBadRequest, "ai_bad_model: x", map[string]any{"model": "m"})
	if got := strings.TrimSpace(rec.Body.String()); got != `{"code":"ai_bad_model","error":"ai_bad_model: x","params":{"model":"m"}}` {
		t.Fatalf("params: %s", got)
	}
}

// TestAIErrorConflictParams checks the reasons derived from the text of a conflict an internal
// handler wrote.
func TestAIErrorConflictParams(t *testing.T) {
	boardCode, boardMsg := newChgEnv(t).s.restoreBoardCheck(context.Background(), "ghost")
	if boardCode != http.StatusConflict {
		t.Fatalf("board check %d %s", boardCode, boardMsg)
	}
	cases := []struct {
		msg, id, fallback string
		want              map[string]any
	}{
		{errAIConflict.msg, "", "x", map[string]any{"reason": "changed"}},
		{conflictMsg("id already exists"), "o9", "x", map[string]any{"reason": "id_taken", "id": "o9"}},
		{conflictMsg("campaign id is taken"), "g9", "x", map[string]any{"reason": "id_taken", "id": "g9"}},
		{conflictMsg("campaign id is taken"), "", "x", map[string]any{"reason": "id_taken"}},
		{conflictMsg("alias is taken: promo"), "g9", "x", map[string]any{"reason": "slug_taken", "slug": "promo"}},
		{boardMsg, "g9", "x", map[string]any{"reason": "board_deleted"}},
		{conflictMsg("unknown offer o5"), "g9", "links_changed", map[string]any{"reason": "links_changed"}},
		{aiErrConflict + ": network n1 is gone", "o9", "links_changed", map[string]any{"reason": "links_changed"}},
		{"already exists", "o9", "x", nil},
		{"ai_stale: the record changed", "o9", "x", nil},
	}
	for _, c := range cases {
		got, _ := json.Marshal(aiConflictParams(c.msg, c.id, c.fallback))
		want, _ := json.Marshal(c.want)
		if string(got) != string(want) {
			t.Errorf("%q: %s, want %s", c.msg, got, want)
		}
	}
	if errAIConflict.Params["reason"] != aiReasonChanged || len(errAIConflict.Params) != 1 {
		t.Fatalf("the shared error was changed: %v", errAIConflict.Params)
	}
	if got := aiChgWithParams(errAIConflict, "x", "y"); got != errAIConflict {
		t.Fatal("an error with params must be returned as it is")
	}
}

// TestAssistantChangesProposeParams checks the params of every refusal of a proposal.
func TestAssistantChangesProposeParams(t *testing.T) {
	e := newChgEnv(t)
	e.groups.list[0].Streams[0].Out = config.Output{Offers: []config.ShareRef{{ID: "o1"}}}
	big := e.s.cfg.LandingFiles.Write("l9", "big.bin", make([]byte, aiLandingMaxBytes+1))
	if big != nil {
		t.Fatal(big)
	}
	for _, c := range []struct {
		label, resource, op, args, code string
		status                          int
		want                            map[string]any
	}{
		{"has links", "offers", aiOpDelete, `{"id":"o1"}`, aiErrHasLinks, http.StatusConflict,
			map[string]any{"links": []map[string]any{{"resource": "campaigns", "id": "g1", "name": "Demo"}}}},
		{"id taken", "offers", aiOpCreate, `{"id":"o1","patch":{"name":"x","url":"https://a.test/"}}`, aiErrConflict, http.StatusConflict,
			map[string]any{"reason": "id_taken", "id": "o1"}},
		{"slug taken", "campaigns", aiOpCreate, `{"id":"demo","patch":{"name":"x"}}`, aiErrConflict, http.StatusConflict,
			map[string]any{"reason": "slug_taken", "slug": "demo"}},
		{"redacted", "offers", aiOpUpdate, `{"id":"o9","patch":{"url":"https://x.test/p?token=***"}}`, aiErrRedactedValue, http.StatusBadRequest,
			map[string]any{"field": "url"}},
		{"redacted create", "offers", aiOpCreate, `{"patch":{"name":"a","url":"https://x.test/p?key=***"}}`, aiErrRedactedValue, http.StatusBadRequest,
			map[string]any{"field": "url"}},
		{"server field", "offers", aiOpUpdate, `{"id":"o9","patch":{"id":"x"}}`, aiErrBadPatch, http.StatusBadRequest,
			map[string]any{"reason": "server_field", "field": "id"}},
		{"not editable", "offers", aiOpUpdate, `{"id":"o9","patch":{"nonsense":1}}`, aiErrBadPatch, http.StatusBadRequest,
			map[string]any{"reason": "not_editable", "field": "nonsense", "resource": "offers"}},
		{"hidden field", "campaigns", aiOpUpdate, `{"id":"g9","patch":{"postbacks":[]}}`, aiErrBadPatch, http.StatusBadRequest,
			map[string]any{"reason": "hidden_field", "field": "postbacks"}},
		{"hidden stream key", "campaigns", aiOpUpdate, `{"id":"g9","patch":{"streams":[{"name":"main","curl":"a|b"}]}}`, aiErrBadPatch, http.StatusBadRequest,
			map[string]any{"reason": "hidden_field", "field": "curl"}},
		{"fetch redirect", "campaigns", aiOpUpdate, `{"id":"g9","patch":{"redirect":"curl"}}`, aiErrBadPatch, http.StatusBadRequest,
			map[string]any{"reason": "fetch_redirect", "field": "redirect"}},
		{"no change", "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Spare"}}`, aiErrBadPatch, http.StatusBadRequest,
			map[string]any{"reason": "no_change"}},
		{"stream dropped", "campaigns", aiOpUpdate, `{"id":"g1","patch":{"streams":[{"name":"other","status":true}]}}`, aiErrBadPatch, http.StatusBadRequest,
			map[string]any{"reason": "stream_dropped", "stream": "main", "fields": []string{"b_curl", "curl"}}},
		{"landing size", "landings", aiOpDelete, `{"id":"l9"}`, aiErrLandingTooLarge, http.StatusConflict,
			map[string]any{"reason": "size", "limit": aiLandingMaxBytes}},
	} {
		_, err := e.propose(t, c.resource, c.op, c.args)
		e.wantAnswer(t, c.label, err, c.status, c.code, c.want)
	}

	files := newChgEnv(t)
	for i := 0; i <= aiLandingMaxFiles; i++ {
		if err := files.s.cfg.LandingFiles.Write("l9", "f/"+strings.Repeat("a", i%7)+string(rune('a'+i%26))+"/"+strconv.Itoa(i)+".txt", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	_, err := files.propose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	files.wantAnswer(t, "landing files", err, http.StatusConflict, aiErrLandingTooLarge, map[string]any{"reason": "files", "limit": aiLandingMaxFiles})

	link := newChgEnv(t)
	outside := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(link.dir, "landing-files", "l9", "link.html")); err != nil {
		t.Skip("symbolic links are not available")
	}
	_, err = link.propose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	link.wantAnswer(t, "landing symlink", err, http.StatusConflict, aiErrLandingTooLarge, map[string]any{"reason": "symlink"})
}

// TestAssistantApplyParams checks the params of every refusal of an apply and the code and params
// of the card of an entry that was aborted.
func TestAssistantApplyParams(t *testing.T) {
	ctx := context.Background()

	e := newChgEnv(t)
	del := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	_, err := e.apply(del.ID, aiApplyOpts{})
	e.wantAnswer(t, "confirm delete", err, http.StatusBadRequest, aiErrConfirmRequired, map[string]any{"need": "delete"})
	host := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"url":"https://new-host.test/x"}}`)
	_, err = e.apply(host.ID, aiApplyOpts{})
	e.wantAnswer(t, "confirm host", err, http.StatusBadRequest, aiErrConfirmRequired, map[string]any{"need": "host"})
	e.mustApply(t, host.ID)
	_, err = e.apply(host.ID, aiApplyOpts{ConfirmHost: true})
	e.wantAnswer(t, "state", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "state", "status": "done"})
	_, err = e.s.rejectChange(ctx, e.root, host.ID)
	e.wantAnswer(t, "reject state", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "state", "status": "done"})

	stale := newChgEnv(t)
	upd := stale.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Mine"}}`)
	offers, _ := stale.s.cfg.Offers.List(ctx)
	offers[1].Name = "Edited by hand"
	_ = stale.s.cfg.Offers.Save(ctx, offers)
	_, err = stale.apply(upd.ID, aiApplyOpts{})
	stale.wantAnswer(t, "stale", err, http.StatusConflict, aiErrStale, nil)

	gone := newChgEnv(t)
	upd = gone.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Mine"}}`)
	offers, _ = gone.s.cfg.Offers.List(ctx)
	_ = gone.s.cfg.Offers.Save(ctx, offers[:1])
	_, err = gone.apply(upd.ID, aiApplyOpts{})
	gone.wantAnswer(t, "record gone", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "changed"})

	race := newChgEnv(t)
	create := race.mustPropose(t, "offers", aiOpCreate, `{"id":"o50","patch":{"name":"x","url":"https://a.test/"}}`)
	offers, _ = race.s.cfg.Offers.List(ctx)
	_ = race.s.cfg.Offers.Save(ctx, append(offers, config.Offer{ID: "o50", Name: "Appeared", URL: "https://b.test/", Status: true}))
	_, err = race.apply(create.ID, aiApplyOpts{ConfirmHost: true})
	race.wantAnswer(t, "id taken", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "id_taken", "id": "o50"})

	linked := newChgEnv(t)
	d := linked.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	linked.groups.list[0].Streams[0].Out = config.Output{Offers: []config.ShareRef{{ID: "o9"}}}
	_, err = linked.apply(d.ID, aiApplyOpts{ConfirmDelete: true})
	linked.wantAnswer(t, "has links", err, http.StatusConflict, aiErrHasLinks,
		map[string]any{"links": []map[string]any{{"resource": "campaigns", "id": "g1", "name": "Demo"}}})
	var le *aiLinksError
	if !errors.As(err, &le) || le.View.ErrorCode != aiErrHasLinks || le.View.ErrorParams["links"] == nil || le.View.Error == "" {
		t.Fatalf("view %+v", le)
	}

	camp := newChgEnv(t)
	cd := camp.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
	camp.groups.list = append(camp.groups.list, config.Group{ID: "g5", Name: "Hands over", Status: true, Redirect: "group", Out: "g9", Streams: []config.Stream{}})
	_, err = camp.apply(cd.ID, aiApplyOpts{ConfirmDelete: true})
	camp.wantAnswer(t, "campaign has links", err, http.StatusConflict, aiErrHasLinks,
		map[string]any{"links": []map[string]any{{"resource": "campaigns", "id": "g5", "name": "Hands over"}}})

	run := newChgEnv(t)
	k := run.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	if msg := run.s.ktJob.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	_, err = run.apply(k.ID, aiApplyOpts{})
	run.wantAnswer(t, "keitaro", err, http.StatusConflict, aiErrKeitaroRunning, nil)
}

// TestAssistantApplyAbortedCardHasCode checks the card of an entry aborted by the write keeps the
// code and the params of the refusal, in the view and in changes.json.
func TestAssistantApplyAbortedCardHasCode(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	if err := e.s.cfg.LandingFiles.Write("l9", "big.bin", make([]byte, aiLandingMaxBytes+1)); err != nil {
		t.Fatal(err)
	}
	_, err := e.apply(v.ID, aiApplyOpts{ConfirmDelete: true})
	e.wantAnswer(t, "snapshot", err, http.StatusConflict, aiErrLandingTooLarge, map[string]any{"reason": "size", "limit": aiLandingMaxBytes})
	recs, _ := e.s.aiChgRead()
	card, _ := json.Marshal(recs[0].view())
	var got map[string]any
	if err := json.Unmarshal(card, &got); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(got["error_params"])
	if got["status"] != aiChangeAborted || got["error_code"] != aiErrLandingTooLarge || got["error"] == "" ||
		string(params) != `{"limit":10485760,"reason":"size"}` {
		t.Fatalf("card %s", card)
	}
	if !strings.Contains(e.chgFile(t), `"error_code": "ai_landing_too_large"`) && !strings.Contains(e.chgFile(t), `"error_code":"ai_landing_too_large"`) {
		t.Fatal("the code is not stored")
	}
	ok := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Fine"}}`)
	if b, _ := json.Marshal(ok); strings.Contains(string(b), "error_code") || strings.Contains(string(b), "error_params") {
		t.Fatalf("a card without an error has an error code: %s", b)
	}
}

// TestAssistantRevertParams checks the params of every refusal of a revert, in the answer and in
// the report of a message revert.
func TestAssistantRevertParams(t *testing.T) {
	ctx := context.Background()

	changed := newChgEnv(t)
	v := changed.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Mine"}}`)
	changed.mustApply(t, v.ID)
	offers, _ := changed.s.cfg.Offers.List(ctx)
	offers[1].Name = "By hand"
	_ = changed.s.cfg.Offers.Save(ctx, offers)
	_, err := changed.s.revertChange(ctx, changed.root, v.ID)
	changed.wantAnswer(t, "changed by hand", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "changed"})
	report, rerr := changed.s.revertMessage(ctx, changed.root, "c1", "m1")
	if rerr != nil || report.StoppedAt != v.ID || report.ErrorCode != aiErrConflict || report.ErrorParams["reason"] != "changed" {
		t.Fatalf("report %+v %v", report, rerr)
	}
	_, err = changed.s.revertChange(ctx, changed.root, "missing")
	if err == nil {
		t.Fatal("a missing change was reverted")
	}

	state := newChgEnv(t)
	s1 := state.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Mine"}}`)
	_, err = state.s.revertChange(ctx, state.root, s1.ID)
	state.wantAnswer(t, "state", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "state", "status": "proposed"})

	links := newChgEnv(t)
	cr := links.mustPropose(t, "offers", aiOpCreate, `{"id":"o60","patch":{"name":"New","url":"https://a.test/"}}`)
	links.mustApply(t, cr.ID)
	links.groups.list[0].Streams[0].Out = config.Output{Offers: []config.ShareRef{{ID: "o60"}}}
	_, err = links.s.revertChange(ctx, links.root, cr.ID)
	links.wantAnswer(t, "links", err, http.StatusConflict, aiErrConflict,
		map[string]any{"reason": "links_changed", "links": []map[string]any{{"resource": "campaigns", "id": "g1", "name": "Demo"}}})

	camp := newChgEnv(t)
	cc := camp.mustPropose(t, "campaigns", aiOpCreate, `{"id":"flowx","patch":{"name":"New"}}`)
	camp.mustApply(t, cc.ID)
	camp.groups.list = append(camp.groups.list, config.Group{ID: "g5", Name: "Hands over", Status: true, Redirect: "group", Out: "flowx", Streams: []config.Stream{}})
	_, err = camp.s.revertChange(ctx, camp.root, cc.ID)
	camp.wantAnswer(t, "campaign links", err, http.StatusConflict, aiErrConflict,
		map[string]any{"reason": "links_changed", "links": []map[string]any{{"resource": "campaigns", "id": "g5", "name": "Hands over"}}})

	taken := newChgEnv(t)
	td := taken.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	taken.mustApply(t, td.ID)
	offers, _ = taken.s.cfg.Offers.List(ctx)
	_ = taken.s.cfg.Offers.Save(ctx, append(offers, config.Offer{ID: "o9", Name: "Squatter", URL: "https://s.test/", Status: true}))
	_, err = taken.s.revertChange(ctx, taken.root, td.ID)
	taken.wantAnswer(t, "id taken", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "id_taken", "id": "o9"})

	gid := newChgEnv(t)
	gd := gid.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
	gid.mustApply(t, gd.ID)
	gid.groups.list = append(gid.groups.list, config.Group{ID: "g9", Name: "Squatter", Status: true, Redirect: "stop", Streams: []config.Stream{}})
	_, err = gid.s.revertChange(ctx, gid.root, gd.ID)
	gid.wantAnswer(t, "campaign id", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "id_taken", "id": "g9"})

	slug := newChgEnv(t)
	sd := slug.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
	slug.mustApply(t, sd.ID)
	slug.groups.list = append(slug.groups.list, config.Group{ID: "g5", Name: "Other", Status: true, Redirect: "stop", Aliases: []string{"promo"}, Streams: []config.Stream{}})
	_, err = slug.s.revertChange(ctx, slug.root, sd.ID)
	slug.wantAnswer(t, "alias", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "slug_taken", "slug": "promo"})

	gone := newChgEnv(t)
	offers, _ = gone.s.cfg.Offers.List(ctx)
	offers[1].NetworkID = "n9"
	_ = gone.s.cfg.Offers.Save(ctx, offers)
	od := gone.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	gone.mustApply(t, od.ID)
	_ = gone.s.cfg.Networks.Save(ctx, []config.AffiliateNetwork{{ID: "n1", Name: "Net", Status: true, PostbackURL: "https://t.test/pb?cid={subid}"}})
	_, err = gone.s.revertChange(ctx, gone.root, od.ID)
	gone.wantAnswer(t, "link gone", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "links_changed"})

	files := newChgEnv(t)
	ld := files.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	files.mustApply(t, ld.ID)
	_ = files.s.cfg.LandingFiles.Write("l9", "other.html", []byte("mine"))
	_, err = files.s.revertChange(ctx, files.root, ld.ID)
	files.wantAnswer(t, "landing files", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "landing_dir_exists"})

	run := newChgEnv(t)
	w := run.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Other"}}`)
	run.mustApply(t, w.ID)
	if msg := run.s.ktJob.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	_, err = run.s.revertChange(ctx, run.root, w.ID)
	run.wantAnswer(t, "keitaro", err, http.StatusConflict, aiErrKeitaroRunning, nil)
}

// TestAssistantRevertSnapshotParams checks a missing snapshot and a damaged one are told apart
// from files that block the restore.
func TestAssistantRevertSnapshotParams(t *testing.T) {
	ctx := context.Background()
	e := newChgEnv(t)
	v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	e.mustApply(t, v.ID)
	recs, _ := e.s.aiChgRead()
	blob := e.s.aiBlobPath(recs[0].Change.Blob)
	if err := os.WriteFile(blob, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.s.revertChange(ctx, e.root, v.ID)
	e.wantAnswer(t, "damaged", err, http.StatusConflict, aiErrConflict, map[string]any{"reason": "snapshot_missing"})
}
