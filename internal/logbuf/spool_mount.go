package logbuf

import (
	"bufio"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// mountInfoHas reports whether the mountinfo table in r (the format of
// /proc/self/mountinfo) has a mount whose target is dir. dir is an absolute,
// cleaned path. A line it cannot parse is skipped.
func mountInfoHas(r io.Reader, dir string) bool {
	dir = filepath.Clean(dir)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		if filepath.Clean(unescapeMountField(fields[4])) == dir {
			return true
		}
	}
	return false
}

// unescapeMountField decodes the octal escapes (\040 for a space) the kernel
// writes into mountinfo paths.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
