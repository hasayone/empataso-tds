package logbuf

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// openTestSpool opens a spool of instance under root with room for everything
// unless the test narrows it through mutate.
func openTestSpool(t *testing.T, root, instance string, mutate func(*SpoolConfig)) *Spool {
	t.Helper()
	cfg := SpoolConfig{
		Root: root, Instance: instance, MaxBytes: 1 << 30, MinFreePct: 1,
		Free: func(string) (uint64, uint64, error) { return 900, 1000, nil },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := OpenSpool(cfg)
	if err != nil {
		t.Fatalf("OpenSpool: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func testQueue(t *testing.T, s *Spool) *SpoolQueue {
	t.Helper()
	q, err := s.Queue(EventsKind)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestSpoolPutLoadRemove(t *testing.T) {
	q := testQueue(t, openTestSpool(t, t.TempDir(), "engine", nil))
	if err := q.Put(SegmentMeta{Token: "tok-1", Count: 3, Sent: true}, []byte("hello payload")); err != nil {
		t.Fatal(err)
	}
	seg, ok := q.Oldest(nil)
	if !ok || seg.Token != "tok-1" || seg.Count != 3 || !seg.Sent {
		t.Fatalf("oldest = %+v ok=%v", seg, ok)
	}
	payload, err := q.Load(seg)
	if err != nil || string(payload) != "hello payload" {
		t.Fatalf("load = %q err=%v", payload, err)
	}
	if st := q.Stats(); st.Segments != 1 || st.Events != 3 || st.Bytes != seg.Size {
		t.Fatalf("stats %+v", st)
	}
	if err := q.Remove(seg); err != nil {
		t.Fatal(err)
	}
	if st := q.Stats(); st.Segments != 0 || st.Bytes != 0 || st.Events != 0 {
		t.Fatalf("stats after remove %+v", st)
	}
	if _, ok := q.Oldest(nil); ok {
		t.Fatal("queue should be empty")
	}
}

func TestSpoolOrderAndSkip(t *testing.T) {
	q := testQueue(t, openTestSpool(t, t.TempDir(), "engine", nil))
	for _, tok := range []string{"a", "b", "c"} {
		if err := q.Put(SegmentMeta{Token: tok, Count: 1}, []byte(tok)); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := q.Oldest(nil)
	second, _ := q.Oldest(func(s Segment) bool { return s.Name == first.Name })
	if first.Token != "a" || second.Token != "b" {
		t.Fatalf("order %q %q, want a then b", first.Token, second.Token)
	}
}

func TestSpoolModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	root := t.TempDir()
	q := testQueue(t, openTestSpool(t, root, "engine", nil))
	if err := q.Put(SegmentMeta{Token: "t", Count: 1}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	seg, _ := q.Oldest(nil)
	info, err := os.Stat(filepath.Join(root, "engine", EventsKind, seg.Name))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("segment mode %v err=%v, want 0600", info.Mode().Perm(), err)
	}
	for _, d := range []string{filepath.Join(root, "engine"), filepath.Join(root, "engine", EventsKind)} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode %v err=%v, want 0700", d, info.Mode().Perm(), err)
		}
	}
}

func TestSpoolReopenKeepsSegmentsInOrder(t *testing.T) {
	root := t.TempDir()
	s := openTestSpool(t, root, "engine", nil)
	q := testQueue(t, s)
	for _, tok := range []string{"a", "b"} {
		if err := q.Put(SegmentMeta{Token: tok, Count: 2, Sent: tok == "b"}, []byte(tok)); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	q2 := testQueue(t, openTestSpool(t, root, "engine", nil))
	st := q2.Stats()
	if st.Segments != 2 || st.Events != 4 {
		t.Fatalf("stats after reopen %+v", st)
	}
	first, _ := q2.Oldest(nil)
	second, _ := q2.Oldest(func(s Segment) bool { return s.Name == first.Name })
	if first.Token != "a" || second.Token != "b" || !second.Sent {
		t.Fatalf("after reopen: %+v %+v", first, second)
	}
}

func TestSpoolSecondProcessGetsError(t *testing.T) {
	if !lockSupported {
		t.Skip("no flock on this system")
	}
	root := t.TempDir()
	openTestSpool(t, root, "engine", nil)
	_, err := OpenSpool(SpoolConfig{Root: root, Instance: "engine"})
	if !errors.Is(err, ErrSpoolLocked) {
		t.Fatalf("second open err = %v, want ErrSpoolLocked", err)
	}
}

func TestSpoolAdoptsStoppedInstance(t *testing.T) {
	root := t.TempDir()
	old := openTestSpool(t, root, "engine2", nil)
	oq := testQueue(t, old)
	if err := oq.Put(SegmentMeta{Token: "orphan", Count: 5, Sent: true}, []byte("o")); err != nil {
		t.Fatal(err)
	}
	old.Close()

	q := testQueue(t, openTestSpool(t, root, "engine", nil))
	seg, ok := q.Oldest(nil)
	if !ok || seg.Token != "orphan" || !seg.Sent {
		t.Fatalf("adopted segment %+v ok=%v", seg, ok)
	}
	if st := q.Stats(); st.Events != 5 {
		t.Fatalf("stats %+v", st)
	}
	if _, err := os.Stat(filepath.Join(root, "engine2")); !os.IsNotExist(err) {
		t.Fatalf("the emptied instance directory is still there: %v", err)
	}
}

func TestSpoolLeavesLiveInstanceAlone(t *testing.T) {
	if !lockSupported {
		t.Skip("no flock on this system")
	}
	root := t.TempDir()
	live := openTestSpool(t, root, "engine2", nil)
	lq := testQueue(t, live)
	if err := lq.Put(SegmentMeta{Token: "live", Count: 1}, []byte("l")); err != nil {
		t.Fatal(err)
	}
	q := testQueue(t, openTestSpool(t, root, "engine", nil))
	if _, ok := q.Oldest(nil); ok {
		t.Fatal("a segment of a live instance was taken")
	}
	if st := lq.Stats(); st.Segments != 1 {
		t.Fatalf("live stats %+v", st)
	}
}

func TestSpoolSizeCeiling(t *testing.T) {
	root := t.TempDir()
	q := testQueue(t, openTestSpool(t, root, "engine", func(c *SpoolConfig) { c.MaxBytes = 400 }))
	payload := bytes.Repeat([]byte("abcdefghij"), 20)
	var full error
	for i := 0; i < 50 && full == nil; i++ {
		full = q.Put(SegmentMeta{Token: "t", Count: 1}, []byte(strings.Repeat(string(rune('a'+i%26)), 300)+string(payload)))
	}
	if !errors.Is(full, ErrSpoolFull) {
		t.Fatalf("err = %v, want ErrSpoolFull before 50 segments", full)
	}
	if st := q.Stats(); st.Bytes > 400 || st.Segments == 0 {
		t.Fatalf("stats %+v exceed the ceiling or hold nothing", st)
	}
}

func TestSpoolCeilingCountsOtherInstances(t *testing.T) {
	root := t.TempDir()
	other := openTestSpool(t, root, "engine2", nil)
	if err := testQueue(t, other).Put(SegmentMeta{Token: "t", Count: 1}, bytes.Repeat([]byte{7}, 300)); err != nil {
		t.Fatal(err)
	}
	size := testQueue(t, other).Stats().Bytes
	q := testQueue(t, openTestSpool(t, root, "engine", func(c *SpoolConfig) { c.MaxBytes = size + 5 }))
	if err := q.Put(SegmentMeta{Token: "t", Count: 1}, []byte("x")); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("err = %v, want ErrSpoolFull: the ceiling is shared by the root", err)
	}
}

func TestSpoolFreeSpaceFloor(t *testing.T) {
	free := uint64(500)
	q := testQueue(t, openTestSpool(t, t.TempDir(), "engine", func(c *SpoolConfig) {
		c.MinFreePct = 10
		c.Free = func(string) (uint64, uint64, error) { return free, 1000, nil }
	}))
	if err := q.Put(SegmentMeta{Token: "t", Count: 1}, []byte("x")); err != nil {
		t.Fatalf("with 50%% free: %v", err)
	}
	free = 100 // exactly the floor: the segment itself would break it
	if err := q.Put(SegmentMeta{Token: "t", Count: 1}, []byte("x")); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("err = %v, want ErrSpoolFull at the free-space floor", err)
	}
}

func TestSpoolFreeSpaceUnknownIsNotFull(t *testing.T) {
	q := testQueue(t, openTestSpool(t, t.TempDir(), "engine", func(c *SpoolConfig) {
		c.Free = func(string) (uint64, uint64, error) { return 0, 0, errors.New("no statfs") }
	}))
	if err := q.Put(SegmentMeta{Token: "t", Count: 1}, []byte("x")); err != nil {
		t.Fatalf("unreadable statistics must not refuse a segment: %v", err)
	}
}

// writeRaw puts a hand-made file into the queue directory of an instance.
func writeRaw(t *testing.T, root, instance, name string, data []byte) {
	t.Helper()
	dir := filepath.Join(root, instance, EventsKind)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSpoolRejectsUnknownVersionWithoutDeleting(t *testing.T) {
	root := t.TempDir()
	data, err := encodeSegment(SegmentMeta{Token: "t", Count: 1}, []byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	data[4] = 99
	writeRaw(t, root, "engine", "00000000000000000001-0001.seg", data)

	q := testQueue(t, openTestSpool(t, root, "engine", nil))
	if _, ok := q.Oldest(nil); ok {
		t.Fatal("a segment of an unknown version is queued")
	}
	if st := q.Stats(); st.Rejected != 1 || st.Segments != 0 {
		t.Fatalf("stats %+v, want one rejected", st)
	}
	kept, err := os.ReadFile(filepath.Join(root, "engine", EventsKind, rejectedDir, "00000000000000000001-0001.seg"))
	if err != nil || !bytes.Equal(kept, data) {
		t.Fatalf("rejected file was not kept intact: err=%v", err)
	}
}

func TestSpoolRejectsDamagedBodyOnLoad(t *testing.T) {
	root := t.TempDir()
	data, err := encodeSegment(SegmentMeta{Token: "t", Count: 1}, bytes.Repeat([]byte("payload "), 50))
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-10] ^= 0xff
	writeRaw(t, root, "engine", "00000000000000000002-0002.seg", data)

	q := testQueue(t, openTestSpool(t, root, "engine", nil))
	seg, ok := q.Oldest(nil)
	if !ok {
		t.Fatal("the header is fine, the segment must be queued")
	}
	if _, err := q.Load(seg); !errors.Is(err, ErrSegmentRejected) {
		t.Fatalf("load err = %v, want ErrSegmentRejected", err)
	}
	if st := q.Stats(); st.Rejected != 1 || st.Segments != 0 {
		t.Fatalf("stats %+v", st)
	}
	if _, err := os.Stat(filepath.Join(root, "engine", EventsKind, rejectedDir, seg.Name)); err != nil {
		t.Fatalf("damaged segment was not kept: %v", err)
	}
}

func TestSpoolRejectsGarbageFile(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "engine", "00000000000000000003-0003.seg", []byte("not a segment at all"))
	q := testQueue(t, openTestSpool(t, root, "engine", nil))
	if st := q.Stats(); st.Rejected != 1 || st.Segments != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestSpoolRejectedSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "engine", "00000000000000000004-0004.seg", []byte("garbage"))
	s := openTestSpool(t, root, "engine", nil)
	testQueue(t, s)
	s.Close()
	if st := testQueue(t, openTestSpool(t, root, "engine", nil)).Stats(); st.Rejected != 1 {
		t.Fatalf("rejected = %d after restart, want 1", st.Rejected)
	}
}

