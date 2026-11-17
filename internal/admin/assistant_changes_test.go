package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/security"
)

// chgEnv is an admin server with every catalog, a landing file store, the assistant directory and
// the audit directory under one temporary directory.
type chgEnv struct {
	s      *Server
	groups *memGroups
	root   security.Session
	dir    string
}

// newChgEnv builds the server and seeds one record of each resource that a test may update (o1, n1,
// s1, l1, g1) and one that it may delete (o9, n9, s9, l9 with files, g9 with number 7).
func newChgEnv(t *testing.T) *chgEnv { return newChgEnvWith(t, nil) }

// newChgEnvWith is newChgEnv with a hook that may change the Config before the server starts
// (Config.DataDir holds the temporary directory). tune may be nil.
func newChgEnvWith(t *testing.T, tune func(*Config)) *chgEnv {
	t.Helper()
	hash, err := security.HashPassword("p@ss")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e := &chgEnv{groups: &memGroups{}, dir: dir}
	cfg := Config{
		AdminUser: "admin", PasswordHash: hash, UsersFile: filepath.Join(dir, "users.json"),
		Sessions: security.NewMemoryStore(), Stats: fakeStats{}, Groups: e.groups, Limiter: allowAll{},
		SessionTTL: time.Hour, DataDir: dir, AIDir: filepath.Join(dir, "assistant"),
		Boards:       NewJSONStore[config.Board](filepath.Join(dir, "boards.json")),
		Offers:       NewJSONStore[config.Offer](filepath.Join(dir, "offers.json")),
		Landings:     NewJSONStore[config.Landing](filepath.Join(dir, "landings.json")),
		Sources:      NewJSONStore[config.TrafficSource](filepath.Join(dir, "sources.json")),
		Networks:     NewJSONStore[config.AffiliateNetwork](filepath.Join(dir, "networks.json")),
		SeqOffers:    NewIDSeq(filepath.Join(dir, "offer_seq.json")),
		SeqLandings:  NewIDSeq(filepath.Join(dir, "landing_seq.json")),
		SeqSources:   NewIDSeq(filepath.Join(dir, "source_seq.json")),
		SeqNetworks:  NewIDSeq(filepath.Join(dir, "network_seq.json")),
		LandingFiles: NewFileLandings(filepath.Join(dir, "landing-files")),
	}
	if tune != nil {
		tune(&cfg)
	}
	if cfg.AIFile == "" {
		cfg.AIFile = filepath.Join(dir, "ai.json")
		if err := os.WriteFile(cfg.AIFile, []byte(`{"perms":{"preset":"full"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.Handler()
	e.s = s
	e.root = security.Session{User: "admin", PwFP: s.current().fp}
	e.seed(t)
	return e
}

// seed fills the stores directly, as an earlier session of some other user left them.
func (e *chgEnv) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.s.cfg.Networks.Save(ctx, []config.AffiliateNetwork{
		{ID: "n1", Name: "Net", Status: true, PostbackURL: "https://t.test/pb?cid={subid}", CreatedBy: "legacy"},
		{ID: "n9", Name: "Spare net", Status: true, PostbackURL: "https://t.test/pb9?cid={subid}", CreatedBy: "legacy"},
	}))
	must(e.s.cfg.Offers.Save(ctx, []config.Offer{
		{ID: "o1", Name: "Alpha", Status: true, Payout: 7, PayoutCurrency: "USD", Countries: []string{"pl", "de"},
			URL: "https://x.test/p/" + aiLongToken + "?token=CANARYQ&sig=CANARYSIG&cid={subid}", NetworkID: "n1", CreatedBy: "legacy"},
		{ID: "o9", Name: "Spare", Status: true, URL: "https://x.test/spare", CreatedBy: "legacy", Group: "spare"},
	}))
	must(e.s.cfg.Sources.Save(ctx, []config.TrafficSource{
		{ID: "s1", Name: "Src", CreatedBy: "legacy"},
		{ID: "s9", Name: "Spare src", Parameters: map[string]string{"sub_id_1": "sid"}, CreatedBy: "legacy"},
	}))
	must(e.s.cfg.Landings.Save(ctx, []config.Landing{
		{ID: "l1", Name: "Land", Status: true, URL: "https://l.test/a", CreatedBy: "legacy"},
		{ID: "l9", Name: "Spare land", Status: true, Local: true, Entry: "index.html", CreatedBy: "legacy"},
	}))
	files := map[string][]byte{
		"index.html":        []byte("<html>spare</html>"),
		"img/pixel.png":     {0x89, 'P', 'N', 'G', 0, 1, 2, 3, 0xff, 0xfe},
		"assets/app.min.js": []byte("var a=1;"),
	}
	for name, data := range files {
		must(e.s.cfg.LandingFiles.Write("l9", name, data))
	}
	must(e.groups.Save(ctx, []config.Group{
		{ID: "g1", Name: "Demo", Status: true, Num: 1, Redirect: "stop", CreatedBy: "legacy", Aliases: []string{"demo"},
			Postbacks:   []config.OutboundPostback{{URL: "https://hook.test/?token=CANARYPB", Method: "GET"}},
			PostbackIDs: []string{"s2s1"},
			Streams:     []config.Stream{{Name: "main", Status: true, Curl: "CANARYCURL", BotCurl: "CANARYBCURL"}}},
		{ID: "g9", Name: "Spare campaign", Status: true, Num: 7, Redirect: "stop", CreatedBy: "legacy", Aliases: []string{"promo"},
			Streams: []config.Stream{{Name: "main", Status: true}}},
	}))
}

// tool returns the propose tool of the registry for a resource and an operation.
func (e *chgEnv) tool(t *testing.T, resource, op string) aiTool {
	t.Helper()
	for _, tl := range aiRegistry() {
		if tl.Mode == asstPropose && tl.Resource == resource && tl.Op == op {
			return tl
		}
	}
	t.Fatalf("no propose tool for %s %s", resource, op)
	return aiTool{}
}

// propose runs aiPropose for the root session in the chat "c1", message "m1".
func (e *chgEnv) propose(t *testing.T, resource, op, args string) (aiChangeView, error) {
	t.Helper()
	return e.s.aiPropose(context.Background(), aiProposeReq{
		Sess: e.root, ChatID: "c1", MsgID: "m1", Tool: e.tool(t, resource, op), Args: json.RawMessage(args),
	})
}

// mustPropose is propose that fails the test on an error.
func (e *chgEnv) mustPropose(t *testing.T, resource, op, args string) aiChangeView {
	t.Helper()
	v, err := e.propose(t, resource, op, args)
	if err != nil {
		t.Fatalf("propose %s %s: %v", resource, op, err)
	}
	if v.Status != aiChangeProposed {
		t.Fatalf("propose status %q", v.Status)
	}
	return v
}

// apply applies change id as the root session.
func (e *chgEnv) apply(id string, opts aiApplyOpts) (aiChangeView, error) {
	return e.s.applyChange(context.Background(), e.root, id, opts)
}

// mustApply applies with the confirmations of a delete and fails the test on an error.
func (e *chgEnv) mustApply(t *testing.T, id string) aiChangeView {
	t.Helper()
	v, err := e.apply(id, aiApplyOpts{ConfirmDelete: true, ConfirmHost: true})
	if err != nil {
		t.Fatalf("apply %s: %v", id, err)
	}
	if v.Status != aiChangeDone {
		t.Fatalf("apply status %q", v.Status)
	}
	return v
}

// mustRevert reverts change id as the root session and fails the test on an error.
func (e *chgEnv) mustRevert(t *testing.T, id string) aiChangeView {
	t.Helper()
	v, err := e.s.revertChange(context.Background(), e.root, id)
	if err != nil {
		t.Fatalf("revert %s: %v", id, err)
	}
	if v.Status != aiChangeReverted {
		t.Fatalf("revert status %q", v.Status)
	}
	return v
}

// hashOf returns the hash of the stored record, or "" when it is missing.
func (e *chgEnv) hashOf(t *testing.T, resource, id string) string {
	t.Helper()
	raw, ok, err := e.s.aiChgStored(context.Background(), resource, id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return ""
	}
	return rawHash(raw)
}

// status returns the status of change id from changes.json.
func (e *chgEnv) status(t *testing.T, id string) string {
	t.Helper()
	recs, err := e.s.aiChgRead()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Proposal.ID == id {
			return r.Change.Status
		}
	}
	t.Fatalf("change %s missing", id)
	return ""
}

// wantErr fails unless err is an aiError with the status and, when code is not empty, the code.
func wantChgErr(t *testing.T, label string, err error, status int, code string) {
	t.Helper()
	var ae *aiError
	if !errors.As(err, &ae) {
		t.Fatalf("%s: error %v, want status %d %s", label, err, status, code)
	}
	got := ae.code
	if got == "" {
		got = aiCodeOf(ae.msg)
	}
	if ae.status != status || (code != "" && got != code) {
		t.Fatalf("%s: status %d code %q (%s), want %d %s", label, ae.status, got, ae.msg, status, code)
	}
}

// otherAdmin creates a second administrator account and returns its session.
func (e *chgEnv) otherAdmin(t *testing.T, login string) security.Session {
	t.Helper()
	created, err := e.s.createUser(userDraft{Login: login, Password: "secret1", Type: security.UserAdmin, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	u, ok := e.s.userByID(created.ID)
	if !ok {
		t.Fatal("user missing")
	}
	return security.Session{User: u.Login, UID: u.ID, PwFP: u.Fingerprint()}
}

// landingBytes returns every file of the landing id by path.
func (e *chgEnv) landingBytes(t *testing.T, id string) map[string][]byte {
	t.Helper()
	list, err := e.s.cfg.LandingFiles.List(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range list {
		b, err := e.s.cfg.LandingFiles.Read(id, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		out[f.Path] = b
	}
	return out
}

// TestAssistantApplyRevertEveryKind applies and reverts create, update and delete of every
// resource and checks the record is the same, byte for byte, after the revert.
func TestAssistantApplyRevertEveryKind(t *testing.T) {
	cases := []struct {
		resource, op, args, id string
	}{
		{"offers", aiOpCreate, `{"patch":{"name":"New offer","url":"https://n.test/o","status":true}}`, ""},
		{"offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Renamed","payout":12.5,"countries":["fr"]}}`, "o1"},
		{"offers", aiOpDelete, `{"id":"o9"}`, "o9"},
		{"affiliate_networks", aiOpCreate, `{"patch":{"name":"New net","status":true,"postback_url":"https://t.test/new?cid={subid}"}}`, ""},
		{"affiliate_networks", aiOpUpdate, `{"id":"n1","patch":{"name":"Net renamed"}}`, "n1"},
		{"affiliate_networks", aiOpDelete, `{"id":"n9"}`, "n9"},
		{"traffic_sources", aiOpCreate, `{"patch":{"name":"New source"}}`, ""},
		{"traffic_sources", aiOpUpdate, `{"id":"s1","patch":{"name":"Src renamed","parameters":{"keyword":"kw"}}}`, "s1"},
		{"traffic_sources", aiOpDelete, `{"id":"s9"}`, "s9"},
		{"landings", aiOpCreate, `{"patch":{"name":"New landing","url":"https://l.test/new","status":true}}`, ""},
		{"landings", aiOpUpdate, `{"id":"l1","patch":{"name":"Land renamed"}}`, "l1"},
		{"landings", aiOpDelete, `{"id":"l9"}`, "l9"},
		{"campaigns", aiOpCreate, `{"patch":{"name":"New campaign"}}`, ""},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"name":"Demo renamed","aliases":["demo","demo2"]}}`, "g1"},
		{"campaigns", aiOpDelete, `{"id":"g9"}`, "g9"},
	}
	for _, c := range cases {
		t.Run(c.resource+"_"+c.op, func(t *testing.T) {
			e := newChgEnv(t)
			before := ""
			if c.id != "" {
				before = e.hashOf(t, c.resource, c.id)
			}
			var filesBefore map[string][]byte
			if c.resource == "landings" && c.op == aiOpDelete {
				filesBefore = e.landingBytes(t, c.id)
			}
			v := e.mustPropose(t, c.resource, c.op, c.args)
			if c.id != "" && e.hashOf(t, c.resource, c.id) != before {
				t.Fatal("propose changed the record")
			}
			done := e.mustApply(t, v.ID)
			id := c.id
			if c.op == aiOpCreate {
				id = done.EntityID
				if id == "" || e.hashOf(t, c.resource, id) == "" {
					t.Fatalf("created record %q missing", id)
				}
			}
			after := e.hashOf(t, c.resource, id)
			switch c.op {
			case aiOpDelete:
				if after != "" {
					t.Fatal("record still exists after delete")
				}
			case aiOpUpdate:
				if after == before {
					t.Fatal("record unchanged after update")
				}
			}
			if c.resource == "landings" && c.op == aiOpDelete && len(e.landingBytes(t, id)) != 0 {
				t.Fatal("landing files remain after delete")
			}
			e.mustRevert(t, v.ID)
			got := e.hashOf(t, c.resource, id)
			if c.op == aiOpCreate && got != "" {
				t.Fatal("created record remains after revert")
			}
			if c.op != aiOpCreate && got != before {
				t.Fatalf("record differs after revert: %s != %s", got, before)
			}
			if filesBefore != nil {
				back := e.landingBytes(t, id)
				if len(back) != len(filesBefore) {
					t.Fatalf("landing files %d, want %d", len(back), len(filesBefore))
				}
				for name, data := range filesBefore {
					if string(back[name]) != string(data) {
						t.Fatalf("landing file %s differs", name)
					}
				}
			}
			if st := e.status(t, v.ID); st != aiChangeReverted {
				t.Fatalf("status %q", st)
			}
		})
	}
}

// TestAssistantApplyKeepsOwnerBoardAndHiddenFields checks an update and its revert leave the owner
// of the record and the hidden campaign fields alone.
func TestAssistantApplyKeepsOwnerBoardAndHiddenFields(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "campaigns", aiOpUpdate, `{"id":"g1","patch":{"name":"Other","streams":[{"name":"main","status":true}]}}`)
	e.mustApply(t, v.ID)
	g, ok := findGroup(e.groups.list, "g1")
	if !ok || g.CreatedBy != "legacy" || len(g.Postbacks) != 1 || g.Streams[0].Curl != "CANARYCURL" || g.Streams[0].BotCurl != "CANARYBCURL" {
		t.Fatalf("hidden fields lost: %+v", g)
	}
	o := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"X"}}`)
	e.mustApply(t, o.ID)
	offers, _ := e.s.cfg.Offers.List(context.Background())
	for _, of := range offers {
		if of.ID == "o1" && (of.CreatedBy != "legacy" || of.NetworkID != "n1") {
			t.Fatalf("owner or link lost: %+v", of)
		}
	}
}

