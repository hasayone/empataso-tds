package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/kuztds/bridge/ktimport"
)

// FileGroups is a GroupsStore backed by a JSON groups configuration file.
// Save writes compact JSON atomically (temporary file, then rename) and bumps
// a generation counter. List caches the last successful parse. The cache key
// is the file mtime, size, and that generation.
type FileGroups struct {
	path  string
	mu    sync.Mutex
	cache []config.Group
	// gen is the counter Save increments. stamp.gen is the value it had
	// when the cache was filled, so the key is file mtime, size and generation.
	gen   uint64
	stamp struct {
		mod  time.Time
		size int64
		gen  uint64
	}
}

// NewFileGroups creates a groups store backed by a JSON file.
func NewFileGroups(path string) *FileGroups { return &FileGroups{path: path} }

// List returns groups from the JSON file.
// The context is accepted for the GroupsStore signature and is not used.
// The result is a copy of the top-level slice. Nested slices such as Streams
// are shared with the cache: handlers do not mutate them in place; they
// replace the group as a whole.
// A missing file returns an empty slice and a nil error.
// It returns an error when the file cannot be read or the JSON is invalid.
func (f *FileGroups) List(context.Context) ([]config.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	info, err := os.Stat(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f.cache = nil
			return []config.Group{}, nil
		}
		return nil, err
	}
	if f.cache != nil && f.stamp.mod.Equal(info.ModTime()) && f.stamp.size == info.Size() && f.stamp.gen == f.gen {
		return append([]config.Group(nil), f.cache...), nil
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f.cache = nil
			return []config.Group{}, nil
		}
		return nil, err
	}
	var groups []config.Group
	if err := json.Unmarshal(b, &groups); err != nil {
		return nil, err
	}
	f.cache = groups
	f.stamp.mod = info.ModTime()
	f.stamp.size = info.Size()
	f.stamp.gen = f.gen
	return append([]config.Group(nil), f.cache...), nil
}

// Save writes groups to the JSON file as one compact JSON value.
// The context is accepted for the GroupsStore signature and is not used.
// groups is the full document to store.
// It returns nil after the temporary file is renamed into place.
// It returns an error when groups cannot be encoded or the disk write fails.
// After a successful rename it increments the generation and drops the cache.
func (f *FileGroups) Save(_ context.Context, groups []config.Group) error {
	b, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return err
	}
	f.cache = nil
	f.gen++
	return nil
}

// FileLists — a ListsStore backed by a directory of .dat files (IP lists, wap operators,
// signatures). Names are strictly validated — no path traversal allowed.
type FileLists struct {
	dir string
}

// NewFileLists creates a lists store in directory dir.
func NewFileLists(dir string) *FileLists { return &FileLists{dir: dir} }

var listNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+\.dat$`)

func (l *FileLists) safePath(name string) (string, error) {
	if !listNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", errors.New("invalid list name")
	}
	return filepath.Join(l.dir, name), nil
}

func (l *FileLists) Names() ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".dat") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (l *FileLists) Read(name string) (string, error) {
	p, err := l.safePath(name)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (l *FileLists) Write(name, content string) error {
	p, err := l.safePath(name)
	if err != nil {
		return err
	}
	return writeFileAtomic(p, []byte(content), 0o644)
}

// Delete removes one list file.
// name is the file name, not a path. It returns nil when the file is already gone.
// It returns an error when the name is invalid or the file cannot be removed.
func (l *FileLists) Delete(name string) error {
	p, err := l.safePath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// pageNameRe matches a page file name. The name ends in .html and otherwise
// contains only letters, digits, dot, underscore and hyphen.
var pageNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+\.html$`)

// errBadPageName is returned when a page name is not one file in the pages directory.
var errBadPageName = errors.New("invalid page name")

// FilePages is a PagesStore backed by a directory of .html files.
// dir is created on the first successful Write.
type FilePages struct {
	dir string
}

// NewFilePages creates a pages store in directory dir.
// dir may be missing until the first Write. It does not return an error.
func NewFilePages(dir string) *FilePages { return &FilePages{dir: dir} }

