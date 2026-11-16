package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// WriteDurable writes data to path so that the file survives a power cut as
// well as a killed process: a reader, now or after a reboot, finds either the
// previous file or the complete new one.
//
// It follows Write — a temporary file in the directory of path, then a rename —
// and adds the two flushes Write leaves out. The data is synced before the
// rename, otherwise the new name can reach the disk ahead of the bytes and a
// crash leaves a complete-looking empty file under it. The directory is synced
// after the rename, otherwise the rename itself can be lost and the segment the
// caller was told is safe is not there after a reboot.
//
// path is the target and its directory must exist. data is the whole content.
// mode is the permission of the published file. A filesystem that refuses to
// sync a directory with EINVAL, and Windows, which cannot open one, are not an
// error: there is nothing more to flush there.
//
// A failure before the rename is returned as it is and leaves nothing at path
// that was not there before; the temporary file is removed. A failure of the
// directory sync comes after the rename: the file is already published and
// complete, only its survival of a power cut is not proven. That error wraps
// ErrSyncDir, so a caller that can live with it tells it from the others.
func WriteDurable(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := syncDirFunc(dir); err != nil {
		return fmt.Errorf("%w: %w", ErrSyncDir, err)
	}
	return nil
}

// syncDirFunc is syncDir, replaceable by a test that needs the sync to fail.
var syncDirFunc = syncDir

// ErrSyncDir marks the error of WriteDurable that comes after the rename: the
// file is in place and whole, but the directory could not be flushed.
var ErrSyncDir = errors.New("atomicfile: the directory was not synced after the rename")

// syncDir flushes the directory entry changes of dir to disk.
// It returns nil where a directory cannot be synced at all (Windows, or a
// filesystem answering EINVAL) and the error of the open or the sync otherwise.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