func TestSpoolRemovesStaleTempFiles(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "engine", ".00000000000000000005-0005.seg.tmp123", []byte("half a segment"))
	q := testQueue(t, openTestSpool(t, root, "engine", nil))
	if st := q.Stats(); st.Segments != 0 {
		t.Fatalf("a temp file was indexed: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(root, "engine", EventsKind, ".00000000000000000005-0005.seg.tmp123")); !os.IsNotExist(err) {
		t.Fatalf("temp file survived: %v", err)
	}
}

func TestSpoolOpenReportsUnwritableRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenSpool(SpoolConfig{Root: filepath.Join(file, "spool"), Instance: "engine"})
	if err == nil || !strings.Contains(err.Error(), "spool") {
		t.Fatalf("err = %v, want a spool error naming the directory", err)
	}
}

func TestSpoolRejectsBadInstance(t *testing.T) {
	if _, err := OpenSpool(SpoolConfig{Root: t.TempDir(), Instance: "../x"}); err == nil {
		t.Fatal("an instance with a path separator must be refused")
	}
}

func TestSpoolConfigFromEnv(t *testing.T) {
	t.Setenv("EMTDS_SPOOL_DIR", "/tmp/sp")
	t.Setenv("EMTDS_INSTANCE", "engine7")
	t.Setenv("EMTDS_SPOOL_MAX_BYTES", "12345")
	t.Setenv("EMTDS_SPOOL_MIN_FREE_PCT", "15")
	cfg, explicit, err := SpoolConfigFromEnv()
	if err != nil || !explicit || cfg.Root != "/tmp/sp" || cfg.Instance != "engine7" || cfg.MaxBytes != 12345 || cfg.MinFreePct != 15 {
		t.Fatalf("cfg=%+v explicit=%v err=%v", cfg, explicit, err)
	}

	t.Setenv("EMTDS_SPOOL_DIR", "")
	t.Setenv("EMTDS_SPOOL_MAX_BYTES", "lots")
	t.Setenv("EMTDS_SPOOL_MIN_FREE_PCT", "-3")
	cfg, explicit, err = SpoolConfigFromEnv()
	if explicit || cfg.Root != DefaultSpoolDir || cfg.MaxBytes != DefaultSpoolMaxBytes || cfg.MinFreePct != DefaultSpoolMinFreePct {
		t.Fatalf("defaults not applied: %+v explicit=%v", cfg, explicit)
	}
	if err == nil || !strings.Contains(err.Error(), "EMTDS_SPOOL_MAX_BYTES") || !strings.Contains(err.Error(), "EMTDS_SPOOL_MIN_FREE_PCT") {
		t.Fatalf("err = %v, want both bad values named", err)
	}
}