// TestAssistantChangesProposeWritesNothing checks a proposal touches no store and no landing file.
func TestAssistantChangesProposeWritesNothing(t *testing.T) {
	e := newChgEnv(t)
	files := []string{"offers.json", "networks.json", "sources.json", "landings.json"}
	snap := func() map[string]string {
		out := map[string]string{}
		for _, f := range files {
			b, _ := os.ReadFile(filepath.Join(e.dir, f))
			out[f] = string(b)
		}
		g, _ := json.Marshal(e.groups.list)
		out["groups"] = string(g)
		return out
	}
	before := snap()
	filesBefore := e.landingBytes(t, "l9")
	e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Y"}}`)
	e.mustPropose(t, "offers", aiOpCreate, `{"patch":{"name":"Z","url":"https://z.test/","status":true}}`)
	e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	e.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
	e.mustPropose(t, "campaigns", aiOpCreate, `{"patch":{"name":"C"}}`)
	after := snap()
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("propose changed %s", k)
		}
	}
	if len(e.landingBytes(t, "l9")) != len(filesBefore) {
		t.Fatal("propose changed landing files")
	}
}

// TestAssistantApplyStale checks a record edited between propose and apply is ai_stale.
func TestAssistantApplyStale(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	offers, _ := e.s.cfg.Offers.List(context.Background())
	offers[0].Name = "Hand edit"
	if err := e.s.cfg.Offers.Save(context.Background(), offers); err != nil {
		t.Fatal(err)
	}
	_, err := e.apply(v.ID, aiApplyOpts{})
	wantChgErr(t, "stale", err, http.StatusConflict, aiErrStale)
	if st := e.status(t, v.ID); st != aiChangeProposed {
		t.Fatalf("status %q, want proposed", st)
	}
	d := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	offers, _ = e.s.cfg.Offers.List(context.Background())
	offers[1].Name = "Changed"
	_ = e.s.cfg.Offers.Save(context.Background(), offers)
	_, err = e.apply(d.ID, aiApplyOpts{ConfirmDelete: true})
	wantChgErr(t, "stale delete", err, http.StatusConflict, aiErrStale)
}

