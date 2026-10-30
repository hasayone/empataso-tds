package admin

import (
	"testing"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/kuztds/bridge/ktimport"
)

// A re-import replaces the campaign but keeps the local sticky switch.
func TestMergeImportedGroupsKeepsSticky(t *testing.T) {
	existing := []config.Group{{ID: "a", Name: "kt: A", Num: 3, StickyStream: true}}
	incoming := []ktimport.Group{{ID: "a", Name: "kt: A renamed"}}
	prev := ktimport.Manifest{}
	prev.Add(ktimport.KindGroups, "a")
	out, _, err := mergeImportedGroups(existing, incoming, prev, "", config.MainBoardID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Name != "kt: A renamed" || !out[0].StickyStream {
		t.Fatalf("reimport lost the sticky switch: %+v", out)
	}
}
