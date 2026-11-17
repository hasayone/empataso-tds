//go:build unix

package cron

import "syscall"

// DiskFree returns the bytes available to an unprivileged process and the
// size of the filesystem holding path. The error is the Statfs error, for example
// a path that does not exist.
func DiskFree(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}
