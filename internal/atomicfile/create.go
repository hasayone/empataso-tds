package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// CreateIfAbsent publishes data at path only when no file is there yet.
//
// The data is written and synced into a private temporary file in the same
// directory, then linked to path with os.Link. A link never replaces an existing
// name, so of several processes racing to create the file exactly one wins, and
// the file a reader finds at path is always complete: there is no moment where
// it exists but is still empty, which O_EXCL followed by a write cannot promise.
//
// path is the target. data is its full content. mode is the permission of the
// published file; the temporary file is private until then.
// It returns created true when this call published the file and false when a
// file already existed, which it leaves untouched. Any other failure, including
// a filesystem without hard links, is returned and leaves nothing at path.
func CreateIfAbsent(path string, data []byte, mode os.FileMode) (created bool, err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return false, err
	}
	switch err := os.Link(tmp, path); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrExist):
		return false, nil
	default:
		return false, err
	}
}