// TestAssistantRevertConflictAfterManualEdit checks a record edited after the assistant is never overwritten.
func TestAssistantRevertConflictAfterManualEdit(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	e.mustApply(t, v.ID)
	offers, _ := e.s.cfg.Offers.List(context.Background())
	offers[0].Name = "Hand edit"
	_ = e.s.cfg.Offers.Save(context.Background(), offers)
	_, err := e.s.revertChange(context.Background(), e.root, v.ID)
	wantChgErr(t, "manual edit", err, http.StatusConflict, aiErrConflict)
	offers, _ = e.s.cfg.Offers.List(context.Background())
	if offers[0].Name != "Hand edit" {
		t.Fatal("revert overwrote a manual edit")
	}
	if st := e.status(t, v.ID); st != aiChangeDone {
		t.Fatalf("status %q, want done", st)
	}
	c := e.mustPropose(t, "offers", aiOpCreate, `{"patch":{"name":"New","url":"https://n.test/o","status":true}}`)
	cd := e.mustApply(t, c.ID)
	offers, _ = e.s.cfg.Offers.List(context.Background())
	for i := range offers {
		if offers[i].ID == cd.EntityID {
			offers[i].Name = "Edited new"
		}
	}
	_ = e.s.cfg.Offers.Save(context.Background(), offers)
	_, err = e.s.revertChange(context.Background(), e.root, c.ID)
	wantChgErr(t, "created then edited", err, http.StatusConflict, aiErrConflict)
}

