package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ErrKeep is what the accept function of WriteFrom returns to leave the
// current file as it is: the new content is fine, it is just not needed.
var ErrKeep = errors.New("atomicfile: keep the current file")

// WriteFrom replaces the file at path with what fill writes, for content too
// large to hold in memory, such as a downloaded database.
//
// fill writes into a temporary file in the directory of path, which must
// exist; the file is synced and closed. accept then looks at the finished
// temporary file by its path, and may refuse it. Only after accept returns nil
// is the file given mode and renamed over path, so a reader finds the old
// version or the complete, accepted new one, never a partial download.
//
// path is the target and mode the permission of the published file. fill is
// required. accept may be nil, which accepts everything.
// It returns replaced true when the new file was put in place. A failure of
// fill or accept leaves path as it was and is returned unchanged, except
// ErrKeep, which gives (false, nil) with the current file untouched. The
// temporary file is removed in every case.
func WriteFrom(path string, mode os.FileMode, fill func(w io.Writer) error, accept func(tmp string) error) (replaced bool, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	err = fill(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if accept != nil {
		if err := accept(tmp); errors.Is(err, ErrKeep) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}
