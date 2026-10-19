package config

import (
	"path"
	"strings"
)

// SafeLandingRel cleans a path inside one landing and rejects escapes and hidden
// segments. rel is a slash-separated path as it appears in a URL or in the catalog.
// The result is the cleaned relative path. ok is false for an empty path, one with a
// backslash or a NUL byte, "..", an absolute path, or a segment starting with '.'.
func SafeLandingRel(rel string) (clean string, ok bool) {
	if rel == "" || strings.Contains(rel, "\\") || strings.ContainsRune(rel, 0) {
		return "", false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "/") || strings.Contains(cleaned, "..") {
		return "", false
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	return cleaned, true
}

// LandingEntry names the file the engine serves as the page of a local landing.
// entry is the catalog Entry; it wins, after trimming and SafeLandingRel, even when
// the file is missing. Otherwise index.php is used when it exists, and index.html after
// that. exists reports whether a regular file with the cleaned relative path is there.
// ok is false when nothing can be named. The engine and the admin API both call it, so
// both agree on the file whose macros are expanded.
func LandingEntry(entry string, exists func(rel string) bool) (name string, ok bool) {
	if clean, good := SafeLandingRel(strings.TrimSpace(entry)); good {
		return clean, true
	}
	for _, def := range []string{"index.php", "index.html"} {
		if exists(def) {
			return def, true
		}
	}
	return "", false
}

// SafeLandingID reports whether id can be the directory name of a landing: 1 to 128
// characters from letters, digits, '.', '_' and '-', not starting with '.'. A leading
// dot is refused so a landing never collides with the hidden staging and backup
// directories of an archive upload. The engine and the admin API both call it.
func SafeLandingID(id string) bool {
	if id == "" || len(id) > 128 || strings.HasPrefix(id, ".") {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
		default:
			return false
		}
	}
	return true
}
