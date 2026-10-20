package admin

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/kuztds/bridge/ktimport"
)

// cfStoredRow is a domain whose server-owned Cloudflare, check and watch fields are all set.
func cfStoredRow() config.Domain {
	return config.Domain{
		ID: "1", Host: "a.example", Role: "both", Group: "g",
		OK: false, Reason: "cf_pending", ReasonDetail: "x.ns.example,y.ns.example", Checked: "2026-10-20T10:00:00Z",
		CfZoneID: "zone1", CfStatus: "pending", CfNS: []string{"x.ns.example", "y.ns.example"},
		ConnectedAt: "2026-10-20T09:00:00Z", ConnectSeen: true, WatchSince: "2026-10-19T09:00:00Z",
	}
}

// sameServerFields reports whether got carries the server-owned fields of want.
func sameServerFields(got, want config.Domain) bool {
	return got.ReasonDetail == want.ReasonDetail && got.CfZoneID == want.CfZoneID && got.CfStatus == want.CfStatus &&
		reflect.DeepEqual(got.CfNS, want.CfNS) && got.ConnectedAt == want.ConnectedAt &&
		got.ConnectSeen == want.ConnectSeen && got.WatchSince == want.WatchSince
}

func TestDomainPutKeepsCloudflareFields(t *testing.T) {
	e := newCFEnv(t, nil)
	want := cfStoredRow()
	e.seed(t, want)
	code, raw := e.do(t, http.MethodPut, "/api/domains/1", map[string]any{
		"host": "a.example", "role": "both", "group": "moved",
		"cf_zone_id": "evil", "cf_status": "active", "cf_ns": []string{"h.example"}, "reason_detail": "h",
		"connected_at": "", "connect_seen": false, "watch_since": "", "ok": true,
	})
	if code != http.StatusOK {
		t.Fatalf("put %d %s", code, raw)
	}
	got := e.row(t, "1")
	if !sameServerFields(got, want) || got.Group != "moved" || got.OK {
		t.Fatalf("PUT changed the server-owned fields: %+v", got)
	}
	code, _ = e.do(t, http.MethodPut, "/api/domains/1", map[string]any{"host": "b.example", "role": "both"})
	if code != http.StatusOK {
		t.Fatalf("rename %d", code)
	}
	got = e.row(t, "1")
	if got.CfZoneID != "" || got.CfStatus != "" || got.CfNS != nil || got.ConnectedAt != "" || got.ConnectSeen || got.WatchSince != "" || got.ReasonDetail != "" {
		t.Fatalf("a renamed host kept the fields of the old one: %+v", got)
	}
}

func TestDomainPostIgnoresCloudflareFields(t *testing.T) {
	e := newCFEnv(t, nil)
	code, raw := e.do(t, http.MethodPost, "/api/domains", map[string]any{
		"id": "7", "host": "c.example", "role": "links",
		"cf_zone_id": "evil", "cf_status": "active", "cf_ns": []string{"h.example"}, "reason_detail": "h",
		"connected_at": "2026-01-01T00:00:00Z", "connect_seen": true, "watch_since": "2026-01-01T00:00:00Z",
	})
	if code != http.StatusCreated {
		t.Fatalf("post %d %s", code, raw)
	}
	got := e.row(t, "7")
	if got.CfZoneID != "" || got.CfStatus != "" || got.CfNS != nil || got.ConnectedAt != "" || got.ConnectSeen || got.WatchSince != "" || got.ReasonDetail != "" {
		t.Fatalf("the client set server-owned fields: %+v", got)
	}
}

func TestKeepLocalFieldsKeepsCloudflare(t *testing.T) {
	old := cfStoredRow()
	got := keepLocalFields(old, config.Domain{ID: "1", Host: "a.example", Role: "links"})
	if !sameServerFields(got, old) || got.Reason != old.Reason || got.Checked != old.Checked {
		t.Fatalf("import overwrote the server-owned fields: %+v", got)
	}
	got.CfNS[0] = "changed"
	if old.CfNS[0] == "changed" {
		t.Fatal("the kept name servers share memory with the stored row")
	}
}

func TestReimportKeepsCloudflareFields(t *testing.T) {
	ctx := context.Background()
	store := NewJSONStore[config.Domain](filepath.Join(t.TempDir(), "domains.json"))
	want := cfStoredRow()
	if err := store.Save(ctx, []config.Domain{want}); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{Domains: store}}
	res := ktimport.Result{Domains: []ktimport.CatDomain{{ID: "1", Host: "a.example", Role: "links"}}}
	res.Manifest.Add(ktimport.KindDomains, "1")
	prev := ktimport.Manifest{}
	prev.Add(ktimport.KindDomains, "1")
	st := &importState{counts: map[string]int{}, losses: []ktimport.Loss{}, prev: prev, res: res}
	if err := s.importCatalogs(ctx, keitaroImportReq{Domains: true}, st); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	if list[0].Role != "links" || !sameServerFields(list[0], want) || list[0].Reason != want.Reason || list[0].Checked != want.Checked {
		t.Fatalf("reimport lost the server-owned fields: %+v", list[0])
	}
}

func TestDomainImportWaitsForDomainLock(t *testing.T) {
	ctx := context.Background()
	store := NewJSONStore[config.Domain](filepath.Join(t.TempDir(), "domains.json"))
	s := &Server{cfg: Config{Domains: store}}
	res := ktimport.Result{Domains: []ktimport.CatDomain{{ID: "1", Host: "a.example", Role: "links"}}}
	st := &importState{counts: map[string]int{}, losses: []ktimport.Loss{}, res: res}
	s.domainMu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.importCatalogs(ctx, keitaroImportReq{Domains: true}, st) }()
	select {
	case <-done:
		t.Fatal("the import wrote domains.json while domainMu was held")
	case <-time.After(150 * time.Millisecond):
	}
	s.domainMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the import did not finish after domainMu was released")
	}
}
