package main

import (
	"strings"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
)

func TestS2SCatalogQueues(t *testing.T) {
	pb := config.OutboundPostback{
		ID: "pb-fixed", URL: "https://pb.example/hook?cid={subid}&st={status}",
		Method: "GET", Statuses: []string{"sale"},
	}
	book := loadBook(t, "s2s.json", []config.OutboundPostback{pb}, func(p config.OutboundPostback) string { return p.ID })
	g := config.NewGroups(&config.Group{ID: "g1", Status: true, PostbackIDs: []string{"pb-fixed"}})
	d := &engineDeps{groups: g, s2s: book, outbound: make(chan outboundJob, 4)}
	if !d.fireOutbound("g1", "abc", "", 1, "sale", [config.MaxSubID]string{}) {
		t.Fatal("catalog template was not queued")
	}
	if len(d.outbound) != 1 {
		t.Fatalf("queued %d, want 1", len(d.outbound))
	}
	job := <-d.outbound
	if !strings.Contains(job.URL, "cid=abc") || !strings.Contains(job.URL, "st=sale") {
		t.Fatalf("url=%s", job.URL)
	}
}

func TestS2SDedupLocalAndGlobal(t *testing.T) {
	local := config.OutboundPostback{
		URL: "https://pb.example/hook?cid={subid}", Method: "GET", Statuses: []string{"sale"},
	}
	global := local
	global.ID = "pb-1"
	global.Name = "shared"
	book := loadBook(t, "s2s.json", []config.OutboundPostback{global}, func(p config.OutboundPostback) string { return p.ID })
	g := config.NewGroups(&config.Group{
		ID: "g1", Status: true,
		Postbacks:   []config.OutboundPostback{local},
		PostbackIDs: []string{"pb-1"},
	})
	d := &engineDeps{groups: g, s2s: book, outbound: make(chan outboundJob, 4)}
	if !d.fireOutbound("g1", "abc", "", 1, "sale", [config.MaxSubID]string{}) {
		t.Fatal("shared template was not queued")
	}
	if len(d.outbound) != 1 {
		t.Fatalf("queued %d, want 1", len(d.outbound))
	}
}

func TestS2SMissingIDNotQueued(t *testing.T) {
	other := config.OutboundPostback{
		ID: "pb-other", URL: "https://pb.example/other", Method: "GET", Statuses: []string{"lead"},
	}
	book := loadBook(t, "s2s.json", []config.OutboundPostback{other}, func(p config.OutboundPostback) string { return p.ID })
	g := config.NewGroups(&config.Group{ID: "g1", Status: true, PostbackIDs: []string{"pb-missing"}})
	d := &engineDeps{log: discardLog(), groups: g, s2s: book, outbound: make(chan outboundJob, 2)}
	if d.fireOutbound("g1", "abc", "", 1, "sale", [config.MaxSubID]string{}) {
		t.Fatal("missing template reported success")
	}
	if len(d.outbound) != 0 {
		t.Fatalf("queued %d, want 0", len(d.outbound))
	}
}
