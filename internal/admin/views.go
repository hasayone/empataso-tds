package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/hasayone/empataso-tds/internal/security"
)

// viewsMaxBody caps a PUT /api/me/views body in bytes.
const viewsMaxBody = 64 << 10

// viewsMu serialises the read-modify-write of the views file.
var viewsMu sync.Mutex

// allowedViewPages lists the page keys a user may store; other keys are dropped.
var allowedViewPages = map[string]bool{
	"range":         true,
	"dashboard":     true,
	"flows":         true,
	"logs":          true,
	"conversions":   true,
	"report":        true,
	"offers":        true,
	"landings":      true,
	"networks":      true,
	"sources":       true,
	"domains":       true,
	"users":         true,
	"roles":         true,
	"keywords":      true,
	"keyword_stats": true,
	"automation":    true,
	"integrations":  true,
	"rating":        true,
}

// viewsFile is the views.json document: page views keyed by user, then by page.
type viewsFile struct {
	Users map[string]map[string]json.RawMessage `json:"users"`
}

// viewsUserKey returns the storage key for sess in views.json.
// File users use sess.UID; the environment administrator uses env: plus login.
func viewsUserKey(sess security.Session) string {
	if sess.UID != "" {
		return sess.UID
	}
	return "env:" + sess.User
}

// readViews loads per-user page views from path.
// A missing or empty file yields an empty store. Invalid JSON returns an error.
func readViews(path string) (viewsFile, error) {
	if path == "" {
		return viewsFile{Users: map[string]map[string]json.RawMessage{}}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return viewsFile{Users: map[string]map[string]json.RawMessage{}}, nil
		}
		return viewsFile{}, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return viewsFile{Users: map[string]map[string]json.RawMessage{}}, nil
	}
	var f viewsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return viewsFile{}, err
	}
	if f.Users == nil {
		f.Users = map[string]map[string]json.RawMessage{}
	}
	return f, nil
}

// filterViewPages keeps only allowed page keys whose values are JSON objects.
func filterViewPages(pages map[string]json.RawMessage) map[string]json.RawMessage {
	if pages == nil {
		return map[string]json.RawMessage{}
	}
	out := make(map[string]json.RawMessage, len(pages))
	for key, raw := range pages {
		if !allowedViewPages[key] || !jsonRawObject(raw) {
			continue
		}
		out[key] = json.RawMessage(bytes.TrimSpace(raw))
	}
	return out
}

// jsonRawObject reports whether raw is a JSON object.
func jsonRawObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '{'
}

// writeViews persists f to path with an atomic replace.
func writeViews(path string, f viewsFile) error {
	if f.Users == nil {
		f.Users = map[string]map[string]json.RawMessage{}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o644)
}

// emptyUserViews returns the answer for a user with nothing stored.
func emptyUserViews() map[string]json.RawMessage {
	return map[string]json.RawMessage{}
}

// handleViewsGet answers GET /api/me/views with the session user's page views, or {}.
// A damaged views file answers 500.
func (s *Server) handleViewsGet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ViewsPath == "" {
		writeJSON(w, http.StatusOK, emptyUserViews())
		return
	}
	key := viewsUserKey(sessionFrom(r.Context()))
	viewsMu.Lock()
	defer viewsMu.Unlock()
	f, err := readViews(s.cfg.ViewsPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	pages, ok := f.Users[key]
	if !ok || len(pages) == 0 {
		writeJSON(w, http.StatusOK, emptyUserViews())
		return
	}
	writeJSON(w, http.StatusOK, pages)
}

// handleViewsPut replaces the session user's page views with the body and answers the stored object.
// An empty object removes the user's entry. A body over viewsMaxBody answers 413, a non-object 400,
// a damaged views file 500 without writing, and an unset ViewsPath 503.
func (s *Server) handleViewsPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ViewsPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "views file not configured")
		return
	}
	pages, ok := decodeViewsBody(w, r)
	if !ok {
		return
	}
	key := viewsUserKey(sessionFrom(r.Context()))
	viewsMu.Lock()
	defer viewsMu.Unlock()
	f, err := readViews(s.cfg.ViewsPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	if len(pages) == 0 {
		delete(f.Users, key)
	} else {
		f.Users[key] = pages
	}
	if err := writeViews(s.cfg.ViewsPath, f); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, pages)
}

// decodeViewsBody reads and validates a PUT /api/me/views body.
// It returns false after writing an HTTP error to w.
func decodeViewsBody(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, viewsMaxBody))
	var body map[string]json.RawMessage
	if err := dec.Decode(&body); err != nil {
		writeBodyErr(w, err)
		return nil, false
	}
	return filterViewPages(body), true
}
