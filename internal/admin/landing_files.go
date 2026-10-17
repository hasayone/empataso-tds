package admin

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

const landingTextLimit = 2 << 20

// landingIDRe matches one landing directory name.
// Dot and dot-dot also match and are rejected separately.
var landingIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var landingTextExt = map[string]bool{
	".html": true,
	".htm":  true,
	".php":  true,
	".css":  true,
	".js":   true,
	".txt":  true,
	".json": true,
	".xml":  true,
	".svg":  true,
}

var (
	errBadLandingID   = errors.New("invalid landing id")
	errBadLandingPath = errors.New("invalid landing path")
	errLandingText    = errors.New("not a text file")
)

// FileInfo is one file inside a landing directory.
// Path is relative and uses forward slashes. Size is the length in bytes.
type FileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// LandingFiles stores the files of one local landing.
// id is the catalog id and the directory name. path is relative to that directory.
// List returns files. Read returns one body. Write replaces one file.
// Delete removes one file. Unzip replaces the directory from a zip archive.
// Remove deletes the directory. Entry names the file the engine should open.
// An invalid id or path is an error. A missing directory is an empty list.
type LandingFiles interface {
	List(id string) ([]FileInfo, error)
	Read(id, path string) ([]byte, error)
	Write(id, path string, data []byte) error
	Delete(id, path string) error
	Unzip(id string, zip []byte) (int, error)
	Remove(id string) error
	Entry(id string) string
}

// FileLandings is a LandingFiles store under one root directory.
// dir is created on the first write. Each landing is a subdirectory named by id.
type FileLandings struct {
	dir string
}

// NewFileLandings returns a landing file store rooted at dir.
// dir may be missing until the first Write or Unzip. It does not return an error.
func NewFileLandings(dir string) *FileLandings { return &FileLandings{dir: dir} }

// cleanLandingID checks a landing directory name.
// id is the catalog id. It returns the same id when it is one safe path segment.
// It returns errBadLandingID for an empty name, dot, dot-dot, or any other character.
func cleanLandingID(id string) (string, error) {
	if id == "." || id == ".." || !landingIDRe.MatchString(id) {
		return "", errBadLandingID
	}
	return id, nil
}

// cleanLandingPath checks a relative file path inside a landing.
// path may use slashes or backslashes. It returns the cleaned path with forward slashes.
// It returns errBadLandingPath when the path is absolute, empty, hidden, or escapes the directory.
func cleanLandingPath(path string) (string, error) {
	path = strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") || strings.Contains(path, ":") {
		return "", errBadLandingPath
	}
	var parts []string
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.HasPrefix(part, ".") {
			return "", errBadLandingPath
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", errBadLandingPath
	}
	return strings.Join(parts, "/"), nil
}

// landingTextPath checks a path that an editor may read or write.
// path is a relative landing path. It returns the cleaned path.
// It returns errBadLandingPath or errLandingText when the extension is not text.
func landingTextPath(path string) (string, error) {
	clean, err := cleanLandingPath(path)
	if err != nil {
		return "", err
	}
	if !landingTextExt[strings.ToLower(filepath.Ext(clean))] {
		return "", errLandingText
	}
	return clean, nil
}

// root returns the absolute directory of one landing.
// id is the catalog id. It returns the directory path.
// It returns errBadLandingID when id is not a safe segment.
func (f *FileLandings) root(id string) (string, error) {
	id, err := cleanLandingID(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(f.dir, id), nil
}

// abs returns the file path for one relative landing path.
// id is the catalog id. rel is already cleaned. It returns the absolute path.
// It returns an error when the path escapes the landing directory.
func (f *FileLandings) abs(id, rel string) (string, error) {
	root, err := f.root(id)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	back, err := filepath.Rel(root, full)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", errBadLandingPath
	}
	return full, nil
}

