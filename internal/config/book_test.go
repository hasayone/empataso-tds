package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBookByIDSnapshot checks that a map taken before Reload keeps its old
// content, and that a map taken after Reload shows the new records.
func TestBookByIDSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offers.json")
	if err := os.WriteFile(path, []byte(`[{"id":"a","name":"A"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	book, err := OpenBook(path, func(o Offer) string { return o.ID })
	if err != nil {
		t.Fatal(err)
	}
	old := book.ByID()
	if len(old) != 1 || old["a"].Name != "A" {
		t.Fatalf("first snapshot = %+v", old)
	}

	if err := os.WriteFile(path, []byte(`[{"id":"a","name":"A2"},{"id":"b","name":"B"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := book.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 || old["a"].Name != "A" {
		t.Fatalf("old snapshot changed after Reload: %+v", old)
	}
	fresh := book.ByID()
	if len(fresh) != 2 || fresh["a"].Name != "A2" || fresh["b"].Name != "B" {
		t.Fatalf("new snapshot = %+v", fresh)
	}
}

// TestBookByIDConcurrentReload runs Reload against readers that walk the map
// ByID returned. Under -race any write into a published map is reported, and
// a reader that sees records of two generations in one map fails the test.
func TestBookByIDConcurrentReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offers.json")
	write := func(gen int) {
		var sb strings.Builder
		sb.WriteString("[")
		for i := 0; i < 20; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"id":"o%d","name":"gen%d"}`, i, gen)
		}
		sb.WriteString("]")
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			t.Error(err)
			return
		}
		stamp := time.Unix(1_700_000_000+int64(gen), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Error(err)
		}
	}
	write(0)
	book, err := OpenBook(path, func(o Offer) string { return o.ID })
	if err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				snap := book.ByID()
				gen := ""
				for _, o := range snap {
					if gen == "" {
						gen = o.Name
					}
					if o.Name != gen {
						t.Errorf("one snapshot mixes %q and %q", gen, o.Name)
						return
					}
				}
				if len(snap) != 20 {
					t.Errorf("snapshot has %d records, want 20", len(snap))
					return
				}
			}
		}()
	}
	for gen := 1; gen <= 200; gen++ {
		write(gen)
		if err := book.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	if got := book.ByID()["o0"].Name; got != "gen200" {
		t.Fatalf("last snapshot = %q, want gen200", got)
	}
}

// TestBookByIDNil checks that a nil book and an empty book read as empty.
func TestBookByIDNil(t *testing.T) {
	var nilBook *Book[Offer]
	if got := nilBook.ByID(); len(got) != 0 {
		t.Fatalf("nil book map = %+v", got)
	}
	empty, err := OpenBook("", func(o Offer) string { return o.ID })
	if err != nil {
		t.Fatal(err)
	}
	if got := empty.ByID(); len(got) != 0 {
		t.Fatalf("empty book map = %+v", got)
	}
}
