package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
)

// chgFile returns the text of changes.json.
func (e *chgEnv) chgFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, "assistant", "changes.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// chgHiddenCanaries are the values planted in the hidden fields of the campaign g1.
var chgHiddenCanaries = []string{"CANARYPB", "CANARYPBID", "CANARYCURL", "CANARYBCURL", "CANARYREMOTE"}

// withHiddenFields plants the canaries in the postbacks, the shared ids, the CURL strings and the
// remote fetch of the campaign g1.
func (e *chgEnv) withHiddenFields() {
	g := &e.groups.list[0]
	g.PostbackIDs = []string{"CANARYPBID"}
	g.Streams[0].Remote = config.Remote{Enabled: true, URL: "https://remote.example/CANARYREMOTE?ip=[IP]"}
}

// TestAssistantChangesCampaignHiddenFieldsNeverShown checks no hidden campaign field reaches a
// card, the stored proposal or an error, for a delete, an update and a refused create.
func TestAssistantChangesCampaignHiddenFieldsNeverShown(t *testing.T) {
	e := newChgEnv(t)
	e.withHiddenFields()
	upd := e.mustPropose(t, "campaigns", aiOpUpdate, `{"id":"g1","patch":{"name":"Renamed demo"}}`)
	del := e.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g1"}`)
	var texts []string
	for _, v := range []aiChangeView{upd, del} {
		b, _ := json.Marshal(v)
		texts = append(texts, string(b))
	}
	texts = append(texts, e.chgFile(t))
	for _, args := range []string{
		`{"patch":{"name":"x","postbacks":[{"url":"https://CANARYPB.test/","method":"GET"}]}}`,
		`{"patch":{"name":"x","postback_ids":["CANARYPBID"]}}`,
		`{"patch":{"name":"x","streams":[{"name":"m","status":true,"curl":"CANARYCURL"}]}}`,
		`{"patch":{"name":"x","streams":[{"name":"m","status":true,"b_curl":"CANARYBCURL"}]}}`,
		`{"patch":{"name":"x","streams":[{"name":"m","status":true,"remote":{"enabled":true,"url":"https://CANARYREMOTE.test/"}}]}}`,
		`{"patch":{"name":"x","s2s_url":"https://CANARYPB.test/"}}`,
	} {
		_, err := e.propose(t, "campaigns", aiOpCreate, args)
		wantChgErr(t, args, err, http.StatusBadRequest, aiErrBadPatch)
		texts = append(texts, err.Error())
		_, err = e.propose(t, "campaigns", aiOpUpdate, strings.Replace(args, `{"patch"`, `{"id":"g1","patch"`, 1))
		wantChgErr(t, args, err, http.StatusBadRequest, aiErrBadPatch)
		texts = append(texts, err.Error())
	}
	for _, text := range texts {
		for _, canary := range chgHiddenCanaries {
			if strings.Contains(text, canary) {
				t.Fatalf("%q leaks %q", text[:min(len(text), 80)], canary)
			}
		}
	}
	for _, d := range del.Diff {
		switch d.Field {
		case "postbacks", "postback_ids":
			t.Fatalf("diff of a delete lists the hidden field %s", d.Field)
		}
	}
	if strings.Contains(texts[1], `"curl"`) || strings.Contains(texts[1], `"remote"`) {
		t.Fatalf("delete card shows a hidden stream key: %s", texts[1])
	}
}