// TestAssistantRevertSecondTimeIsConflict checks a change is reverted once.
func TestAssistantRevertSecondTimeIsConflict(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	e.mustApply(t, v.ID)
	e.mustRevert(t, v.ID)
	_, err := e.s.revertChange(context.Background(), e.root, v.ID)
	wantChgErr(t, "second revert", err, http.StatusConflict, aiErrConflict)
	_, err = e.apply(v.ID, aiApplyOpts{})
	wantChgErr(t, "apply reverted", err, http.StatusConflict, aiErrConflict)
}

// TestAssistantRevertDeleteConflicts checks a restored record never takes an id, slug, alias or link that moved on.
func TestAssistantRevertDeleteConflicts(t *testing.T) {
	ctx := context.Background()
	t.Run("offer id taken", func(t *testing.T) {
		e := newChgEnv(t)
		v := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
		e.mustApply(t, v.ID)
		offers, _ := e.s.cfg.Offers.List(ctx)
		offers = append(offers, config.Offer{ID: "o9", Name: "Squatter", URL: "https://s.test/", Status: true})
		_ = e.s.cfg.Offers.Save(ctx, offers)
		_, err := e.s.revertChange(ctx, e.root, v.ID)
		wantChgErr(t, "taken id", err, http.StatusConflict, aiErrConflict)
		offers, _ = e.s.cfg.Offers.List(ctx)
		if offers[len(offers)-1].Name != "Squatter" {
			t.Fatal("the other record was overwritten")
		}
	})
	t.Run("network gone", func(t *testing.T) {
		e := newChgEnv(t)
		offers, _ := e.s.cfg.Offers.List(ctx)
		offers[1].NetworkID = "n9"
		_ = e.s.cfg.Offers.Save(ctx, offers)
		v := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
		e.mustApply(t, v.ID)
		_ = e.s.cfg.Networks.Save(ctx, []config.AffiliateNetwork{{ID: "n1", Name: "Net", Status: true, PostbackURL: "https://t.test/pb?cid={subid}"}})
		_, err := e.s.revertChange(ctx, e.root, v.ID)
		wantChgErr(t, "link gone", err, http.StatusConflict, aiErrConflict)
		if e.hashOf(t, "offers", "o9") != "" {
			t.Fatal("record was restored over a missing network")
		}
	})
	t.Run("campaign alias taken", func(t *testing.T) {
		e := newChgEnv(t)
		v := e.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
		e.mustApply(t, v.ID)
		e.groups.list = append(e.groups.list, config.Group{ID: "g5", Name: "Other", Status: true, Redirect: "stop", Aliases: []string{"promo"}, Streams: []config.Stream{}})
		_, err := e.s.revertChange(ctx, e.root, v.ID)
		wantChgErr(t, "alias taken", err, http.StatusConflict, aiErrConflict)
		if _, ok := findGroup(e.groups.list, "g9"); ok {
			t.Fatal("campaign restored over a taken alias")
		}
	})
	t.Run("campaign id taken", func(t *testing.T) {
		e := newChgEnv(t)
		v := e.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
		e.mustApply(t, v.ID)
		e.groups.list = append(e.groups.list, config.Group{ID: "g9", Name: "Squatter", Status: true, Redirect: "stop", Streams: []config.Stream{}})
		_, err := e.s.revertChange(ctx, e.root, v.ID)
		wantChgErr(t, "taken campaign id", err, http.StatusConflict, aiErrConflict)
	})
	t.Run("landing id taken", func(t *testing.T) {
		e := newChgEnv(t)
		v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
		e.mustApply(t, v.ID)
		ls, _ := e.s.cfg.Landings.List(ctx)
		ls = append(ls, config.Landing{ID: "l9", Name: "Squatter", URL: "https://q.test/", Status: true})
		_ = e.s.cfg.Landings.Save(ctx, ls)
		_, err := e.s.revertChange(ctx, e.root, v.ID)
		wantChgErr(t, "taken landing id", err, http.StatusConflict, aiErrConflict)
		if len(e.landingBytes(t, "l9")) != 0 {
			t.Fatal("files were written for a landing that was not restored")
		}
	})
	t.Run("landing files added", func(t *testing.T) {
		e := newChgEnv(t)
		v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
		e.mustApply(t, v.ID)
		_ = e.s.cfg.LandingFiles.Write("l9", "other.html", []byte("mine"))
		_, err := e.s.revertChange(ctx, e.root, v.ID)
		wantChgErr(t, "files present", err, http.StatusConflict, aiErrConflict)
		if got := e.landingBytes(t, "l9"); len(got) != 1 || string(got["other.html"]) != "mine" {
			t.Fatalf("files changed: %v", got)
		}
	})
}

