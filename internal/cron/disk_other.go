//go:build !unix

package cron

import "errors"

// DiskFree reports that disk statistics are not available on this platform.
// The path is ignored; the error is always returned.
func DiskFree(string) (free, total uint64, err error) {
	return 0, 0, errors.New("disk monitoring is not supported on this platform")
}
