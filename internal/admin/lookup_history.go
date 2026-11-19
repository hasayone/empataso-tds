package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

const (
	// lookupHistoryFileName is the history file under DataDir.
	lookupHistoryFileName = "lookup-history.json"
	// lookupHistoryMax is the most checks kept for the whole installation; the oldest go first.
	lookupHistoryMax = 2000
	// lookupHistoryKeep is how long a check is kept.
	lookupHistoryKeep = 90 * 24 * time.Hour
	// lookupHistoryPage is the default page size of GET /api/tools/lookups.
	lookupHistoryPage = 20
	// lookupHistoryPageMax is the largest page size of GET /api/tools/lookups.
	lookupHistoryPageMax = 200
	// lookupOwnerUID is the history id of the environment administrator, who has no users.json id.
	lookupOwnerUID = "root"
)

// lookupEntry is one IP check of the Tools page: who ran it, when, and a short
// result. Lists names the .dat lists the address fell into.
type lookupEntry struct {
	TS      time.Time `json:"ts"`
	UID     string    `json:"uid"`
	Login   string    `json:"login"`
	IP      string    `json:"ip"`
	Country string    `json:"country"`
	City    string    `json:"city,omitempty"`
	ASN     uint32    `json:"asn,omitempty"`
	Org     string    `json:"org,omitempty"`
	Proxy   bool      `json:"proxy,omitempty"`
	Lists   []string  `json:"lists"`
}

// lookupUser is one author of checks, for the owner's filter.
type lookupUser struct {
	UID   string `json:"uid"`
	Login string `json:"login"`
}

// lookupHistory keeps the IP checks in one JSON file, oldest first. It is read on
// first use and rewritten after every change. An empty path keeps the history in
// memory only. A nil history records nothing and lists nothing.
type lookupHistory struct {
	mu     sync.Mutex
	path   string
	loaded bool
	rows   []lookupEntry
	now    func() time.Time
}

// newLookupHistory returns the history stored under dataDir, or kept in memory
// when dataDir is empty.
func newLookupHistory(dataDir string) *lookupHistory {
	h := &lookupHistory{now: time.Now}
	if dataDir != "" {
		h.path = filepath.Join(dataDir, lookupHistoryFileName)
	}
	return h
}

// load reads the file once. A missing file is an empty history; an unreadable or
// corrupt one is too, and the next write replaces it. Called with mu held.
func (h *lookupHistory) load() {
	if h.loaded {
		return
	}
	h.loaded = true
	if h.path == "" {
		return
	}
	b, err := os.ReadFile(h.path)
	if err != nil {
		return
	}
	var rows []lookupEntry
	if json.Unmarshal(b, &rows) == nil {
		h.rows = rows
	}
}

// prune drops checks older than lookupHistoryKeep and the oldest beyond
// lookupHistoryMax. Called with mu held.
func (h *lookupHistory) prune() {
	cut := h.now().Add(-lookupHistoryKeep)
	i := 0
	for i < len(h.rows) && h.rows[i].TS.Before(cut) {
		i++
	}
	if over := len(h.rows) - i - lookupHistoryMax; over > 0 {
		i += over
	}
	if i > 0 {
		h.rows = append([]lookupEntry(nil), h.rows[i:]...)
	}
}

// save writes the rows to the file, readable by the owner only. Called with mu held.
// It returns the write error; without a path it does nothing.
func (h *lookupHistory) save() error {
	if h.path == "" {
		return nil
	}
	b, err := json.Marshal(h.rows)
	if err != nil {
		return err
	}
	return writeFileAtomic(h.path, b, 0o600)
}

// add records e with the current time and prunes the history.
// It returns the write error; the check stays in memory either way.
func (h *lookupHistory) add(e lookupEntry) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	e.TS = h.now().UTC()
	h.rows = append(h.rows, e)
	h.prune()
	return h.save()
}