func TestSegmentRoundTripKeepsFlags(t *testing.T) {
	data, err := encodeSegment(SegmentMeta{Token: "abc", Count: 7, Sent: true}, []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	meta, payload, err := decodeSegment(data)
	if err != nil || meta.Token != "abc" || meta.Count != 7 || !meta.Sent || string(payload) != "body" {
		t.Fatalf("meta=%+v payload=%q err=%v", meta, payload, err)
	}
	if _, err := encodeSegment(SegmentMeta{Token: ""}, nil); err == nil {
		t.Fatal("an empty token must be refused")
	}
}

func TestSpoolMarkSentSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	s := openTestSpool(t, root, "engine", nil)
	q := testQueue(t, s)
	putSegment(t, q, "tok", false, Event{CID: "a"})
	seg, _ := q.Oldest(nil)
	if seg.Sent {
		t.Fatal("a new segment must not be marked sent")
	}
	if err := q.MarkSent(seg); err != nil {
		t.Fatal(err)
	}
	if now, _ := q.Oldest(nil); !now.Sent {
		t.Fatal("the index must show the flag")
	}
	if err := q.MarkSent(seg); err != nil {
		t.Fatalf("marking twice: %v", err)
	}
	if _, err := q.Load(seg); err != nil {
		t.Fatalf("a marked segment must still load: %v", err)
	}
	s.Close()

	q2 := testQueue(t, openTestSpool(t, root, "engine", nil))
	got, ok := q2.Oldest(nil)
	if !ok || !got.Sent || got.Token != "tok" || got.Count != 1 || got.Size != seg.Size {
		t.Fatalf("after restart %+v ok=%v, want the same segment with the flag", got, ok)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "engine", EventsKind))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && !e.IsDir() {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
}

