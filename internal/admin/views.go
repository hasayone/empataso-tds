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
	"ui":            true,
}

// uiViewKeys are the fields of the ui page. Anything else, including a token,
// a secret, a board or the account language, is dropped.
var uiViewKeys = map[string]bool{
	"theme":     true,
	"accent":    true,
	"layout":    true,
	"menu":      true,
	"cols":      true,
	"dock":      true,
	"assistant": true,
}

// viewsFile is the views.json document: page views keyed by user, then by page.
// Rev is the revision of each user's record. A missing entry is revision 0.
type viewsFile struct {
	Users map[string]map[string]json.RawMessage `json:"users"`
	Rev   map[string]int64                      `json:"rev,omitempty"`
}

// revOf returns the stored revision of key. A missing map or key is 0.
func (f viewsFile) revOf(key string) int64 {
	if f.Rev == nil {
		return 0
	}
	return f.Rev[key]
}

// setRev stores rev for key, creating the map when it is nil.
func (f *viewsFile) setRev(key string, rev int64) {
	if f.Rev == nil {
		f.Rev = map[string]int64{}
	}
	f.Rev[key] = rev
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
// The ui page is reduced to uiViewKeys. A page that is not an object is dropped,
// which does not delete a page already stored.
func filterViewPages(pages map[string]json.RawMessage) map[string]json.RawMessage {
	if pages == nil {
		return map[string]json.RawMessage{}
	}
	out := make(map[string]json.RawMessage, len(pages))
	for key, raw := range pages {
		if !allowedViewPages[key] || !jsonRawObject(raw) {
			continue
		}
		if key == "ui" {
			raw = sanitizeUIPage(raw)
			if len(raw) == 0 {
				continue
			}
		}
		out[key] = json.RawMessage(bytes.TrimSpace(raw))
	}
	return out
}

// sanitizeUIPage keeps the known fields of a ui page.
// raw is the JSON object. It returns that object with only uiViewKeys, or nil
// when nothing known is left or raw is not an object.
func sanitizeUIPage(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(uiViewKeys))
	for key, val := range obj {
		if uiViewKeys[key] {
			out[key] = val
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
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
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

// viewsAnswer is the GET and PUT body: the user's pages plus rev.
// pages may be nil. Keys that are not an allowed object are left out.
// rev is the revision the caller must send back.
func viewsAnswer(rev int64, pages map[string]json.RawMessage) map[string]any {
	out := make(map[string]any, len(pages)+1)
	out["rev"] = rev
	for key, raw := range pages {
		if !allowedViewPages[key] || !jsonRawObject(raw) {
			continue
		}
		out[key] = json.RawMessage(bytes.TrimSpace(raw))
	}
	return out
}

// emptyUserViews returns the answer for a user with nothing stored.
func emptyUserViews() map[string]json.RawMessage {
	return map[string]json.RawMessage{}
}

// handleViewsGet answers GET /api/me/views with the session user's pages and rev.
// A user with nothing stored gets rev 0. An unset ViewsPath answers {}.
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
	writeJSON(w, http.StatusOK, viewsAnswer(f.revOf(key), f.Users[key]))
}

// viewsWrite is a validated PUT /api/me/views body.
// rev is the caller's revision; hasRev is false when the body omitted it.
// owner is the views key the caller claims, or empty when the body omitted it.
// pages holds only the pages the caller sent.
type viewsWrite struct {
	rev    int64
	hasRev bool
	owner  string
	pages  map[string]json.RawMessage
}

// handleViewsPut merges the sent pages into the session user's record.
// A sent page replaces only that page. Pages the body does not mention stay.
// An empty body does not remove the user's key. rev must equal the stored
// revision, otherwise the answer is 409 and the file is not written; a missing
// rev matches only revision 0. owner, when set, must be this session's key,
// otherwise the answer is 403. A body over viewsMaxBody answers 413, a
// non-object 400, a damaged views file 500 without writing, and an unset
// ViewsPath 503.
func (s *Server) handleViewsPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ViewsPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "views file not configured")
		return
	}
	put, ok := decodeViewsBody(w, r)
	if !ok {
		return
	}
	key := viewsUserKey(sessionFrom(r.Context()))
	if put.owner != "" && put.owner != key {
		writeErr(w, http.StatusForbidden, "views owner")
		return
	}
	viewsMu.Lock()
	defer viewsMu.Unlock()
	f, err := readViews(s.cfg.ViewsPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	stored := f.revOf(key)
	if put.hasRev {
		if put.rev != stored {
			writeErr(w, http.StatusConflict, "views conflict")
			return
		}
	} else if stored != 0 {
		writeErr(w, http.StatusConflict, "views conflict")
		return
	}
	pages := f.Users[key]
	if len(put.pages) == 0 {
		writeJSON(w, http.StatusOK, viewsAnswer(stored, pages))
		return
	}
	if pages == nil {
		pages = map[string]json.RawMessage{}
	}
	for name, raw := range put.pages {
		pages[name] = raw
	}
	if f.Users == nil {
		f.Users = map[string]map[string]json.RawMessage{}
	}
	f.Users[key] = pages
	next := stored + 1
	f.setRev(key, next)
	if err := writeViews(s.cfg.ViewsPath, f); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, viewsAnswer(next, pages))
}

// dropUserViews removes id from views.json.
// id is the users.json id, the same key the user's session writes.
// A missing file, an empty ViewsPath and an id that is not stored are not an error.
// A damaged file is returned and left unchanged. The caller holds userMu;
// this function takes viewsMu and must not be called while viewsMu is held.
func (s *Server) dropUserViews(id string) error {
	if s == nil || id == "" || s.cfg.ViewsPath == "" {
		return nil
	}
	viewsMu.Lock()
	defer viewsMu.Unlock()
	f, err := readViews(s.cfg.ViewsPath)
	if err != nil {
		return err
	}
	_, hasUser := f.Users[id]
	hasRev := false
	if f.Rev != nil {
		_, hasRev = f.Rev[id]
	}
	if !hasUser && !hasRev {
		return nil
	}
	delete(f.Users, id)
	if f.Rev != nil {
		delete(f.Rev, id)
		if len(f.Rev) == 0 {
			f.Rev = nil
		}
	}
	return writeViews(s.cfg.ViewsPath, f)
}

// decodeViewsBody reads and validates a PUT /api/me/views body.
// It returns false after writing an HTTP error to w.
func decodeViewsBody(w http.ResponseWriter, r *http.Request) (viewsWrite, bool) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, viewsMaxBody))
	var body map[string]json.RawMessage
	if err := dec.Decode(&body); err != nil {
		writeBodyErr(w, err)
		return viewsWrite{}, false
	}
	if body == nil {
		body = map[string]json.RawMessage{}
	}
	var put viewsWrite
	if raw, ok := body["rev"]; ok {
		n, err := parseViewsRev(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad request")
			return viewsWrite{}, false
		}
		put.rev = n
		put.hasRev = true
		delete(body, "rev")
	}
	if raw, ok := body["owner"]; ok {
		var owner string
		if err := json.Unmarshal(bytes.TrimSpace(raw), &owner); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request")
			return viewsWrite{}, false
		}
		put.owner = strings.TrimSpace(owner)
		delete(body, "owner")
	}
	put.pages = filterViewPages(body)
	return put, true
}

// parseViewsRev reads a revision from raw.
// raw is a JSON number. Null counts as 0. A negative or a non-integer is an error.
func parseViewsRev(raw json.RawMessage) (int64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil || n < 0 {
		return 0, errors.New("bad rev")
	}
	return n, nil
}
