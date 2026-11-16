package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/egerkuzma/emtds/internal/config"
)

func TestFileGroupsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	fg := NewFileGroups(path)

	// no file → empty list, no error
	got, err := fg.List(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty list: got %v err %v", got, err)
	}

	in := []config.Group{{ID: "g1", Name: "demo", Status: true}}
	if err := fg.Save(context.Background(), in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = fg.List(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "g1" {
		t.Fatalf("round-trip: got %v err %v", got, err)
	}
	// .tmp must not remain
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file leaked")
	}
}

func TestFileGroupsBadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	os.WriteFile(path, []byte("{not json"), 0o644)
	if _, err := NewFileGroups(path).List(context.Background()); err == nil {
		t.Error("expected error on bad json")
	}
}

func TestFileListsValidationAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fl := NewFileLists(dir)

	if err := fl.Write("ip_test.dat", "1.2.3.4\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := fl.Read("ip_test.dat")
	if err != nil || c != "1.2.3.4\n" {
		t.Fatalf("read: %q err %v", c, err)
	}
	names, err := fl.Names()
	if err != nil || len(names) != 1 || names[0] != "ip_test.dat" {
		t.Fatalf("names: %v err %v", names, err)
	}

	// path traversal / invalid names are rejected
	for _, bad := range []string{"../etc/passwd", "no_ext", "..dat", "a/b.dat", "x.dat/../y.dat"} {
		if err := fl.Write(bad, "x"); err == nil {
			t.Errorf("Write(%q) must be rejected", bad)
		}
		if _, err := fl.Read(bad); err == nil {
			t.Errorf("Read(%q) must be rejected", bad)
		}
	}
	// nonexistent valid file → read error
	if _, err := fl.Read("missing.dat"); err == nil {
		t.Error("read missing file must error")
	}
}

func TestFileKeysValidation(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "g1"), 0o755)
	os.WriteFile(filepath.Join(dir, "g1", "2026-06-07.dat"), []byte("kw1\nkw2\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "g1", "2026-06-07-se.dat"), []byte("se1\n"), 0o644)
	fk := NewFileKeys(dir)

	read := func(group, date string, se bool) (string, int64, error) {
		rc, size, err := fk.Open(group, date, se)
		if err != nil {
			return "", 0, err
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		return string(b), size, err
	}
	if c, size, err := read("g1", "2026-06-07", false); err != nil || c != "kw1\nkw2\n" || size != 8 {
		t.Fatalf("read keys: %q size %d err %v", c, size, err)
	}
	if c, _, err := read("g1", "2026-06-07", true); err != nil || c != "se1\n" {
		t.Fatalf("read se keys: %q err %v", c, err)
	}
	for _, bad := range [][2]string{{"../etc", "2026-06-07"}, {"g1", "../../x"}, {"..", "2026-06-07"}, {"g1", ".."}, {"", "2026-06-07"}} {
		if _, _, err := fk.Open(bad[0], bad[1], false); !errors.Is(err, errBadKeysName) {
			t.Errorf("Open(%q, %q) err %v, want errBadKeysName", bad[0], bad[1], err)
		}
	}
	if _, _, err := fk.Open("g1", "2026-06-08", false); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing list err %v, want fs.ErrNotExist", err)
	}
}

func TestFileGroupsSaveListCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	fg := NewFileGroups(path)
	in := []config.Group{{
		ID: "g1", Name: "demo", Status: true,
		Streams: []config.Stream{{Name: "s", Status: true}},
	}}
	if err := fg.Save(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	got, err := fg.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("save/list = %+v", got)
	}
	got[0] = config.Group{ID: "changed"}
	got = append(got, config.Group{ID: "extra"})
	again, err := fg.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, in) {
		t.Fatalf("cache saw caller edits: %+v", again)
	}

	prev, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`[{"id":"from-disk","name":"rewritten-campaign-body","status":false,"streams":[{"name":"only","status":true}]}]`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	now, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if now.Size() == prev.Size() {
		t.Fatal("rewrite must change the file size")
	}
	got, err = fg.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "from-disk" || got[0].Status {
		t.Fatalf("rewrite not visible: %+v", got)
	}
}

func TestFileGroupsSaveCompact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groups.json")
	fg := NewFileGroups(path)
	in := []config.Group{{
		ID: "g", Name: "n", Status: false,
		Streams: []config.Stream{{Name: "s", Status: false}},
	}}
	if err := fg.Save(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disk, compact) {
		t.Fatalf("save = %s want %s", disk, compact)
	}
	if bytes.Contains(disk, []byte("\n")) {
		t.Fatalf("newline in compact json: %s", disk)
	}
	indented, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(disk) >= len(indented) {
		t.Fatalf("compact %d not smaller than indent %d", len(disk), len(indented))
	}
}
