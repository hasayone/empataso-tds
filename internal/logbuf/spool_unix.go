//go:build linux || darwin || freebsd

package logbuf

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockSupported tells that lockDir really excludes a second process.
const lockSupported = true

// lockDir takes an exclusive, non-blocking flock on the directory dir.
// It returns release, which drops the lock and closes the descriptor, or
// ErrSpoolLocked when another open file description holds the lock (another
// process, or another Open in this one). Any other failure is returned as it is.
// The lock is released by the kernel when the process dies, so a crash never
// leaves a stale lock.
func lockDir(dir string) (release func(), err error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrSpoolLocked
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// freeSpace returns the bytes available to an unprivileged process and the
// total bytes of the volume that holds dir. It returns the statfs error.
func freeSpace(dir string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}

// IsMountPoint reports whether dir is a mount point: its device differs from
// the device of "/", or /proc/self/mountinfo lists it as a mount target. A
// volume or bind mount passes; a plain directory inside the container's own
// layer does not. It returns the error of stat when dir cannot be examined.
func IsMountPoint(dir string) (bool, error) {
	var d, root syscall.Stat_t
	if err := syscall.Stat(dir, &d); err != nil {
		return false, err
	}
	if err := syscall.Stat("/", &root); err != nil {
		return false, err
	}
	if d.Dev != root.Dev {
		return true, nil
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, nil
	}
	defer f.Close()
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	return mountInfoHas(f, abs), nil
}