// TestAssistantRevertRenumbersTakenNum checks a restored campaign gets a new number when its own is taken.
func TestAssistantRevertRenumbersTakenNum(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "campaigns", aiOpDelete, `{"id":"g9"}`)
	e.mustApply(t, v.ID)
	e.groups.list = append(e.groups.list, config.Group{ID: "g5", Name: "Took the number", Status: true, Num: 7, Redirect: "stop", Streams: []config.Stream{}})
	back := e.mustRevert(t, v.ID)
	if !back.Renumbered {
		t.Fatal("renumbered not reported")
	}
	g, ok := findGroup(e.groups.list, "g9")
	if !ok || g.Num == 7 || g.CreatedBy != "legacy" {
		t.Fatalf("restored campaign: %+v", g)
	}
	if other, _ := findGroup(e.groups.list, "g5"); other.Num != 7 {
		t.Fatal("the other campaign lost its number")
	}
}

// TestAssistantApplyNeedsConfirmation checks the two confirmations.
func TestAssistantApplyNeedsConfirmation(t *testing.T) {
	e := newChgEnv(t)
	d := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	_, err := e.apply(d.ID, aiApplyOpts{})
	wantChgErr(t, "delete", err, http.StatusBadRequest, aiErrConfirmRequired)
	if e.hashOf(t, "offers", "o9") == "" || e.status(t, d.ID) != aiChangeProposed {
		t.Fatal("an unconfirmed delete changed state")
	}
	h := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"url":"https://elsewhere.example/o"}}`)
	if !h.ExternalHostChange {
		t.Fatal("host change not flagged")
	}
	hostFlag := false
	for _, d := range h.Diff {
		hostFlag = hostFlag || d.HostChanged
	}
	if !hostFlag {
		t.Fatal("no diff entry marks the host")
	}
	_, err = e.apply(h.ID, aiApplyOpts{ConfirmDelete: true})
	wantChgErr(t, "host", err, http.StatusBadRequest, aiErrConfirmRequired)
	if _, err := e.apply(h.ID, aiApplyOpts{ConfirmHost: true}); err != nil {
		t.Fatal(err)
	}
	n := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Same host"}}`)
	if n.ExternalHostChange {
		t.Fatal("a rename flagged as a host change")
	}
}

// TestAssistantChangesHasLinks checks a delete of a record that others use is refused.
func TestAssistantChangesHasLinks(t *testing.T) {
	e := newChgEnv(t)
	e.groups.list[0].Streams[0].Out = config.Output{
		Offers:   []config.ShareRef{{ID: "o1"}},
		Landings: []config.ShareRef{{ID: "l1"}},
	}
	e.groups.list[0].SourceID = "s1"
	for _, c := range []struct{ resource, id string }{
		{"offers", "o1"}, {"landings", "l1"}, {"traffic_sources", "s1"}, {"affiliate_networks", "n1"},
	} {
		_, err := e.propose(t, c.resource, aiOpDelete, `{"id":"`+c.id+`"}`)
		wantChgErr(t, c.resource, err, http.StatusConflict, aiErrHasLinks)
	}
}

// TestAssistantChangesHasLinksAtApply checks a link added between propose and apply stops the delete.
func TestAssistantChangesHasLinksAtApply(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	e.groups.list[0].Streams[0].Out = config.Output{Offers: []config.ShareRef{{ID: "o9"}}}
	_, err := e.apply(v.ID, aiApplyOpts{ConfirmDelete: true})
	wantChgErr(t, "link added", err, http.StatusConflict, aiErrHasLinks)
	if e.hashOf(t, "offers", "o9") == "" {
		t.Fatal("offer deleted while linked")
	}
}