// List returns the files of one landing.
// id is the catalog id. Paths are relative and sorted. A missing directory is an empty slice.
// It returns errBadLandingID for a bad id, or a disk error when the tree cannot be read.
func (f *FileLandings) List(id string) ([]FileInfo, error) {
	root, err := f.root(id)
	if err != nil {
		return nil, err
	}
	var out []FileInfo
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, err := cleanLandingPath(rel); err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, FileInfo{Path: rel, Size: info.Size()})
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []FileInfo{}, nil
		}
		return nil, err
	}
	if out == nil {
		out = []FileInfo{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Read returns the body of one landing file.
// id is the catalog id. path is relative. It returns the file bytes.
// A bad id or path is an error. A missing file wraps os.ErrNotExist.
func (f *FileLandings) Read(id, path string) ([]byte, error) {
	rel, err := cleanLandingPath(path)
	if err != nil {
		return nil, err
	}
	full, err := f.abs(id, rel)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return b, nil
}

// Write replaces one landing file and creates missing directories.
// id is the catalog id. path is relative. data is written as-is.
// It returns nil after the file is renamed into place.
// A bad id or path is an error. A disk failure is returned.
func (f *FileLandings) Write(id, path string, data []byte) error {
	rel, err := cleanLandingPath(path)
	if err != nil {
		return err
	}
	full, err := f.abs(id, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(full, data, 0o644)
}

// Delete removes one landing file.
// id is the catalog id. path is relative. It returns nil when the file is already gone.
// A bad id or path is an error. A disk failure is returned.
func (f *FileLandings) Delete(id, path string) error {
	rel, err := cleanLandingPath(path)
	if err != nil {
		return err
	}
	full, err := f.abs(id, rel)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Remove deletes the landing directory.
// id is the catalog id. It returns nil when the directory is already gone.
// It returns errBadLandingID for a bad id, or a disk error when the tree cannot be removed.
func (f *FileLandings) Remove(id string) error {
	root, err := f.root(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Entry names the file the engine should open for this landing.
// id is the catalog id. It returns index.php, otherwise index.html,
// otherwise the first root file ending in .php or .html, otherwise an empty string.
// A bad id or a missing directory returns an empty string. It does not return an error.
func (f *FileLandings) Entry(id string) string {
	infos, err := f.List(id)
	if err != nil {
		return ""
	}
	indexPHP, indexHTML, first := "", "", ""
	for _, info := range infos {
		if strings.Contains(info.Path, "/") {
			continue
		}
		low := strings.ToLower(info.Path)
		switch low {
		case "index.php":
			indexPHP = info.Path
		case "index.html":
			if indexHTML == "" {
				indexHTML = info.Path
			}
		}
		if first == "" && (strings.HasSuffix(low, ".php") || strings.HasSuffix(low, ".html")) {
			first = info.Path
		}
	}
	if indexPHP != "" {
		return indexPHP
	}
	if indexHTML != "" {
		return indexHTML
	}
	return first
}

// removeLandingFiles deletes the directory of a landing removed from the catalog.
// id is the catalog id. A nil store does nothing.
// It does not return an error. A disk failure is logged.
func (s *Server) removeLandingFiles(id string) {
	if s.cfg.LandingFiles == nil {
		return
	}
	if err := s.cfg.LandingFiles.Remove(id); err != nil && s.cfg.Log != nil {
		s.cfg.Log.Warn("landing files", "id", id, "err", err.Error())
	}
}

// registerLandingFiles mounts the landing file routes behind auth.
// A nil store answers 503. Paths that escape the landing directory are 400.
func registerLandingFiles(s *Server, mux *http.ServeMux) {
	s.handle(mux, "GET /api/landings/{id}/files", s.landingAllowed(false, s.handleLandingFiles))
	s.handle(mux, "POST /api/landings/{id}/archive", s.landingAllowed(true, s.handleLandingArchive))
	s.handle(mux, "GET /api/landings/{id}/files/{path...}", s.landingAllowed(false, s.handleLandingFileGet))
	s.handle(mux, "PUT /api/landings/{id}/files/{path...}", s.landingAllowed(true, s.handleLandingFilePut))
	s.handle(mux, "DELETE /api/landings/{id}/files/{path...}", s.landingAllowed(true, s.handleLandingFileDelete))
}

// landingAllowed checks landing catalog grants before file handlers run.
// write selects POST archive, PUT and DELETE. A missing landings store yields 404.
func (s *Server) landingAllowed(write bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := s.viewerFrom(r.Context())
		if v.admin {
			next(w, r)
			return
		}
		if s.cfg.Landings == nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		id := r.PathValue("id")
		list, err := s.cfg.Landings.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read error")
			return
		}
		var found config.Landing
		ok := false
		for _, l := range list {
			if l.ID == id {
				found = l
				ok = true
				break
			}
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		lref := ref{id: found.ID, label: found.Group, owner: found.CreatedBy}
		if !v.canSee(resLandings, lref) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if write && !v.canWrite(resLandings, lref) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	})
}

// landingFilesStore returns the file store or writes 503.
// It returns nil when the handler should stop.
func (s *Server) landingFilesStore(w http.ResponseWriter) LandingFiles {
	if s.cfg.LandingFiles == nil {
		writeErr(w, http.StatusServiceUnavailable, "landing files unavailable")
		return nil
	}
	return s.cfg.LandingFiles
}

// writeLandingErr maps a landing file error onto an HTTP status.
// err is the store failure. It does not return a value.
func writeLandingErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBadLandingID):
		writeErr(w, http.StatusBadRequest, "bad id")
	case errors.Is(err, errBadLandingPath):
		writeErr(w, http.StatusBadRequest, "bad path")
	case errors.Is(err, errLandingText):
		writeErr(w, http.StatusBadRequest, "not a text file")
	case errors.Is(err, os.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, errLandingArchive):
		writeErr(w, http.StatusBadRequest, "bad archive")
	default:
		writeErr(w, http.StatusInternalServerError, "save error")
	}
}

// handleLandingFiles lists the files of one landing.
// id is the URL segment. A nil store is 503. A bad id is 400.
// Success is a JSON array. It does not return an error value.
func (s *Server) handleLandingFiles(w http.ResponseWriter, r *http.Request) {
	files := s.landingFilesStore(w)
	if files == nil {
		return
	}
	infos, err := files.List(r.PathValue("id"))
	if err != nil {
		writeLandingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, infos)
}

// handleLandingFileGet returns one text file body.
// id and path are URL segments. Only text extensions are served, and only up to 2 MiB.
// A missing file is 404. A file over the limit is 413. Success is the raw body.
func (s *Server) handleLandingFileGet(w http.ResponseWriter, r *http.Request) {
	files := s.landingFilesStore(w)
	if files == nil {
		return
	}
	rel, err := landingTextPath(r.PathValue("path"))
	if err != nil {
		writeLandingErr(w, err)
		return
	}
	data, err := files.Read(r.PathValue("id"), rel)
	if err != nil {
		writeLandingErr(w, err)
		return
	}
	if len(data) > landingTextLimit {
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleLandingFilePut replaces one text file from the raw body.
// id and path are URL segments. The body is limited to 2 MiB.
// A bad path is 400. A nil store is 503. Success is status saved.
func (s *Server) handleLandingFilePut(w http.ResponseWriter, r *http.Request) {
	files := s.landingFilesStore(w)
	if files == nil {
		return
	}
	rel, err := landingTextPath(r.PathValue("path"))
	if err != nil {
		writeLandingErr(w, err)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, landingTextLimit))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	if err := files.Write(r.PathValue("id"), rel, data); err != nil {
		writeLandingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// handleLandingFileDelete removes one file.
// id and path are URL segments. Any safe relative path is accepted.
// A nil store is 503. Success is status deleted. A missing file is also success.
func (s *Server) handleLandingFileDelete(w http.ResponseWriter, r *http.Request) {
	files := s.landingFilesStore(w)
	if files == nil {
		return
	}
	if err := files.Delete(r.PathValue("id"), r.PathValue("path")); err != nil {
		writeLandingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
