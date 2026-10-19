package main

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egerkuzma/kuztds/internal/config"
)

// The keyword and the sub ids are visitor input: the click log stores at most
// config.MaxValueBytes of each, cut at a rune boundary.
func TestVisitorInputIsLimitedInTheLog(t *testing.T) {
	h, ins, stop := curlEnv(t, uniqGroup("ip"), "0")
	wide := strings.Repeat("\u00e9", 300) // 600 bytes
	q := url.Values{"q": {wide}, "sub_id_1": {wide}, "sub_id_3": {strings.Repeat("z", 500)}}
	jar{}.visit(t, h, "/g1?"+q.Encode())
	stop()
	if len(ins.events) != 1 {
		t.Fatalf("events %d", len(ins.events))
	}
	e := ins.events[0]
	for name, v := range map[string]string{"keyword": e.Keyword, "sub_id_1": e.Subs[0], "sub_id_3": e.Subs[2]} {
		if v == "" || len(v) > config.MaxValueBytes || !utf8.ValidString(v) {
			t.Errorf("%s: %d bytes, valid=%v", name, len(v), utf8.ValidString(v))
		}
	}
	if len(e.Subs[2]) != config.MaxValueBytes {
		t.Errorf("an ASCII sub id keeps the full limit, got %d", len(e.Subs[2]))
	}
}
