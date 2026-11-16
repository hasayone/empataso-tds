package logbuf

import "testing"

// TestSpoolListOldestFirstWithTokens checks that List returns every pending
// segment, oldest first, with its token, and that changing the copy does not
// change the queue.
func TestSpoolListOldestFirstWithTokens(t *testing.T) {
	q := testQueue(t, openTestSpool(t, t.TempDir(), "engine", nil))
	for _, tok := range []string{"a", "b", "c"} {
		if err := q.Put(SegmentMeta{Token: tok, Count: 1}, []byte(tok)); err != nil {
			t.Fatal(err)
		}
	}
	list := q.List()
	if len(list) != 3 || list[0].Token != "a" || list[1].Token != "b" || list[2].Token != "c" {
		t.Fatalf("list %+v, want a b c", list)
	}
	list[0].Token = "changed"
	if got := q.List()[0].Token; got != "a" {
		t.Fatalf("the queue followed a change of the copy: %q", got)
	}
}

// TestSpoolAnyPendingSeesOtherInstances checks that AnyPending finds a segment
// of the kind in this instance and in another instance's directory, and
// ignores other kinds and empty queues.
func TestSpoolAnyPendingSeesOtherInstances(t *testing.T) {
	root := t.TempDir()
	other := openTestSpool(t, root, "old", nil)
	mine := openTestSpool(t, root, "engine", nil)
	if mine.AnyPending(EventsKind, false) {
		t.Fatal("an empty spool reported pending segments")
	}
	oq, err := other.Queue(EventsKind)
	if err != nil {
		t.Fatal(err)
	}
	if err := oq.Put(SegmentMeta{Token: "x", Count: 1}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !mine.AnyPending(EventsKind, false) {
		t.Fatal("a segment of another instance was not seen")
	}
	if mine.AnyPending("postbacks", false) {
		t.Fatal("a segment of another kind was counted")
	}
	seg, _ := oq.Oldest(nil)
	if err := oq.Remove(seg); err != nil {
		t.Fatal(err)
	}
	if mine.AnyPending(EventsKind, false) {
		t.Fatal("a removed segment was still reported")
	}
}

// TestSpoolAnyPendingSkipsSentAndRejected checks that a segment the buffer holds
// on purpose (sent, with skipSent) and a segment set aside in rejected do not
// keep AnyPending true, while an unsent one still does.
func TestSpoolAnyPendingSkipsSentAndRejected(t *testing.T) {
	s := openTestSpool(t, t.TempDir(), "engine", nil)
	q := testQueue(t, s)
	if err := q.Put(SegmentMeta{Token: "held", Count: 1, Sent: true}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !s.AnyPending(EventsKind, false) {
		t.Fatal("without skipSent a sent segment is pending")
	}
	if s.AnyPending(EventsKind, true) {
		t.Fatal("a segment held for the lack of deduplication kept AnyPending true")
	}
	if err := q.Put(SegmentMeta{Token: "bad", Count: 1}, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if !s.AnyPending(EventsKind, true) {
		t.Fatal("an unsent segment is not pending")
	}
	var bad Segment
	for _, seg := range q.List() {
		if seg.Token == "bad" {
			bad = seg
		}
	}
	q.Reject(bad, nil)
	if s.AnyPending(EventsKind, true) || q.Stats().Rejected != 1 {
		t.Fatalf("pending %v rejected %d after the unsent one was set aside", s.AnyPending(EventsKind, true), q.Stats().Rejected)
	}
	if s.AnyPending(EventsKind, false) != true {
		t.Fatal("the sent segment is still on disk and counts without skipSent")
	}
}
