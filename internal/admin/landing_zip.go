package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	landingArchiveLimit = 50 << 20
	landingArchiveFiles = 2000
)

// errLandingArchive means the zip is too large, too wide, or not a zip.
var errLandingArchive = errors.New("invalid landing archive")

// Unzip replaces the landing directory with the files in a zip archive.
// id is the catalog id. data is the raw zip. It returns how many files were written.
// Archives over 50 MiB or 2000 entries are rejected. Directory entries and __MACOSX are skipped.
// A single shared top-level directory is removed. An unsafe path is errBadLandingPath.
// The previous directory contents are removed only after every path has been checked.
func (f *FileLandings) Unzip(id string, data []byte) (int, error) {
	if _, err := cleanLandingID(id); err != nil {
		return 0, err
	}
	if len(data) > landingArchiveLimit {
		return 0, errLandingArchive
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, errLandingArchive
	}
	if len(zr.File) > landingArchiveFiles {
		return 0, errLandingArchive
	}
	var names []string
	var files []*zip.File
	for _, zf := range zr.File {
		if skipZipEntry(zf) {
			continue
		}
		names = append(names, strings.ReplaceAll(zf.Name, "\\", "/"))
		files = append(files, zf)
	}
	rels := stripTopDir(names)
	cleaned := make([]string, len(rels))
	var total uint64
	for i, rel := range rels {
		c, err := cleanLandingPath(rel)
		if err != nil {
			return 0, err
		}
		size := files[i].UncompressedSize64
		if size > landingArchiveLimit || total > landingArchiveLimit-size {
			return 0, errLandingArchive
		}
		total += size
		cleaned[i] = c
	}
	if err := f.Remove(id); err != nil {
		return 0, err
	}
	root, err := f.root(id)
	if err != nil {
		return 0, err
	}
	if len(cleaned) > 0 {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return 0, err
		}
	}
	seen := map[string]bool{}
	for i, rel := range cleaned {
		if err := writeZipFile(root, rel, files[i]); err != nil {
			return 0, err
		}
		seen[rel] = true
	}
	return len(seen), nil
}

// skipZipEntry reports entries that are not landing files.
// zf is one archive entry. Directories, symlinks and __MACOSX are skipped.
// It does not return an error.
func skipZipEntry(zf *zip.File) bool {
	if zf.FileInfo().IsDir() || zf.Mode()&os.ModeSymlink != 0 {
		return true
	}
	name := strings.ReplaceAll(zf.Name, "\\", "/")
	if strings.HasSuffix(name, "/") {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == "__MACOSX" {
			return true
		}
	}
	return false
}

// stripTopDir removes one shared parent directory from every path.
// paths are archive names with forward slashes. A root file, or more than one parent, is left as-is.
// A parent of dot, dot-dot or a hidden name is not stripped. It does not return an error.
func stripTopDir(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	top := ""
	for i, path := range paths {
		path = strings.TrimPrefix(path, "/")
		slash := strings.IndexByte(path, '/')
		if slash <= 0 {
			return paths
		}
		dir := path[:slash]
		if dir == "." || dir == ".." || strings.HasPrefix(dir, ".") {
			return paths
		}
		if i == 0 {
			top = dir
			continue
		}
		if dir != top {
			return paths
		}
	}
	out := make([]string, len(paths))
	for i, path := range paths {
		path = strings.TrimPrefix(path, "/")
		out[i] = path[len(top)+1:]
	}
	return out
}

// writeZipFile extracts one archive entry under root.
// root is the landing directory. rel is a cleaned relative path. zf is the entry.
// It returns an error when the file escapes root, expands past 50 MiB, or cannot be written.
func writeZipFile(root, rel string, zf *zip.File) error {
	full := filepath.Join(root, filepath.FromSlash(rel))
	back, err := filepath.Rel(root, full)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return errBadLandingPath
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, landingArchiveLimit+1))
	if err != nil {
		return err
	}
	if len(data) > landingArchiveLimit {
		return errLandingArchive
	}
	return writeFileAtomic(full, data, 0o644)
}

// handleLandingArchive replaces a landing directory from an uploaded zip.
// The body is application/zip or a multipart field named file, at most 50 MiB.
// Success is the file count and the detected entry. When the catalog has this id, Entry is saved.
// A bad id or archive is 400. A nil store is 503. A catalog save failure is 500.
func (s *Server) handleLandingArchive(w http.ResponseWriter, r *http.Request) {
	files := s.landingFilesStore(w)
	if files == nil {
		return
	}
	id := r.PathValue("id")
	data, err := readZipUpload(w, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad archive")
		return
	}
	n, err := files.Unzip(id, data)
	if err != nil {
		writeLandingErr(w, err)
		return
	}
	entry := files.Entry(id)
	if err := s.updateLandingEntry(r.Context(), id, entry); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": n, "entry": entry})
}

// readZipUpload reads a zip from the request body.
// A multipart body uses the file field. Any other body is the zip itself.
// The reader stops at 50 MiB. It returns the bytes, or an error when the body is unreadable or too large.
func readZipUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, landingArchiveLimit)
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media == "multipart/form-data" {
		if err := r.ParseMultipartForm(landingArchiveLimit); err != nil {
			return nil, err
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return readLimited(file)
	}
	return readLimited(r.Body)
}

// readLimited reads at most one byte past the archive limit.
// r is the upload. It returns the bytes when they fit.
// It returns errLandingArchive when the upload is larger than 50 MiB, or the read error.
func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, landingArchiveLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > landingArchiveLimit {
		return nil, errLandingArchive
	}
	return data, nil
}

// updateLandingEntry stores the detected entry on the catalog row.
// id is the catalog id. entry is the relative file name, or empty when none was found.
// A missing store or a missing row is not an error. It returns an error when the catalog cannot be read or saved.
func (s *Server) updateLandingEntry(ctx context.Context, id, entry string) error {
	if s.cfg.Landings == nil {
		return nil
	}
	list, err := s.cfg.Landings.List(ctx)
	if err != nil {
		return err
	}
	found := false
	for i := range list {
		if list[i].ID != id {
			continue
		}
		list[i].Entry = entry
		found = true
		break
	}
	if !found {
		return nil
	}
	return s.cfg.Landings.Save(ctx, list)
}
