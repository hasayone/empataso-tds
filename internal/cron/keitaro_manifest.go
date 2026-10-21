package cron

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kuztds/bridge/ktimport"
)

// keitaroManifestPath returns keitaro-import.json next to the groups file,
// matching cmd/admin applyDataFiles. An empty groups path yields "".
func keitaroManifestPath(groupsFile string) string {
	groupsFile = strings.TrimSpace(groupsFile)
	if groupsFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(groupsFile), "keitaro-import.json")
}

// keitaroExcludeHost returns the tracker host from the import manifest, or ""
// when the file is missing or the host is blank. A missing file is silent; a
// read error or invalid JSON is logged once per manifest modification time.
func (r *Runner) keitaroExcludeHost() string {
	path := keitaroManifestPath(r.paths.Groups)
	if path == "" {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		r.warnKeitaroManifest(path, time.Time{}, err)
		return ""
	}
	mod := fi.ModTime()
	b, err := os.ReadFile(path)
	if err != nil {
		r.warnKeitaroManifest(path, mod, err)
		return ""
	}
	var m ktimport.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		if isJSONCorrupt(err) {
			r.warnKeitaroManifest(path, mod, err)
		}
		return ""
	}
	host := strings.TrimSpace(m.Host)
	r.mu.Lock()
	r.manifestMod = mod
	r.manifestHost = host
	r.mu.Unlock()
	return host
}

func (r *Runner) warnKeitaroManifest(path string, mod time.Time, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !mod.IsZero() && r.manifestWarnMod.Equal(mod) {
		return
	}
	if !mod.IsZero() {
		r.manifestWarnMod = mod
	}
	r.log.Warn("cron: keitaro import manifest unreadable, conversion filter off", "path", path, "err", err)
}

func isJSONCorrupt(err error) bool {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syn) || errors.As(err, &typ) || errors.Is(err, io.ErrUnexpectedEOF)
}
