//go:build !(linux || darwin || freebsd)

package logbuf

import "errors"

// lockSupported is false here: lockDir cannot exclude a second process.
const lockSupported = false

// errFreeUnsupported is returned by freeSpace where the volume statistics are
// not read; the spool then enforces only its size ceiling.
var errFreeUnsupported = errors.New("spool: volume statistics are not available on this system")

// lockDir does not lock on this system. It returns a release that does nothing.
// The engine runs in Linux containers; this exists so the package compiles on a
// developer machine.
func lockDir(dir string) (release func(), err error) {
	return func() {}, nil
}

// freeSpace reports that the volume statistics are not available.
func freeSpace(dir string) (free, total uint64, err error) {
	return 0, 0, errFreeUnsupported
}

// IsMountPoint cannot tell on this system and reports true, so that a developer
// machine is not refused. The check matters in the Linux container.
func IsMountPoint(dir string) (bool, error) { return true, nil }