func TestSpoolNoteSentChangesOnlyTheIndex(t *testing.T) {
	root := t.TempDir()
	s := openTestSpool(t, root, "engine", nil)
	q := testQueue(t, s)
	putSegment(t, q, "tok", false, Event{CID: "a"})
	seg, _ := q.Oldest(nil)
	q.NoteSent(seg.Name)
	if now, _ := q.Oldest(nil); !now.Sent {
		t.Fatal("the index must show the flag")
	}
	meta, err := readHeaderFile(filepath.Join(q.dir, seg.Name))
	if err != nil || meta.Sent {
		t.Fatalf("on disk %+v err=%v, want the flag untouched", meta, err)
	}
	q.NoteSent("no-such-segment.seg")
}

func TestSpoolMarkSentOfVanishedSegmentDropsIt(t *testing.T) {
	s := openTestSpool(t, t.TempDir(), "engine", nil)
	q := testQueue(t, s)
	putSegment(t, q, "tok", false, Event{CID: "a"})
	seg, _ := q.Oldest(nil)
	if err := os.Remove(filepath.Join(q.dir, seg.Name)); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkSent(seg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
	if q.Stats().Segments != 0 {
		t.Fatal("the vanished segment must leave the index")
	}
}

// failingReader returns an I/O error after its data, like a disk that fails.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("input/output error") }

func TestParseHeaderTellsAnIOErrorFromADamagedHeader(t *testing.T) {
	if _, err := parseHeader(failingReader{}); err == nil || errors.Is(err, errBadHeader) {
		t.Fatalf("err = %v, want an I/O error that is not a bad header", err)
	}
	for name, data := range map[string][]byte{
		"empty": nil, "short": []byte("EMS"), "magic": []byte("XXXXXXXXXXXXXXX"),
	} {
		if _, err := parseHeader(bytes.NewReader(data)); !errors.Is(err, errBadHeader) {
			t.Errorf("%s: err = %v, want a bad header", name, err)
		}
	}
}

func TestSpoolMinFreePctZeroMeansNoFloor(t *testing.T) {
	// 10% of the volume is free; a floor of 50% refuses a segment, none accepts it.
	low := func(string) (uint64, uint64, error) { return 10000, 100000, nil }
	put := func(pct int) error {
		s := openTestSpool(t, t.TempDir(), "engine", func(c *SpoolConfig) { c.MinFreePct = pct; c.Free = low })
		q := testQueue(t, s)
		payload, _ := encodeEvents([]Event{{CID: "a"}})
		return q.Put(SegmentMeta{Token: "t", Count: 1}, payload)
	}
	if err := put(50); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("a 50%% floor: err = %v, want ErrSpoolFull", err)
	}
	if err := put(0); err != nil {
		t.Fatalf("a 0%% floor must accept the segment: %v", err)
	}
}