// TestAssistantChangesForbiddenPatches checks the fields no patch may set.
func TestAssistantChangesForbiddenPatches(t *testing.T) {
	e := newChgEnv(t)
	cases := []struct{ resource, op, args string }{
		{"offers", aiOpUpdate, `{"id":"o1","patch":{"id":"x"}}`},
		{"offers", aiOpUpdate, `{"id":"o1","patch":{"created_by":"me"}}`},
		{"offers", aiOpUpdate, `{"id":"o1","patch":{"board_id":"b2"}}`},
		{"offers", aiOpUpdate, `{"id":"o1","patch":{"nonsense":1}}`},
		{"offers", aiOpCreate, `{"patch":{"id":"o77","name":"x","url":"https://a.test/"}}`},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"num":99}}`},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"postbacks":[{"url":"https://evil.test/","method":"GET"}]}}`},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"postback_ids":["s2s1"]}}`},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"curl":"a|b"}}`},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"b_curl":"a|b"}}`},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"streams":[{"name":"main","status":true,"curl":"a|b"}]}}`},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"streams":[{"name":"main","status":true,"b_curl":"a|b"}]}}`},
		{"campaigns", aiOpUpdate, `{"id":"g1","patch":{"s2s_url":"https://evil.test/"}}`},
		{"campaigns", aiOpCreate, `{"patch":{"name":"x","postbacks":[]}}`},
		{"campaigns", aiOpCreate, `{"patch":{"name":"x","created_by":"me"}}`},
		{"offers", aiOpUpdate, `{"id":"o1","patch":{"name":"x"},"extra":1}`},
	}
	for _, c := range cases {
		_, err := e.propose(t, c.resource, c.op, c.args)
		wantChgErr(t, c.args, err, http.StatusBadRequest, "")
	}
	recs, _ := e.s.aiChgRead()
	if len(recs) != 0 {
		t.Fatalf("%d changes stored for refused patches", len(recs))
	}
}

// TestAssistantChangesRedactedValue checks a patch that carries a redaction mark is refused.
func TestAssistantChangesRedactedValue(t *testing.T) {
	e := newChgEnv(t)
	for _, args := range []string{
		`{"id":"o1","patch":{"url":"https://x.test/p?token=***"}}`,
		`{"id":"o1","patch":{"name":"***"}}`,
	} {
		_, err := e.propose(t, "offers", aiOpUpdate, args)
		wantChgErr(t, args, err, http.StatusBadRequest, aiErrRedactedValue)
	}
}

// TestAssistantChangesEllipsisName checks a short name that ends in an ellipsis is the user's own
// text and passes, while a long string cut by aiCompress is refused.
func TestAssistantChangesEllipsisName(t *testing.T) {
	e := newChgEnv(t)
	e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Акция…"}}`)
	e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Акция… лето"}}`)
	_, err := e.propose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"`+strings.Repeat("a", 70)+`…"}}`)
	wantChgErr(t, "cut name", err, http.StatusBadRequest, aiErrRedactedValue)
}

// TestAssistantChangesExistence checks update needs the record and create needs its absence.
func TestAssistantChangesExistence(t *testing.T) {
	e := newChgEnv(t)
	_, err := e.propose(t, "campaigns", aiOpUpdate, `{"id":"nope","patch":{"name":"x"}}`)
	wantChgErr(t, "update missing campaign", err, http.StatusNotFound, "")
	_, err = e.propose(t, "offers", aiOpUpdate, `{"id":"nope","patch":{"name":"x"}}`)
	wantChgErr(t, "update missing offer", err, http.StatusNotFound, "")
	_, err = e.propose(t, "offers", aiOpDelete, `{"id":"nope"}`)
	wantChgErr(t, "delete missing offer", err, http.StatusNotFound, "")
	_, err = e.propose(t, "campaigns", aiOpCreate, `{"id":"g1","patch":{"name":"dup"}}`)
	wantChgErr(t, "create existing campaign", err, http.StatusConflict, aiErrConflict)
	_, err = e.propose(t, "offers", aiOpCreate, `{"id":"o1","patch":{"name":"dup","url":"https://d.test/"}}`)
	if err == nil {
		t.Fatal("create of an existing offer accepted")
	}
	v := e.mustPropose(t, "campaigns", aiOpCreate, `{"patch":{"name":"fresh"}}`)
	if v.EntityID == "" {
		t.Fatal("a created campaign has no id")
	}
	e.mustApply(t, v.ID)
	if _, ok := findGroup(e.groups.list, v.EntityID); !ok {
		t.Fatal("campaign not created under the proposed id")
	}
	taken := e.mustPropose(t, "campaigns", aiOpCreate, `{"patch":{"name":"second"}}`)
	if taken.EntityID == v.EntityID {
		t.Fatal("two proposals got one id")
	}
}

// TestAssistantApplyCreateRaceIsConflict checks a create whose id appeared before apply is refused.
func TestAssistantApplyCreateRaceIsConflict(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "campaigns", aiOpCreate, `{"patch":{"name":"fresh"}}`)
	e.groups.list = append(e.groups.list, config.Group{ID: v.EntityID, Name: "Appeared", Status: true, Redirect: "stop", Streams: []config.Stream{}})
	_, err := e.apply(v.ID, aiApplyOpts{})
	wantChgErr(t, "create race", err, http.StatusConflict, aiErrConflict)
	if g, _ := findGroup(e.groups.list, v.EntityID); g.Name != "Appeared" {
		t.Fatal("the existing campaign was overwritten")
	}
}

// TestAssistantApplyRefusedWhileKeitaroRuns checks apply and revert wait for an import to end.
func TestAssistantApplyRefusedWhileKeitaroRuns(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	w := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o9","patch":{"name":"Other"}}`)
	e.mustApply(t, w.ID)
	if msg := e.s.ktJob.beginImport(); msg != "" {
		t.Fatal(msg)
	}
	before := e.hashOf(t, "offers", "o1")
	_, err := e.apply(v.ID, aiApplyOpts{})
	wantChgErr(t, "apply", err, http.StatusConflict, aiErrKeitaroRunning)
	_, err = e.s.revertChange(context.Background(), e.root, w.ID)
	wantChgErr(t, "revert", err, http.StatusConflict, aiErrKeitaroRunning)
	if e.hashOf(t, "offers", "o1") != before || e.status(t, v.ID) != aiChangeProposed || e.status(t, w.ID) != aiChangeDone {
		t.Fatal("a refused write changed state")
	}
	e.s.ktJob.finish("", nil, nil, "")
	e.mustApply(t, v.ID)
	e.mustRevert(t, w.ID)
}

