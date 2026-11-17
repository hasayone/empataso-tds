package admin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	// unzipPrefix starts the name of the staging directory of an archive upload:
	// ".unzip-<id>-<random digits>".
	unzipPrefix = ".unzip-"
	// unzipBackupSuffix ends the name of the previous landing directory while it is
	// swapped out: "<staging name>.old".
	unzipBackupSuffix = ".old"
)

// unzipOwner reads the name of a staging or backup directory left by Unzip.
// name is a directory name in the landings root. It returns the landing id, whether
// the name is a backup, and ok false for any other name.
func unzipOwner(name string) (id string, backup, ok bool) {
	rest, found := strings.CutPrefix(name, unzipPrefix)
	if !found {
		return "", false, false
	}
	if trimmed, isBackup := strings.CutSuffix(rest, unzipBackupSuffix); isBackup {
		rest, backup = trimmed, true
	}
	dash := strings.LastIndexByte(rest, '-')
	if dash <= 0 || dash == len(rest)-1 {
		return "", false, false
	}
	for _, c := range rest[dash+1:] {
		if c < '0' || c > '9' {
			return "", false, false
		}
	}
	id = rest[:dash]
	if !config.SafeLandingID(id) {
		return "", false, false
	}
	return id, backup, true
}

// RecoverUnzip cleans up after an interrupted archive upload. It is meant to run once
// at start, before requests arrive.
// maxAge is how old a leftover must be before it is deleted. A backup directory whose
// landing directory is missing is renamed back to the landing, whatever its age, since
// the swap never finished; a backup beside an existing landing and a staging directory
// older than maxAge are deleted. Other names are left alone.
// It returns how many directories were restored and deleted, and the first error
// met; a missing landings directory is not an error.
func (f *FileLandings) RecoverUnzip(maxAge time.Duration) (restored, removed int, err error) {
	entries, readErr := os.ReadDir(f.dir)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, readErr
	}
	keep := func(e error) {
		if err == nil {
			err = e
		}
	}
	for _, e := range entries {
		id, backup, ok := unzipOwner(e.Name())
		if !ok || !e.IsDir() {
			continue
		}
		full := filepath.Join(f.dir, e.Name())
		if backup {
			root := filepath.Join(f.dir, id)
			if _, statErr := os.Lstat(root); errors.Is(statErr, os.ErrNotExist) {
				if renameErr := os.Rename(full, root); renameErr != nil {
					keep(renameErr)
					continue
				}
				restored++
				continue
			}
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			keep(infoErr)
			continue
		}
		if time.Since(info.ModTime()) < maxAge {
			continue
		}
		if rmErr := os.RemoveAll(full); rmErr != nil {
			keep(rmErr)
			continue
		}
		removed++
	}
	return restored, removed, err
}