// safePath returns the file path for name inside the pages directory.
// name is a file name, not a path. The result is dir joined with name.
// It returns errBadPageName when name contains ".." or fails pageNameRe.
func (p *FilePages) safePath(name string) (string, error) {
	if !pageNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", errBadPageName
	}
	return filepath.Join(p.dir, name), nil
}

// Names lists HTML files in the pages directory.
// It returns name and size for each regular file whose name is safe.
// A missing directory is an empty slice and a nil error.
// It returns an error when the directory cannot be read.
func (p *FilePages) Names() ([]PageInfo, error) {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []PageInfo{}, nil
		}
		return nil, err
	}
	out := make([]PageInfo, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !pageNameRe.MatchString(name) || strings.Contains(name, "..") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, PageInfo{Name: name, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Read returns the body of one page file.
// name is a file name, not a path. The result is the file contents.
// A missing file wraps os.ErrNotExist. An invalid name is errBadPageName.
// Any other read error is returned as-is.
func (p *FilePages) Read(name string) (string, error) {
	path, err := p.safePath(name)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read page %s: %w", name, os.ErrNotExist)
		}
		return "", err
	}
	return string(b), nil
}

// Write replaces one page file and creates the directory on first use.
// name is a file name. content is the HTML or text written as-is.
// It returns nil after the file is renamed into place.
// An invalid name is errBadPageName. A directory or disk failure is returned.
func (p *FilePages) Write(name, content string) error {
	path, err := p.safePath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(content), 0o644)
}

// Delete removes one page file.
// name is a file name, not a path. It returns nil when the file is already gone.
// It returns errBadPageName when the name is invalid, or a disk error when the file cannot be removed.
func (p *FilePages) Delete(name string) error {
	path, err := p.safePath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// FileKeys reads collected keywords from keysDir/<group>/<date>[-se].dat.
type FileKeys struct{ dir string }

// NewFileKeys creates a keys reader in directory dir.
func NewFileKeys(dir string) *FileKeys { return &FileKeys{dir: dir} }

var safeSeg = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func (k *FileKeys) Read(group, date string, se bool) (string, error) {
	if !safeSeg.MatchString(group) || !safeSeg.MatchString(date) {
		return "", errors.New("invalid group/date")
	}
	name := date
	if se {
		name += "-se"
	}
	b, err := os.ReadFile(filepath.Join(k.dir, group, name+".dat"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ManifestStore loads and saves the Keitaro import manifest.
// Load returns the last saved manifest. Save replaces the file.
// A missing file is an empty manifest and a nil error.
// A disk or JSON failure is an error.
type ManifestStore interface {
	Load() (ktimport.Manifest, error)
	Save(ktimport.Manifest) error
}

// FileManifest is a ManifestStore backed by one JSON file.
// path is the file. The parent directory must already exist.
type FileManifest struct {
	path string
}

// NewFileManifest returns a manifest store at path.
// path is the JSON file, usually keitaro-import.json in the state directory.
// It does not create the file and does not return an error.
func NewFileManifest(path string) *FileManifest { return &FileManifest{path: path} }

// Load reads the manifest.
// It returns an empty manifest when the file is missing or blank.
// It returns an error when the file cannot be read or is not a manifest object.
func (f *FileManifest) Load() (ktimport.Manifest, error) {
	if f == nil || f.path == "" {
		return ktimport.Manifest{}, errors.New("manifest unavailable")
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ktimport.Manifest{}, nil
		}
		return ktimport.Manifest{}, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return ktimport.Manifest{}, nil
	}
	var m ktimport.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return ktimport.Manifest{}, err
	}
	return m, nil
}

// Save writes the manifest atomically.
// m is the full manifest, including an empty one after a purge.
// It returns nil after the file is renamed into place.
// It returns an error when the value cannot be encoded or the disk write fails.
func (f *FileManifest) Save(m ktimport.Manifest) error {
	if f == nil || f.path == "" {
		return errors.New("manifest unavailable")
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(f.path, b, 0o644)
}