// TestAssistantChangesRefusesFetchRedirect checks the redirect mode curl is refused wherever a redirect lives.
func TestAssistantChangesRefusesFetchRedirect(t *testing.T) {
	e := newChgEnv(t)
	for _, patch := range []string{
		`{"redirect":"curl"}`,
		`{"redirect":" CURL "}`,
		`{"streams":[{"name":"main","status":true,"out":{"redirect":"curl","out":"https://a.test/"}}]}`,
		`{"streams":[{"name":"main","status":true,"bots":{"redirect":"curl","out":"https://a.test/"}}]}`,
	} {
		_, err := e.propose(t, "campaigns", aiOpUpdate, `{"id":"g9","patch":`+patch+`}`)
		wantChgErr(t, patch, err, http.StatusBadRequest, aiErrBadPatch)
		if !strings.Contains(err.Error(), "curl") {
			t.Fatalf("the refusal does not say why: %v", err)
		}
		_, err = e.propose(t, "campaigns", aiOpCreate, `{"patch":`+strings.Replace(patch, `{"redirect"`, `{"name":"x","redirect"`, 1)+`}`)
		wantChgErr(t, patch, err, http.StatusBadRequest, aiErrBadPatch)
	}
	e.mustPropose(t, "campaigns", aiOpUpdate, `{"id":"g9","patch":{"redirect":"http","out":"https://a.test/"}}`)
}

// TestAssistantChangesRemoteIsKept checks a streams patch keeps the stored remote fetch and CURL strings of a stream of the same name.
func TestAssistantChangesRemoteIsKept(t *testing.T) {
	e := newChgEnv(t)
	e.withHiddenFields()
	v := e.mustPropose(t, "campaigns", aiOpUpdate, `{"id":"g1","patch":{"streams":[{"name":"main","status":true,"comment":"edited"}]}}`)
	e.mustApply(t, v.ID)
	g, _ := findGroup(e.groups.list, "g1")
	st := g.Streams[0]
	if st.Comment != "edited" || st.Curl != "CANARYCURL" || st.BotCurl != "CANARYBCURL" || st.Remote.URL == "" {
		t.Fatalf("hidden settings lost: %+v", st)
	}
}

// TestAssistantChangesStreamsMustKeepHiddenStreams checks a streams patch that would silently drop
// hidden settings is refused, and a truncated list is a redacted value.
func TestAssistantChangesStreamsMustKeepHiddenStreams(t *testing.T) {
	e := newChgEnv(t)
	e.withHiddenFields()
	for _, patch := range []string{
		`{"streams":[{"name":"renamed","status":true}]}`,
		`{"streams":[]}`,
	} {
		_, err := e.propose(t, "campaigns", aiOpUpdate, `{"id":"g1","patch":`+patch+`}`)
		wantChgErr(t, patch, err, http.StatusBadRequest, aiErrBadPatch)
		if !strings.Contains(err.Error(), "main") {
			t.Fatalf("the refusal does not name the stream: %v", err)
		}
	}
	_, err := e.propose(t, "campaigns", aiOpUpdate, `{"id":"g1","patch":{"streams":[{"name":"main","status":true,"comment":"`+strings.Repeat("a", 70)+`…"}]}}`)
	wantChgErr(t, "truncated", err, http.StatusBadRequest, aiErrRedactedValue)
	_, err = e.propose(t, "campaigns", aiOpUpdate, `{"id":"g1","patch":{"streams":"`+strings.Repeat("a", 70)+`…"}}`)
	wantChgErr(t, "truncated list", err, http.StatusBadRequest, aiErrRedactedValue)
	v := e.mustPropose(t, "campaigns", aiOpUpdate, `{"id":"g9","patch":{"streams":[{"name":"renamed","status":true}]}}`)
	found := false
	for _, d := range v.Diff {
		found = found || d.Field == "streams"
	}
	if !found {
		t.Fatal("a renamed stream is not in the diff")
	}
}

