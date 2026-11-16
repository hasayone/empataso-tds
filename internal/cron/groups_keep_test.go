package cron

import (
	"os"
	"strings"
	"testing"
)

// TestDisableStreamsKeepsUnknownFields checks that a stream switched off does
// not drop campaign or stream fields the cron program does not know.
func TestDisableStreamsKeepsUnknownFields(t *testing.T) {
	e := newEnv(t)
	raw := []byte(`[{"id":"promo","future_note":"keep-me","uniq_by":"ip_ua","uniq_cookie":true,"streams":[{"name":"a","status":true,"vendor":"keep-stream","out":{"redirect":"http_redirect","out":"https://bad.example/x"}},{"name":"b","status":true,"out":{"redirect":"http_redirect","out":"https://good.example/y"}}]}]`)
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
	flat := strings.ReplaceAll(string(b), " ", "")
	for _, want := range []string{`"future_note":"keep-me"`, `"uniq_by":"ip_ua"`, `"uniq_cookie":true`, `"vendor":"keep-stream"`} {
		if !strings.Contains(flat, want) {
			t.Fatalf("rewritten groups lost %s: %s", want, b)
		}
	}
	g := e.readGroups()
	if len(g) != 1 || g[0].UniqBy != "ip_ua" || !g[0].UniqCookie || len(g[0].Streams) != 2 {
		t.Fatalf("groups after rewrite = %+v", g)
	}
	if g[0].Streams[0].Status || !g[0].Streams[1].Status {
		t.Fatalf("stream status = %v %v", g[0].Streams[0].Status, g[0].Streams[1].Status)
	}
}
