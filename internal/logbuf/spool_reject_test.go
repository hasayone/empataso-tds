package logbuf

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Reject sets a pending segment aside: it leaves the queue, the file moves to
// rejected, the counter grows and the bytes stay on disk for inspection.
func TestSpoolRejectSetsSegmentAside(t *testing.T) {
	root := t.TempDir()
	s := openTestSpool(t, root, "engine", nil)
	q, err := s.Queue("postbacks")
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"a", "b"} {
		if err := q.Put(SegmentMeta{Token: tok, Count: 1}, []byte(tok)); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := q.Oldest(nil)
	q.Reject(first, errors.New("payload is not JSON"))

	if st := q.Stats(); st.Segments != 1 || st.Rejected != 1 {
		t.Fatalf("stats %+v, want one pending and one rejected", st)
	}
	if next, ok := q.Oldest(nil); !ok || next.Token != "b" {
		t.Fatalf("next = %+v ok=%v, want b", next, ok)
	}
	if _, err := os.Stat(filepath.Join(root, "engine", "postbacks", rejectedDir, first.Name)); err != nil {
		t.Fatalf("the rejected segment is not kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "engine", "postbacks", first.Name)); !os.IsNotExist(err) {
		t.Fatalf("the segment is still in the queue directory: %v", err)
	}
}