// TestAssistantChangesHostDetection checks every way a new host can be named is flagged, and a
// change that keeps the host is not.
func TestAssistantChangesHostDetection(t *testing.T) {
	e := newChgEnv(t)
	for _, c := range []struct {
		url     string
		changed bool
	}{
		{"https://x.test@evil.test/", true},
		{"https://EVIL.TEST:8443/x", true},
		{"https://x.test/spare2", false},
		{"https://X.TEST:8080/other", false},
		{"https://x.test./a", false},
	} {
		raw, _ := json.Marshal(map[string]any{"id": "o9", "patch": map[string]any{"url": c.url}})
		v, err := e.propose(t, "offers", aiOpUpdate, string(raw))
		if err != nil {
			t.Fatalf("%s: %v", c.url, err)
		}
		if v.ExternalHostChange != c.changed {
			t.Fatalf("%s: external_host_change %v, want %v", c.url, v.ExternalHostChange, c.changed)
		}
	}
	for _, patch := range []string{
		`{"out":"//evil.test/x"}`,
		`{"redirect":"http","out":"evil.test/path"}`,
		`{"streams":[{"name":"main","status":true,"out":{"redirect":"http","out":"https://evil.test/"}}]}`,
	} {
		v, err := e.propose(t, "campaigns", aiOpUpdate, `{"id":"g9","patch":`+patch+`}`)
		if err != nil {
			t.Fatalf("%s: %v", patch, err)
		}
		if !v.ExternalHostChange {
			t.Fatalf("%s: host not flagged", patch)
		}
		marked := false
		for _, d := range v.Diff {
			marked = marked || d.HostChanged
		}
		if !marked {
			t.Fatalf("%s: no diff entry marks the host", patch)
		}
		_, err = e.apply(v.ID, aiApplyOpts{})
		wantChgErr(t, patch, err, http.StatusBadRequest, aiErrConfirmRequired)
	}
}

// TestAssistantChangesCampaignHasLinks checks a campaign that another campaign hands visitors to is not deleted.
func TestAssistantChangesCampaignHasLinks(t *testing.T) {
	for _, target := range []string{"g9", "promo"} {
		e := newChgEnv(t)
		e.groups.list = append(e.groups.list, config.Group{ID: "g5", Name: "Hands over", Status: true, Redirect: "group", Out: target, Streams: []config.Stream{}})
		_, err := e.propose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
		wantChgErr(t, target, err, http.StatusConflict, aiErrHasLinks)
		if !strings.Contains(err.Error(), "g5") || !strings.Contains(err.Error(), "Hands over") {
			t.Fatalf("the refusal does not name the campaign: %v", err)
		}
	}
	e := newChgEnv(t)
	v := e.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
	e.groups.list = append(e.groups.list, config.Group{ID: "g5", Name: "Hands over", Status: true, Redirect: "group", Out: "g9", Streams: []config.Stream{}})
	_, err := e.apply(v.ID, aiApplyOpts{ConfirmDelete: true})
	if err == nil {
		t.Fatal("a campaign that others link to was deleted")
	}
	if _, ok := findGroup(e.groups.list, "g9"); !ok {
		t.Fatal("the campaign is gone")
	}
	if st := e.status(t, v.ID); st == aiChangeDone || st == aiChangePending {
		t.Fatalf("status %q", st)
	}
}

// TestAssistantChangesNetworkLinksNamed checks the refusal to delete a network names its offers.
func TestAssistantChangesNetworkLinksNamed(t *testing.T) {
	e := newChgEnv(t)
	_, err := e.propose(t, "affiliate_networks", aiOpDelete, `{"id":"n1"}`)
	wantChgErr(t, "network", err, http.StatusConflict, aiErrHasLinks)
	if !strings.Contains(err.Error(), "o1") || !strings.Contains(err.Error(), "Alpha") {
		t.Fatalf("the refusal does not name the offer: %v", err)
	}
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Same"}}`)
	if len(v.Links) != 0 {
		t.Fatalf("links %v", v.Links)
	}
}

