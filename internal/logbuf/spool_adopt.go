package logbuf

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
)

// adoptForeign moves the segments of every other instance directory under the
// root whose lock nobody holds into this instance's queues, then removes the
// emptied directory. A directory held by a live process is left alone. Failures
// are logged and the directory is tried again at the next start.
//
// There is a window between taking the lock of a directory and its owner
// starting: an engine that starts at the same moment may find its own directory
// locked by this adoption and fail with ErrSpoolLocked, and a supervisor that
// restarts it gets the directory back on the next try. A directory is adopted
// only while its lock is held here, so two adopters never move the same file.
func (s *Spool) adoptForeign() {
	entries, err := os.ReadDir(s.cfg.Root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == s.cfg.Instance || !config.ValidInstance(e.Name()) {
			continue
		}
		dir := filepath.Join(s.cfg.Root, e.Name())
		release, err := lockDir(dir)
		if err != nil {
			continue
		}
		moved, failed := s.adoptDir(dir, e.Name())
		if moved > 0 || failed > 0 {
			s.log.Info("spool: adopted segments of a stopped instance", "instance", e.Name(), "segments", moved, "failed", failed)
		}
		release()
	}
}

// adoptDir renames the segments under one foreign instance directory into the
// matching kind directories of this instance, keeping their names and with them
// their order. Files from its rejected directories keep the old instance as a
// prefix. Empty directories are removed. It returns how many segments moved and
// how many could not.
func (s *Spool) adoptDir(dir, instance string) (moved, failed int) {
	kinds, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, k := range kinds {
		if !k.IsDir() || !config.ValidInstance(k.Name()) {
			continue
		}
		src := filepath.Join(dir, k.Name())
		dst := filepath.Join(s.dir, k.Name())
		if err := os.MkdirAll(filepath.Join(dst, rejectedDir), spoolDirMode); err != nil {
			failed++
			continue
		}
		m, f := moveSegments(src, dst, "")
		moved, failed = moved+m, failed+f
		_, f = moveSegments(filepath.Join(src, rejectedDir), filepath.Join(dst, rejectedDir), instance+"-")
		failed += f
		removeTemp(src)
		_ = os.Remove(filepath.Join(src, rejectedDir))
		_ = os.Remove(src)
	}
	_ = os.Remove(dir)
	return moved, failed
}

// moveSegments renames every .seg file of src into dst, putting prefix before
// the name. A name already present in dst is not overwritten; the file is left
// where it is and counted as failed. It returns the moved and failed counts.
func moveSegments(src, dst, prefix string) (moved, failed int) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), segmentExt) {
			continue
		}
		target := filepath.Join(dst, prefix+e.Name())
		if _, err := os.Lstat(target); err == nil {
			failed++
			continue
		}
		if err := os.Rename(filepath.Join(src, e.Name()), target); err != nil {
			failed++
			continue
		}
		moved++
	}
	return moved, failed
}

// removeTemp deletes the temporary files atomicfile leaves when a process dies
// between creating and renaming one: hidden files in dir and in its direct
// subdirectories. Errors are ignored.
func removeTemp(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case e.IsDir():
			if e.Name() != rejectedDir {
				removeTemp(p)
			}
		case strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), ".tmp"):
			_ = os.Remove(p)
		}
	}
}

// countSegments counts the .seg files under dir and their bytes, rejected ones
// included. It does not fail.
func countSegments(dir string) (n int, size int64) {
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), segmentExt) {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			n++
			size += info.Size()
		}
		return nil
	})
	return n, size
}
