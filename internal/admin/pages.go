package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
)

// PageInfo is one HTML file in the pages directory.
// Name is the file name. Size is the length in bytes.
type PageInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// PagesStore reads and writes HTML files named by stream output @page:.
// Names lists files. Read returns one body. Write replaces one file.
// Delete removes one file. A missing file is not an error from Delete.
// An invalid name is an error from every method. Disk failures are errors.
type PagesStore interface {
	Names() ([]PageInfo, error)
	Read(name string) (string, error)
	Write(name, content string) error
	Delete(name string) error
}

// handlePagesIndex lists HTML page files.
// It returns an empty array when the directory has no pages.
// A nil store is 503. A directory error is 500.
// It does not return an error value.
func (s *Server) handlePagesIndex(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Pages == nil {
		writeErr(w, http.StatusServiceUnavailable, "pages unavailable")
		return
	}
	infos, err := s.cfg.Pages.Names()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "pages error")
		return
	}
	if infos == nil {
		infos = []PageInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

// handlePageRead returns one page body as name and content.
// name is the URL path segment. A bad name is 400. A missing file is 404.
// A nil store is 503. Another read error is 500.
// It does not return an error value.
func (s *Server) handlePageRead(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Pages == nil {
		writeErr(w, http.StatusServiceUnavailable, "pages unavailable")
		return
	}
	name := r.PathValue("name")
	content, err := s.cfg.Pages.Read(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if errors.Is(err, errBadPageName) {
			writeErr(w, http.StatusBadRequest, "bad name")
			return
		}
		writeErr(w, http.StatusInternalServerError, "pages error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "content": content})
}

// handlePageWrite replaces one page from a JSON body with content.
// name is the URL path segment. The body is limited to 4 MiB.
// A bad name or a bad body is 400. A nil store is 503. A disk error is 500.
// Success is status saved. It does not return an error value.
func (s *Server) handlePageWrite(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Pages == nil {
		writeErr(w, http.StatusServiceUnavailable, "pages unavailable")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	name := r.PathValue("name")
	if err := s.cfg.Pages.Write(name, body.Content); err != nil {
		if errors.Is(err, errBadPageName) {
			writeErr(w, http.StatusBadRequest, "bad name")
			return
		}
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// handlePageDelete removes one page file.
// name is the URL path segment. A bad name is 400. A missing file is status deleted.
// A nil store is 503. A disk error is 500.
// It does not return an error value.
func (s *Server) handlePageDelete(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Pages == nil {
		writeErr(w, http.StatusServiceUnavailable, "pages unavailable")
		return
	}
	name := r.PathValue("name")
	if err := s.cfg.Pages.Delete(name); err != nil {
		if errors.Is(err, errBadPageName) {
			writeErr(w, http.StatusBadRequest, "bad name")
			return
		}
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