// TestAssistantRevertKeepsRealValidationError checks a refusal of the write during a revert is not
// turned into ai_conflict, and that the change is done again.
func TestAssistantRevertKeepsRealValidationError(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	e.mustApply(t, v.ID)
	if _, err := e.s.aiChgPatch(v.ID, func(r *aiChangeRec) error {
		r.Change.Before = json.RawMessage(`{"id":"o1","name":"Alpha","url":"ftp://bad.test/","status":true}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.s.revertChange(context.Background(), e.root, v.ID)
	wantChgErr(t, "validation", err, http.StatusBadRequest, "")
	if strings.Contains(err.Error(), aiErrConflict) || !strings.Contains(err.Error(), "url") {
		t.Fatalf("the validation text was lost: %v", err)
	}
	if st := e.status(t, v.ID); st != aiChangeDone {
		t.Fatalf("status %q, want done", st)
	}
}

// TestAssistantRevertSurvivesCanceledContext checks a revert finishes when the request is gone.
func TestAssistantRevertSurvivesCanceledContext(t *testing.T) {
	e := newChgEnv(t)
	original := e.hashOf(t, "offers", "o1")
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	e.mustApply(t, v.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := e.s.revertChange(ctx, e.root, v.ID)
	if err != nil || got.Status != aiChangeReverted {
		t.Fatalf("%+v %v", got, err)
	}
	if e.hashOf(t, "offers", "o1") != original {
		t.Fatal("the record is not back")
	}
}

// TestAssistantRecoverReverting checks a revert that stopped half way is settled by the store.
func TestAssistantRecoverReverting(t *testing.T) {
	e := newChgEnv(t)
	upd := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	e.mustApply(t, upd.ID)
	del := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	e.mustApply(t, del.ID)
	created := e.mustPropose(t, "offers", aiOpCreate, `{"patch":{"name":"Fresh","url":"https://n.test/o","status":true}}`)
	e.mustApply(t, created.ID)
	reverting := func(id string) {
		if _, err := e.s.aiChgPatch(id, func(r *aiChangeRec) error { r.Change.Status = aiChangeReverting; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{upd.ID, del.ID, created.ID} {
		reverting(id)
	}
	e.s.aiRecoverChanges()
	for _, id := range []string{upd.ID, del.ID, created.ID} {
		if st := e.status(t, id); st != aiChangeDone {
			t.Fatalf("the revert never happened, status %q", st)
		}
	}
	for _, id := range []string{upd.ID, del.ID, created.ID} {
		e.mustRevert(t, id)
		reverting(id)
	}
	e.s.aiRecoverChanges()
	for _, id := range []string{upd.ID, del.ID, created.ID} {
		if st := e.status(t, id); st != aiChangeReverted {
			t.Fatalf("the revert had happened, status %q", st)
		}
	}
}

// TestAssistantRecoverCreateWithoutID checks a catalog create that stopped before it learned its id is found by its content.
func TestAssistantRecoverCreateWithoutID(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpCreate, `{"patch":{"name":"Fresh","url":"https://n.test/o","status":true}}`)
	done := e.mustApply(t, v.ID)
	if _, err := e.s.aiChgPatch(v.ID, func(r *aiChangeRec) error {
		r.Change.Status, r.Change.EntityID, r.Proposal.EntityID, r.Change.AfterHash = aiChangePending, "", "", ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.aiRecoverChanges()
	recs, _ := e.s.aiChgRead()
	for _, r := range recs {
		if r.Proposal.ID == v.ID {
			if r.Change.Status != aiChangeDone || r.Change.EntityID != done.EntityID || r.Change.AfterHash == "" {
				t.Fatalf("%+v", r.Change)
			}
		}
	}
	e.mustRevert(t, v.ID)
	if e.hashOf(t, "offers", done.EntityID) != "" {
		t.Fatal("the recovered create was not reverted")
	}
	w := e.mustPropose(t, "offers", aiOpCreate, `{"patch":{"name":"Other","url":"https://m.test/o","status":true}}`)
	e.mustApply(t, w.ID)
	if _, err := e.s.aiChgPatch(w.ID, func(r *aiChangeRec) error {
		r.Change.Status, r.Change.EntityID, r.Proposal.EntityID, r.Change.AfterHash = aiChangePending, "", "", ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	offers, _ := e.s.cfg.Offers.List(context.Background())
	_ = e.s.cfg.Offers.Save(context.Background(), offers[:len(offers)-1])
	e.s.aiRecoverChanges()
	if st := e.status(t, w.ID); st != aiChangeAborted {
		t.Fatalf("a create whose record is gone: status %q", st)
	}
}

// TestAssistantExecHiddenFieldsOneTable checks the one table of hidden campaign fields.
func TestAssistantExecHiddenFieldsOneTable(t *testing.T) {
	for _, k := range []string{"postbacks", "Postback_IDs", "curl", "B_CURL", "remote", "s2s", "S2S_url"} {
		if !aiCampaignHidden(k) {
			t.Fatalf("%s is not hidden", k)
		}
	}
	for _, k := range []string{"name", "streams", "out", "redirect"} {
		if aiCampaignHidden(k) {
			t.Fatalf("%s is hidden", k)
		}
	}
	v := aiStripKeys(map[string]any{
		"name": "x", "s2s_url": "u",
		"streams": []any{map[string]any{"name": "m", "remote": map[string]any{"url": "u"}, "curl": "c"}},
	}, aiCampaignHidden)
	b, _ := json.Marshal(v)
	if string(b) != `{"name":"x","streams":[{"name":"m"}]}` {
		t.Fatalf("%s", b)
	}
}

// TestAssistantChangesHostsOfValues checks the host of each form a URL field can take: with a
// scheme, protocol-relative, bare, in a list, with user info, a port, upper case or a trailing dot.
func TestAssistantChangesHostsOfValues(t *testing.T) {
	for _, c := range []struct {
		key, value string
		want       []string
	}{
		{"url", "https://a.test/x", []string{"a.test"}},
		{"url", "//a.test/x", []string{"a.test"}},
		{"out", "a.test/x", []string{"a.test"}},
		{"out", "a.test", []string{"a.test"}},
		{"out", "a.test:8080?x=1", []string{"a.test"}},
		{"out", "https://a.test/x|||b.test/y|||//c.test", []string{"a.test", "b.test", "c.test"}},
		{"url", "https://user:pw@A.TEST:443/x", []string{"a.test"}},
		{"url", "https://a.test./x", []string{"a.test"}},
		{"url", `https:\\a.test\x`, []string{"a.test"}},
		{"url", "https://a.test/x//b.test/y", []string{"a.test"}},
		{"out", "https://[2001:db8::1]:80/x", []string{"[2001:db8::1]"}},
		{"name", "a.test/x", nil},
		{"name", "see //a.test/x", []string{"a.test"}},
		{"out", "just words", nil},
		{"out", "version 1.2", nil},
		{"out", "203.0.113.5", []string{"203.0.113.5"}},
		{"out", "203.0.113.5/x?a=1", []string{"203.0.113.5"}},
		{"url", "203.0.113.5:8080/x", []string{"203.0.113.5"}},
		{"out", "[2001:db8::1]:8443/x", []string{"[2001:db8::1]"}},
		{"out", "[2001:db8::1]", []string{"[2001:db8::1]"}},
		{"out", "[2001:DB8::1]/x|||1.2.3.4", []string{"[2001:db8::1]", "1.2.3.4"}},
		{"out", "1.2.3", nil},
		{"name", "203.0.113.5/x", nil},
	} {
		got := map[string]bool{}
		aiChgHosts(c.key, c.value, got)
		if len(got) != len(c.want) {
			t.Fatalf("%s %q: %v, want %v", c.key, c.value, got, c.want)
		}
		for _, h := range c.want {
			if !got[h] {
				t.Fatalf("%s %q: %v, want %v", c.key, c.value, got, c.want)
			}
		}
	}
	if !aiChgHostChanged("out", "a.test/x", "b.test/x") || aiChgHostChanged("out", "a.test/x", "https://a.test/y") {
		t.Fatal("aiChgHostChanged compares hosts as a whole")
	}
	if !aiChgHostChanged("out", "1.2.3.4/x", "5.6.7.8/x") || aiChgHostChanged("out", "[::1]:80/a", "http://[::1]/b") || !aiChgHostChanged("url", "https://a.test/", "[2001:db8::1]/x") {
		t.Fatal("a bare address is a host")
	}
	if !aiChgHostChanged("out", "a.test", "a.test.evil.test") || !aiChgHostChanged("out", "evil.test", "evil.test.a.test") {
		t.Fatal("a host that only contains the old host is a new host")
	}
}