// TestAssistantRevertMessage checks a reply is rolled back last change first, and stops on a conflict.
func TestAssistantRevertMessage(t *testing.T) {
	t.Run("reverse order", func(t *testing.T) {
		e := newChgEnv(t)
		original := e.hashOf(t, "offers", "o1")
		a := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"First"}}`)
		e.mustApply(t, a.ID)
		b := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Second"}}`)
		e.mustApply(t, b.ID)
		report, err := e.s.revertMessage(context.Background(), e.root, "c1", "m1")
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Reverted) != 2 || report.Reverted[0] != b.ID || report.Reverted[1] != a.ID || report.StoppedAt != "" {
			t.Fatalf("report %+v", report)
		}
		if e.hashOf(t, "offers", "o1") != original {
			t.Fatal("the offer is not back to the original")
		}
	})
	t.Run("stops on conflict", func(t *testing.T) {
		e := newChgEnv(t)
		a := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"First"}}`)
		e.mustApply(t, a.ID)
		b := e.mustPropose(t, "affiliate_networks", aiOpUpdate, `{"id":"n1","patch":{"name":"Second"}}`)
		e.mustApply(t, b.ID)
		offers, _ := e.s.cfg.Offers.List(context.Background())
		offers[0].Name = "Hand edit"
		_ = e.s.cfg.Offers.Save(context.Background(), offers)
		report, err := e.s.revertMessage(context.Background(), e.root, "c1", "m1")
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Reverted) != 1 || report.Reverted[0] != b.ID || report.StoppedAt != a.ID || report.Error == "" {
			t.Fatalf("report %+v", report)
		}
		if e.status(t, a.ID) != aiChangeDone || e.status(t, b.ID) != aiChangeReverted {
			t.Fatal("wrong statuses after a stopped run")
		}
	})
	t.Run("other message untouched", func(t *testing.T) {
		e := newChgEnv(t)
		a := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"First"}}`)
		e.mustApply(t, a.ID)
		report, err := e.s.revertMessage(context.Background(), e.root, "c1", "other")
		if err != nil || len(report.Reverted) != 0 {
			t.Fatalf("%+v %v", report, err)
		}
	})
}

