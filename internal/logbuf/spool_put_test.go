package logbuf

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Writers racing for the last bytes of the ceiling cannot all get them: the
// space is booked before the write.
func TestSpoolConcurrentPutsStayUnderTheCeiling(t *testing.T) {
	meta := SegmentMeta{Token: "tok-00", Count: 1}
	one, err := encodeSegment(meta, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(one))
	q := testQueue(t, openTestSpool(t, t.TempDir(), "engine", func(c *SpoolConfig) { c.MaxBytes = 5*size + size/2 }))

	var wg sync.WaitGroup
	var ok, full atomic.Int32
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if q.Put(meta, []byte("payload")) == nil {
				ok.Add(1)
			} else {
				full.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 5 || full.Load() != 35 {
		t.Fatalf("%d segments accepted and %d refused, want 5 and 35", ok.Load(), full.Load())
	}
	if st := q.Stats(); st.Bytes != 5*size || st.Segments != 5 {
		t.Fatalf("stats %+v, want 5 segments of %d bytes", st, size)
	}
}

// A failed write gives its reserved bytes back.
func TestSpoolFailedPutReleasesItsReservation(t *testing.T) {
	meta := SegmentMeta{Token: "tok-00", Count: 1}
	one, err := encodeSegment(meta, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	s := openTestSpool(t, t.TempDir(), "engine", func(c *SpoolConfig) { c.MaxBytes = int64(len(one)) })
	q := testQueue(t, s)
	q.dir += "-missing"
	if err := q.Put(meta, []byte("payload")); err == nil {
		t.Fatal("a write into a missing directory must fail")
	}
	q.dir = q.dir[:len(q.dir)-len("-missing")]
	if err := q.Put(meta, []byte("payload")); err != nil {
		t.Fatalf("the reservation of the failed write was not released: %v", err)
	}
}

// The index stays oldest first by name whatever order the entries arrive in,
// as when two writers finish their files out of order.
func TestSpoolIndexIsOrderedByName(t *testing.T) {
	q := testQueue(t, openTestSpool(t, t.TempDir(), "engine", nil))
	for _, n := range []int{30, 10, 20, 40, 5} {
		name := fmt.Sprintf("%020d-0001.seg", n)
		q.add(segmentEntry{Segment: Segment{SegmentMeta: SegmentMeta{Token: name, Count: 1}, Name: name, Size: 10}})
	}
	var got []string
	skip := map[string]bool{}
	for {
		seg, ok := q.Oldest(func(s Segment) bool { return skip[s.Name] })
		if !ok {
			break
		}
		got = append(got, seg.Name)
		skip[seg.Name] = true
	}
	want := []string{
		"00000000000000000005-0001.seg", "00000000000000000010-0001.seg", "00000000000000000020-0001.seg",
		"00000000000000000030-0001.seg", "00000000000000000040-0001.seg",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if st := q.Stats(); st.Segments != 5 || st.Bytes != 50 || st.Events != 5 {
		t.Fatalf("stats %+v", st)
	}
}
