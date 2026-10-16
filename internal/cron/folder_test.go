package cron

import (
	"os"
	"strings"
	"testing"
)

// TestDisableStreamsKeepsFolder checks that turning a stream off rewrites
// groups.json without dropping the campaign folder label.
func TestDisableStreamsKeepsFolder(t *testing.T) {
	e := newEnv(t)
	raw := []byte(`[{"id":"promo","folder":"Leads","streams":[{"name":"a","status":true,"out":{"redirect":"http_redirect","out":"https://bad.example/x"}}]}]`)
	if err := os.WriteFile(e.r.paths.Groups, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	off, err := e.r.disableStreams(map[string]int{"bad.example": 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(off) != 1 || off[0] != "promo/a" {
		t.Fatalf("switched off %v; want promo/a", off)
	}
	b, err := os.ReadFile(e.r.paths.Groups)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(string(b), " ", ""), `"folder":"Leads"`) {
		t.Fatalf("rewritten groups lost folder: %s", b)
	}
	g := e.readGroups()
	if len(g) != 1 || g[0].Folder != "Leads" || g[0].Streams[0].Status {
		t.Fatalf("groups after rewrite = %+v", g)
	}
}