// TestAssistantRecoverChanges checks a change left pending by a stopped process is settled by what the store holds.
func TestAssistantRecoverChanges(t *testing.T) {
	e := newChgEnv(t)
	done := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Landed"}}`)
	e.mustApply(t, done.ID)
	lost := e.mustPropose(t, "affiliate_networks", aiOpUpdate, `{"id":"n1","patch":{"name":"Lost"}}`)
	e.mustApply(t, lost.ID)
	gone := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o9"}`)
	e.mustApply(t, gone.ID)
	pending := func(id string) {
		if _, err := e.s.aiChgPatch(id, func(r *aiChangeRec) error { r.Change.Status = aiChangePending; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{done.ID, lost.ID, gone.ID} {
		pending(id)
	}
	nets, _ := e.s.cfg.Networks.List(context.Background())
	nets[0].Name = "Edited meanwhile"
	_ = e.s.cfg.Networks.Save(context.Background(), nets)
	e.s.aiRecoverChanges()
	if st := e.status(t, done.ID); st != aiChangeDone {
		t.Fatalf("written change: %q", st)
	}
	if st := e.status(t, gone.ID); st != aiChangeDone {
		t.Fatalf("deleted record: %q", st)
	}
	if st := e.status(t, lost.ID); st != aiChangeAborted {
		t.Fatalf("diverged record: %q", st)
	}
	recs, _ := e.s.aiChgRead()
	for _, r := range recs {
		if r.Proposal.ID == lost.ID && (r.Change.Error == "" || r.Change.Before != nil) {
			t.Fatalf("aborted entry keeps data: %+v", r.Change)
		}
	}
}

// TestAssistantApplyAudit checks the audit line of an apply and of a revert.
func TestAssistantApplyAudit(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Audited"}}`)
	e.mustApply(t, v.ID)
	e.mustRevert(t, v.ID)
	files, _ := filepath.Glob(filepath.Join(e.dir, "audit", "*"))
	if len(files) == 0 {
		t.Fatal("no audit file")
	}
	var text string
	for _, f := range files {
		b, _ := os.ReadFile(f)
		text += string(b)
	}
	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		var row auditEntry
		if err := json.Unmarshal([]byte(l), &row); err != nil {
			t.Fatalf("bad audit line %q", l)
		}
		if row.ChangeID == v.ID {
			lines++
			if row.Source != "assistant" || row.UID != "env-admin" || row.Status != http.StatusOK {
				t.Fatalf("audit row %+v", row)
			}
		}
	}
	if lines != 2 {
		t.Fatalf("%d audit rows for the change, want 2", lines)
	}
}

// TestAssistantChangesFilePermissions checks the modes of changes.json, the blobs and their directories.
func TestAssistantChangesFilePermissions(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	e.mustApply(t, v.ID)
	assistant := filepath.Join(e.dir, "assistant")
	for _, c := range []struct {
		path string
		want os.FileMode
	}{
		{assistant, 0o700},
		{filepath.Join(assistant, "changes.json"), 0o600},
		{filepath.Join(assistant, "blobs"), 0o700},
		{filepath.Join(assistant, "blobs", v.ID+".tar.gz"), 0o600},
	} {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != c.want {
			t.Fatalf("%s mode %o, want %o", c.path, info.Mode().Perm(), c.want)
		}
	}
	e.mustRevert(t, v.ID)
	if _, err := os.Stat(filepath.Join(assistant, "blobs", v.ID+".tar.gz")); !os.IsNotExist(err) {
		t.Fatal("the snapshot stays after the revert")
	}
}

// TestAssistantChangesOwnerOnly checks another administrator and a plain user cannot touch a change.
func TestAssistantChangesOwnerOnly(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Mine"}}`)
	other := e.otherAdmin(t, "second")
	ctx := context.Background()
	_, err := e.s.applyChange(ctx, other, v.ID, aiApplyOpts{})
	wantChgErr(t, "foreign apply", err, http.StatusNotFound, "")
	_, err = e.s.rejectChange(ctx, other, v.ID)
	wantChgErr(t, "foreign reject", err, http.StatusNotFound, "")
	e.mustApply(t, v.ID)
	_, err = e.s.revertChange(ctx, other, v.ID)
	wantChgErr(t, "foreign revert", err, http.StatusNotFound, "")
	report, err := e.s.revertMessage(ctx, other, "c1", "m1")
	if err != nil || len(report.Reverted) != 0 {
		t.Fatalf("foreign revert of a message: %+v %v", report, err)
	}
	plain, err := e.s.createUser(userDraft{Login: "plain", Password: "secret1", Type: security.UserRegular, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := e.s.userByID(plain.ID)
	sess := security.Session{User: u.Login, UID: u.ID, PwFP: u.Fingerprint()}
	_, err = e.s.applyChange(ctx, sess, v.ID, aiApplyOpts{})
	wantChgErr(t, "plain user", err, http.StatusForbidden, "")
	if e.status(t, v.ID) != aiChangeDone {
		t.Fatal("state changed by a refused caller")
	}
}

// TestAssistantChangesReject checks a rejected proposal cannot be applied.
func TestAssistantChangesReject(t *testing.T) {
	e := newChgEnv(t)
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"No"}}`)
	r, err := e.s.rejectChange(context.Background(), e.root, v.ID)
	if err != nil || r.Status != aiChangeRejected {
		t.Fatalf("%+v %v", r, err)
	}
	_, err = e.apply(v.ID, aiApplyOpts{})
	wantChgErr(t, "apply rejected", err, http.StatusConflict, aiErrConflict)
	_, err = e.s.rejectChange(context.Background(), e.root, v.ID)
	wantChgErr(t, "reject twice", err, http.StatusConflict, aiErrConflict)
}

// TestAssistantChangesViewRedactsSecrets checks the card never shows a secret, while the revert
// brings the stored value back whole.
func TestAssistantChangesViewRedactsSecrets(t *testing.T) {
	e := newChgEnv(t)
	original := e.hashOf(t, "offers", "o1")
	v := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"url":"https://x.test/p/new?cid={subid}"}}`)
	d := e.mustPropose(t, "offers", aiOpDelete, `{"id":"o1"}`)
	body, _ := json.Marshal([]aiChangeView{v, d})
	for _, canary := range []string{"CANARYQ", "CANARYSIG", aiLongToken} {
		if strings.Contains(string(body), canary) {
			t.Fatalf("card leaks %q: %s", canary, body)
		}
	}
	e.mustApply(t, v.ID)
	e.mustRevert(t, v.ID)
	if e.hashOf(t, "offers", "o1") != original {
		t.Fatal("the secret URL was not restored whole")
	}
	raw, _, _ := e.s.aiChgStored(context.Background(), "offers", "o1")
	if !strings.Contains(string(raw), "CANARYQ") {
		t.Fatal("the restored record lost its token")
	}
}

// TestAssistantChangesStored checks the stored proposal names its author and that auto apply is off.
func TestAssistantChangesStored(t *testing.T) {
	e := newChgEnv(t)
	e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"One"}}`)
	recs, err := e.s.aiChgRead()
	if err != nil || len(recs) != 1 {
		t.Fatalf("%d %v", len(recs), err)
	}
	if recs[0].Proposal.UID != "env-admin" || recs[0].Proposal.ChatID != "c1" || recs[0].Proposal.MessageID != "m1" {
		t.Fatalf("proposal %+v", recs[0].Proposal)
	}
	if aiAutoApply(recs[0].Proposal) {
		t.Fatal("auto apply is on")
	}
}

// TestAssistantPruneChanges checks entries older than 90 days go away with their snapshots, and
// a snapshot that no entry names goes too.
func TestAssistantPruneChanges(t *testing.T) {
	e := newChgEnv(t)
	old := e.mustPropose(t, "landings", aiOpDelete, `{"id":"l9"}`)
	e.mustApply(t, old.ID)
	fresh := e.mustPropose(t, "offers", aiOpUpdate, `{"id":"o1","patch":{"name":"Fresh"}}`)
	stray := filepath.Join(e.dir, "assistant", "blobs", "stray.tar.gz")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.aiChgPatch(old.ID, func(r *aiChangeRec) error {
		r.Proposal.Created = time.Now().Add(-100 * 24 * time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	n, err := e.s.aiPruneChanges(time.Now())
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "assistant", "blobs", old.ID+".tar.gz")); !os.IsNotExist(err) {
		t.Fatal("snapshot of a removed entry stays")
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stray snapshot stays")
	}
	if e.status(t, fresh.ID) != aiChangeProposed {
		t.Fatal("a recent entry was removed")
	}
}