func TestOpenSpoolRejectsAFloorOutOfRange(t *testing.T) {
	for _, pct := range []int{-1, 91} {
		if _, err := OpenSpool(SpoolConfig{Root: t.TempDir(), Instance: "engine", MinFreePct: pct}); err == nil {
			t.Errorf("floor %d must be refused", pct)
		}
	}
}

func TestSpoolConfigFromEnvZeroFloor(t *testing.T) {
	t.Setenv("EMTDS_SPOOL_MIN_FREE_PCT", "0")
	cfg, _, err := SpoolConfigFromEnv()
	if err != nil || cfg.MinFreePct != 0 {
		t.Fatalf("floor %d err=%v, want 0 kept", cfg.MinFreePct, err)
	}
	t.Setenv("EMTDS_SPOOL_MIN_FREE_PCT", "-3")
	if _, _, err := SpoolConfigFromEnv(); err == nil {
		t.Fatal("a negative floor must be reported")
	}
}

// A root that is not a mount point, or whose check fails, only produces a
// warning: the spool opens either way and keeps working.
func TestSpoolCheckMountOnlyWarns(t *testing.T) {
	root := t.TempDir()
	mount := func(ok bool, err error) func(string) (bool, error) {
		return func(dir string) (bool, error) {
			if dir != root {
				t.Errorf("checked %q, want the root %q", dir, root)
			}
			return ok, err
		}
	}
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	for i, tc := range []struct {
		isMount func(string) (bool, error)
		warn    bool
	}{
		{mount(false, nil), true},
		{mount(false, errors.New("stat failed")), true},
		{mount(true, nil), false},
	} {
		logged.Reset()
		s, err := OpenSpool(SpoolConfig{Root: root, Instance: "engine", CheckMount: true, IsMount: tc.isMount, Log: log})
		if err != nil {
			t.Fatalf("case %d: the mount check must never stop the open: %v", i, err)
		}
		s.Close()
		if got := strings.Contains(logged.String(), "level=WARN") && strings.Contains(logged.String(), root); got != tc.warn {
			t.Fatalf("case %d: warning logged = %v, want %v\n%s", i, got, tc.warn, logged.String())
		}
	}
	// Without CheckMount the check is not made.
	s2, err := OpenSpool(SpoolConfig{Root: root, Instance: "engine2", IsMount: func(string) (bool, error) {
		t.Error("IsMount called although CheckMount was off")
		return false, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestSpoolConfigFromEnvChecksMountOnlyWhenExplicit(t *testing.T) {
	t.Setenv("EMTDS_SPOOL_DIR", "/data/spool")
	if cfg, explicit, _ := SpoolConfigFromEnv(); !explicit || !cfg.CheckMount {
		t.Fatalf("explicit=%v CheckMount=%v, want both", explicit, cfg.CheckMount)
	}
	t.Setenv("EMTDS_SPOOL_DIR", "")
	if cfg, explicit, _ := SpoolConfigFromEnv(); explicit || cfg.CheckMount {
		t.Fatalf("explicit=%v CheckMount=%v, want neither", explicit, cfg.CheckMount)
	}
}

func TestMountInfoHas(t *testing.T) {
	const table = `22 28 0:21 / /proc rw,nosuid - proc proc rw
401 400 8:1 /var/lib/docker/volumes/x/_data /var/lib/emtds-spool rw,relatime - ext4 /dev/sda1 rw
402 400 8:1 /a /mnt/with\040space rw - ext4 /dev/sda1 rw
bad line
`
	for dir, want := range map[string]bool{
		"/var/lib/emtds-spool":     true,
		"/var/lib/emtds-spool/":    true,
		"/var/lib/emtds":           false,
		"/var/lib/emtds-spool/sub": false,
		"/mnt/with space":          true,
		"/proc":                    true,
	} {
		if got := mountInfoHas(strings.NewReader(table), dir); got != want {
			t.Errorf("mountInfoHas(%q) = %v, want %v", dir, got, want)
		}
	}
}

func TestIsMountPointOfAPlainDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("mount points are read on Linux")
	}
	if _, err := IsMountPoint("/"); err != nil {
		t.Fatalf("IsMountPoint(/) err=%v", err)
	}
	if _, err := IsMountPoint(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing directory must be an error")
	}
}
