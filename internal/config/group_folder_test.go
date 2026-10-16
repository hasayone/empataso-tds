package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestGroupFolderJSONRoundTrip checks that a campaign folder label is stored
// under "folder", omitted when empty, and restored on read.
func TestGroupFolderJSONRoundTrip(t *testing.T) {
	with, err := json.Marshal(Group{Folder: "Leads"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(with), `"folder":"Leads"`) {
		t.Fatalf("marshal with folder = %s", with)
	}

	empty, err := json.Marshal(Group{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), `"folder"`) {
		t.Fatalf("empty folder leaked into JSON: %s", empty)
	}

	var g Group
	if err := json.Unmarshal([]byte(`{"id":"a","folder":"X"}`), &g); err != nil {
		t.Fatal(err)
	}
	if g.ID != "a" || g.Folder != "X" {
		t.Fatalf("unmarshaled group = %+v", g)
	}
}
