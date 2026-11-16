package admin

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

const (
	// keysPreviewLines is the most keyword lines GET /api/keys returns.
	keysPreviewLines = 5000

	// keysPreviewBytes is the most bytes GET /api/keys reads from one list.
	keysPreviewBytes = 1 << 20
)

// errBadKeysName is the error of a keyword list request whose group or date is not a safe name.
var errBadKeysName = errors.New("invalid group/date")

// KeysReader opens the collected keyword lists.
// Open takes the group, the date (YYYY-MM-DD) and se for the search-engine list.
// It returns the reader, which the caller closes, and the list size in bytes.
// A bad group or date is errBadKeysName, a missing list wraps fs.ErrNotExist.
type KeysReader interface {
	Open(group, date string, se bool) (io.ReadCloser, int64, error)
}

// keysPreview is the GET /api/keys answer.
// Content is the first Lines lines of the list. Truncated is true when the list
// holds more than Content. Size is the full list length in bytes. MaxLines is
// the line cap of the answer.
type keysPreview struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Lines     int    `json:"lines"`
	Size      int64  `json:"size"`
	MaxLines  int    `json:"max_lines"`
}

// FileKeys reads collected keywords from keysDir/<group>/<date>[-se].dat.
type FileKeys struct{ dir string }

// NewFileKeys creates a keys reader in directory dir.
func NewFileKeys(dir string) *FileKeys { return &FileKeys{dir: dir} }

// Open opens the list of group for date; se selects the search-engine list.
// It returns the open file and its size in bytes; the caller closes the file.
// A group or date outside [a-zA-Z0-9._-] is errBadKeysName. A missing list
// wraps fs.ErrNotExist; another disk failure is returned as is.
func (k *FileKeys) Open(group, date string, se bool) (io.ReadCloser, int64, error) {
	if !safeKeysSeg(group) || !safeKeysSeg(date) {
		return nil, 0, errBadKeysName
	}
	f, err := os.Open(filepath.Join(k.dir, group, keysBaseName(date, se)+".dat"))
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// safeKeysSeg reports whether s may be one segment of a keyword list path:
// safeSeg characters and not a dot directory.
func safeKeysSeg(s string) bool {
	return s != "." && s != ".." && safeSeg.MatchString(s)
}

// keysBaseName returns the file name of the list for date without the extension;
// se selects the search-engine list.
func keysBaseName(date string, se bool) string {
	if se {
		return date + "-se"
	}
	return date
}

// readKeysPreview reads the start of a keyword list from r.
// It keeps at most maxLines lines and maxBytes bytes and never reads more than
// maxBytes+1 bytes. A cut by bytes ends at the last whole line when there is one.
// It returns the text, whether the list holds more, and a read error.
func readKeysPreview(r io.Reader, maxLines int, maxBytes int64) ([]byte, bool, error) {
	buf, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	truncated := false
	if int64(len(buf)) > maxBytes {
		truncated = true
		buf = buf[:maxBytes]
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			buf = buf[:i+1]
		}
	}
	seen := 0
	for i, c := range buf {
		if c != '\n' {
			continue
		}
		if seen++; seen == maxLines {
			if i+1 < len(buf) {
				buf, truncated = buf[:i+1], true
			}
			break
		}
	}
	return buf, truncated, nil
}

// countLines returns the number of lines in b; a last line without a line feed counts.
func countLines(b []byte) int {
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// keysTarget checks that the caller may read the keyword lists of the group in r.
// It writes 503 without keys storage, 403 when the keyword field is hidden, 404 for a
// campaign outside the session view and 500 when the campaigns cannot be loaded.
// It returns the group id and false when it has written the answer.
func (s *Server) keysTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.cfg.Keys == nil {
		writeErr(w, http.StatusServiceUnavailable, "keys unavailable")
		return "", false
	}
	v := s.viewerFrom(r.Context())
	if fieldHidden(v, "keyword") {
		writeErr(w, http.StatusForbidden, "forbidden")
		return "", false
	}
	groupID := r.URL.Query().Get("group")
	if !v.admin && groupID != "" {
		ref, ok, err := s.campaignRefByID(r.Context(), groupID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "groups error")
			return "", false
		}
		if !ok || !v.canSee(resCampaigns, ref) {
			writeErr(w, http.StatusNotFound, "not found")
			return "", false
		}
	}
	return groupID, true
}

// handleKeys writes the start of the keyword list of one campaign and day.
// r supplies group, date and se=1 for the search-engine list. The body is
// keysPreview: at most keysPreviewLines lines and keysPreviewBytes bytes, with
// truncated set when the list is longer. Access errors are those of keysTarget.
// A list that cannot be read is an empty content with 200.
func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	groupID, ok := s.keysTarget(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	out := keysPreview{MaxLines: keysPreviewLines}
	rc, size, err := s.cfg.Keys.Open(groupID, q.Get("date"), q.Get("se") == "1")
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	defer rc.Close()
	buf, truncated, err := readKeysPreview(rc, keysPreviewLines, keysPreviewBytes)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Content, out.Truncated, out.Lines, out.Size = string(buf), truncated, countLines(buf), size
	writeJSON(w, http.StatusOK, out)
}

// handleKeysDownload streams the whole keyword list of one campaign and day as an attachment.
// r supplies group, date and se=1 like handleKeys. Access errors are those of
// keysTarget; a bad group or date is 400, a missing list 404, another disk error 500.
// The file is copied to w without being held in memory.
func (s *Server) handleKeysDownload(w http.ResponseWriter, r *http.Request) {
	groupID, ok := s.keysTarget(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	date, se := q.Get("date"), q.Get("se") == "1"
	rc, size, err := s.cfg.Keys.Open(groupID, date, se)
	switch {
	case errors.Is(err, errBadKeysName):
		writeErr(w, http.StatusBadRequest, "bad group or date")
		return
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "keys error")
		return
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="keywords-`+groupID+"-"+keysBaseName(date, se)+`.txt"`)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, rc, size)
}