// page returns checks newest first. uid limits them to one author when it is not
// empty. offset skips rows, limit is the page size. total counts every matching row.
func (h *lookupHistory) page(uid string, offset, limit int) (rows []lookupEntry, total int) {
	rows = []lookupEntry{}
	if h == nil {
		return rows, 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	h.prune()
	for i := len(h.rows) - 1; i >= 0; i-- {
		e := h.rows[i]
		if uid != "" && e.UID != uid {
			continue
		}
		if total >= offset && len(rows) < limit {
			rows = append(rows, e)
		}
		total++
	}
	return rows, total
}

// users lists the authors in the history, each once, with the login of the newest check.
func (h *lookupHistory) users() []lookupUser {
	out := []lookupUser{}
	if h == nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	seen := map[string]bool{}
	for i := len(h.rows) - 1; i >= 0; i-- {
		if e := h.rows[i]; !seen[e.UID] {
			seen[e.UID] = true
			out = append(out, lookupUser{UID: e.UID, Login: e.Login})
		}
	}
	return out
}

// clear removes the checks of uid, or every check when uid is empty.
// It returns how many were removed and the write error.
func (h *lookupHistory) clear(uid string) (int, error) {
	if h == nil {
		return 0, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.load()
	kept := make([]lookupEntry, 0, len(h.rows))
	for _, e := range h.rows {
		if uid != "" && e.UID != uid {
			kept = append(kept, e)
		}
	}
	n := len(h.rows) - len(kept)
	h.rows = kept
	return n, h.save()
}

// isOwnerSession reports whether sess is the owner of the installation: the
// environment administrator signed in with the panel cookie. File users, ADMIN
// users included, and API keys are not.
func isOwnerSession(sess security.Session) bool {
	return !sess.API && sess.UID == ""
}

// lookupUID returns the history id of sess: lookupOwnerUID for the owner,
// "apikey:<id>" for any API key and the users.json id for a file user.
func lookupUID(sess security.Session) string {
	switch {
	case sess.API:
		return keyUserPrefix + sess.KeyID
	case sess.UID == "":
		return lookupOwnerUID
	}
	return sess.UID
}

// recordLookup adds one check of ip by the caller of r to the history. A write
// error is logged and does not change the answer of the lookup.
func (s *Server) recordLookup(r *http.Request, vi visitorInfo, lists []string) {
	sess := sessionFrom(r.Context())
	err := s.lookups.add(lookupEntry{
		UID: lookupUID(sess), Login: sess.User, IP: vi.IP, Country: vi.Country, City: vi.City,
		ASN: vi.ASN, Org: vi.Org, Proxy: vi.Proxy, Lists: lists,
	})
	if err != nil {
		s.logWarn("lookup history not saved", "err", err)
	}
}

// errLookupNotOwner is the 403 text for a history request only the owner may make.
var errLookupNotOwner = errors.New("only the owner may do this")

// lookupScope returns the author whose checks the caller of r may read or clear.
// The owner gets the user query parameter, empty for everyone. Anyone else gets
// their own id, and a user parameter naming someone else is errLookupNotOwner.
func lookupScope(r *http.Request) (uid string, owner bool, err error) {
	sess := sessionFrom(r.Context())
	want := r.URL.Query().Get("user")
	if isOwnerSession(sess) {
		return want, true, nil
	}
	self := lookupUID(sess)
	if want != "" && want != self {
		return "", false, errLookupNotOwner
	}
	return self, false, nil
}

// handleLookupHistory writes one page of IP checks, newest first:
// {rows, total, owner, users}. users is listed for the owner only. offset and
// limit page the list (limit defaults to lookupHistoryPage and is capped at
// lookupHistoryPageMax). A user parameter naming someone else is 403 for anyone
// but the owner.
func (s *Server) handleLookupHistory(w http.ResponseWriter, r *http.Request) {
	uid, owner, err := lookupScope(r)
	if err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	q := r.URL.Query()
	limit := min(max(atoiDef(q.Get("limit"), lookupHistoryPage), 1), lookupHistoryPageMax)
	rows, total := s.lookups.page(uid, max(atoiDef(q.Get("offset"), 0), 0), limit)
	out := map[string]any{"rows": rows, "total": total, "owner": owner}
	if owner {
		out["users"] = s.lookups.users()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLookupHistoryClear removes IP checks: every check, or those of the user
// query parameter. Only the owner may; anyone else gets 403. The answer is
// {"deleted": n}; a write failure is 500.
func (s *Server) handleLookupHistoryClear(w http.ResponseWriter, r *http.Request) {
	uid, owner, err := lookupScope(r)
	if err != nil || !owner {
		writeErr(w, http.StatusForbidden, errLookupNotOwner.Error())
		return
	}
	n, err := s.lookups.clear(uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "history not saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}
