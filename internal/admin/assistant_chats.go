package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// aiChatMaxBytes is the largest size of one chat file.
	aiChatMaxBytes = 1 << 20
	// aiChatReserve is the room a chat keeps free for the reply of the turn that follows a user
	// message, so the reply always fits in the file.
	aiChatReserve = 48 << 10
	// aiChatsPerUser is the most chats one user keeps; the oldest are dropped past it.
	aiChatsPerUser = 200
	// aiChatKeep is how long a chat lives after its last change.
	aiChatKeep = 60 * 24 * time.Hour
	// aiChatTitleMax is the longest chat title in characters.
	aiChatTitleMax = 80
	// aiChatsDir and aiChatIndexFile name the chat directory under the assistant directory and
	// the per-user list of chats.
	aiChatsDir      = "chats"
	aiChatIndexFile = "index.json"
	// aiChatSweepEvery is the period of the rotation of chats and changes.
	aiChatSweepEvery = time.Hour
	// aiChatCreateRoom is the free room in the assistant directory needed to start a chat.
	aiChatCreateRoom int64 = 4 << 10

	// aiErrChatFull is the code of a chat that has no room for one more message (413).
	aiErrChatFull = "ai_chat_full"
)

var (
	// aiUIDPattern is the shape of a user id that may name a directory of chats.
	aiUIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// aiChatIDPattern is the shape of a chat id: 24 lower-case hex digits.
	aiChatIDPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)
	// aiChatFileName matches the file of one chat inside a user directory.
	aiChatFileName = regexp.MustCompile(`^[0-9a-f]{24}\.json$`)
)

// aiChatStore holds the lock and the tunables of the chat files.
//
// mu serializes every read and write of the chat files and indexes, so the files of one user are
// never changed by two requests at once. dirCap, keep and now replace the directory ceiling, the
// retention and the clock in tests; zero values mean the built-in ones.
type aiChatStore struct {
	mu     sync.Mutex
	dirCap int64
	keep   time.Duration
	now    func() time.Time
}

// clock returns the current time.
func (c *aiChatStore) clock() time.Time {
	if c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

// ceiling returns the size the assistant directory may not pass.
func (c *aiChatStore) ceiling() int64 {
	if c.dirCap > 0 {
		return c.dirCap
	}
	return aiChangesDirCap
}

// retention returns how long a chat is kept.
func (c *aiChatStore) retention() time.Duration {
	if c.keep > 0 {
		return c.keep
	}
	return aiChatKeep
}

// aiChatMeta is one line of a user's chat list: what the history shows without opening a chat.
type aiChatMeta struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Messages int       `json:"messages"`
	Status   string    `json:"status"`
}

// aiChatIndex is the content of a user's index.json.
type aiChatIndex struct {
	Chats []aiChatMeta `json:"chats"`
}

// aiChatView is a whole chat as GET /api/ai/chats/{id} and POST /api/ai/chats answer: the messages
// and the changes the chat produced.
type aiChatView struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Status   string         `json:"status"`
	Created  time.Time      `json:"created"`
	Updated  time.Time      `json:"updated"`
	Messages []aiMessage    `json:"messages"`
	Changes  []aiChangeView `json:"changes"`
}

// aiChatKey returns the directory name of the chats of uid. A uid of the shape
// aiUIDPattern is used as is; any other (an environment administrator whose login holds other
// characters) maps to "h-" and a hash of the uid, so the name is always safe and stable.
func aiChatKey(uid string) string {
	if aiUIDPattern.MatchString(uid) {
		return uid
	}
	sum := sha256.Sum256([]byte(uid))
	return "h-" + hex.EncodeToString(sum[:16])
}

// aiChatDirOf returns the directory of the chats of uid, or "" when the assistant has no directory.
func (s *Server) aiChatDirOf(uid string) string {
	if s.cfg.AIDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.AIDir, aiChatsDir, aiChatKey(uid))
}

// aiChatFilePath returns the path of chat id inside dir.
func aiChatFilePath(dir, id string) string { return filepath.Join(dir, id+".json") }

// aiMetaOf returns the list line of c.
func aiMetaOf(c aiChat) aiChatMeta {
	return aiChatMeta{ID: c.ID, Title: c.Title, Created: c.Created, Updated: c.Updated, Messages: len(c.Messages), Status: c.Status}
}

// aiChatFullErr is the 413 error of a chat that cannot take more.
func aiChatFullErr() *aiError {
	return newAIError(http.StatusRequestEntityTooLarge, aiErrChatFull, "the chat is full; start a new chat")
}

// aiDiskFullErr is the 507 error of an assistant directory at its ceiling.
func aiDiskFullErr() *aiError {
	return newAIError(http.StatusInsufficientStorage, aiErrDiskFull, "no room left for the assistant history; delete old chats")
}

// aiDirSize returns the total size in bytes of the regular files under root. Unreadable entries
// count as zero.
func aiDirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// aiChatRoomLocked fails with 507 ai_disk_full when the assistant directory plus need bytes would
// pass the ceiling. The caller holds s.ai.chats.mu.
func (s *Server) aiChatRoomLocked(need int64) error {
	if aiDirSize(s.cfg.AIDir)+need > s.ai.chats.ceiling() {
		return aiDiskFullErr()
	}
	return nil
}

// aiChatRoom is aiChatRoomLocked under the lock: it tells whether a turn that adds about need
// bytes may start.
func (s *Server) aiChatRoom(need int64) error {
	s.ai.chats.mu.Lock()
	defer s.ai.chats.mu.Unlock()
	return s.aiChatRoomLocked(need)
}

// aiChatIndexLocked returns the chat list of the user directory dir. A missing directory is an empty
// list. A missing or damaged index.json is rebuilt from the chat files. The caller holds
// s.ai.chats.mu.
func (s *Server) aiChatIndexLocked(dir string) []aiChatMeta {
	raw, err := os.ReadFile(filepath.Join(dir, aiChatIndexFile))
	if err == nil {
		var idx aiChatIndex
		if json.Unmarshal(raw, &idx) == nil {
			return idx.Chats
		}
	}
	return s.aiChatRebuildLocked(dir)
}

// aiChatRebuildLocked reads every chat file of dir and returns their list lines. A file that
// cannot be read or parsed is skipped.
func (s *Server) aiChatRebuildLocked(dir string) []aiChatMeta {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var metas []aiChatMeta
	for _, e := range entries {
		if e.IsDir() || !aiChatFileName.MatchString(e.Name()) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var c aiChat
		if json.Unmarshal(raw, &c) != nil || c.ID != strings.TrimSuffix(e.Name(), ".json") {
			continue
		}
		metas = append(metas, aiMetaOf(c))
	}
	return metas
}

// aiChatWriteIndexLocked stores metas as the index of dir, the newest chat first.
// The caller holds s.ai.chats.mu.
func (s *Server) aiChatWriteIndexLocked(dir string, metas []aiChatMeta) error {
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].Updated.After(metas[j].Updated) })
	if metas == nil {
		metas = []aiChatMeta{}
	}
	path := filepath.Join(dir, aiChatIndexFile)
	raw, err := json.Marshal(aiChatIndex{Chats: metas})
	if err == nil {
		err = aiChgWriteDurable(path, append(raw, '\n'))
	}
	if err != nil {
		// A stale index would hide or list chats that are not there; without it the next read rebuilds it.
		_ = os.Remove(path)
		s.logWarn("assistant chat index not saved")
		return aiPlainError(http.StatusInternalServerError, "chat index cannot be written")
	}
	return nil
}

// aiChatLoadLocked reads chat id of uid. An id of a wrong shape, a missing file and a chat of
// another user are all 404; a file that cannot be read or parsed is a 500 that holds none of its
// content. The caller holds s.ai.chats.mu.
func (s *Server) aiChatLoadLocked(uid, id string) (aiChat, error) {
	dir := s.aiChatDirOf(uid)
	if dir == "" {
		return aiChat{}, errAIChgNoDir
	}
	if !aiChatIDPattern.MatchString(id) {
		return aiChat{}, aiPlainError(http.StatusNotFound, "not found")
	}
	raw, err := os.ReadFile(aiChatFilePath(dir, id))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return aiChat{}, aiPlainError(http.StatusNotFound, "not found")
	case err != nil:
		return aiChat{}, aiPlainError(http.StatusInternalServerError, "chat cannot be read")
	}
	var c aiChat
	if json.Unmarshal(raw, &c) != nil || c.ID != id {
		return aiChat{}, aiPlainError(http.StatusInternalServerError, "chat is damaged")
	}
	if c.UID != uid {
		return aiChat{}, aiPlainError(http.StatusNotFound, "not found")
	}
	if c.Messages == nil {
		c.Messages = []aiMessage{}
	}
	return c, nil
}

// aiChatSaveLocked writes c and refreshes its line of the index. touch sets Updated to now; the
// recovery of a restart keeps the old time. A chat over aiChatMaxBytes is not written (413
// ai_chat_full). Files are 0600 in directories 0700, written through a flushed temporary file.
// The caller holds s.ai.chats.mu.
func (s *Server) aiChatSaveLocked(c *aiChat, touch bool) error {
	dir := s.aiChatDirOf(c.UID)
	if dir == "" {
		return errAIChgNoDir
	}
	if touch {
		c.Updated = s.ai.chats.clock()
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return aiPlainError(http.StatusInternalServerError, "chat cannot be encoded")
	}
	if len(raw) > aiChatMaxBytes {
		return aiChatFullErr()
	}
	if err := aiChgWriteDurable(aiChatFilePath(dir, c.ID), append(raw, '\n')); err != nil {
		s.logWarn("assistant chat not saved")
		return aiPlainError(http.StatusInternalServerError, "chat cannot be written")
	}
	metas := s.aiChatIndexLocked(dir)
	line, found := aiMetaOf(*c), false
	for i := range metas {
		if metas[i].ID == c.ID {
			metas[i], found = line, true
		}
	}
	if !found {
		metas = append(metas, line)
	}
	return s.aiChatWriteIndexLocked(dir, metas)
}

// aiChatTitle cleans a title: control characters and runs of blanks become one space, secrets are
// cut out, and the text is cut to aiChatTitleMax characters.
func (s *Server) aiChatTitle(raw string) string {
	flat := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, raw)
	flat = strings.Join(strings.Fields(flat), " ")
	return aiCutRunes(aiRedactText(flat, s.processSecrets()), aiChatTitleMax)
}

// aiCutRunes returns s cut to at most n characters.
func aiCutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// aiChatCreate makes an empty chat for uid. title is cleaned by aiChatTitle and may be empty. The
// oldest chats are dropped while the user holds aiChatsPerUser or more, and a chat that runs is
// never dropped. It returns 503 ai_disabled without an assistant directory, 507 ai_disk_full at
// the directory ceiling, or the chat.
func (s *Server) aiChatCreate(uid, title string) (aiChat, error) {
	dir := s.aiChatDirOf(uid)
	if dir == "" {
		return aiChat{}, errAIChgNoDir
	}
	title = s.aiChatTitle(title)
	s.ai.chats.mu.Lock()
	defer s.ai.chats.mu.Unlock()
	if err := s.aiChatRoomLocked(aiChatCreateRoom); err != nil {
		return aiChat{}, err
	}
	metas := s.aiChatIndexLocked(dir)
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].Updated.Before(metas[j].Updated) })
	for len(metas) >= aiChatsPerUser {
		drop := -1
		for i, m := range metas {
			if m.Status != aiChatRunning {
				drop = i
				break
			}
		}
		if drop < 0 {
			return aiChat{}, newAIError(http.StatusConflict, aiErrBusy, "too many chats are running")
		}
		_ = os.Remove(aiChatFilePath(dir, metas[drop].ID))
		metas = append(metas[:drop], metas[drop+1:]...)
	}
	if err := s.aiChatWriteIndexLocked(dir, metas); err != nil {
		return aiChat{}, err
	}
	id, err := aiChgNewID()
	if err != nil {
		return aiChat{}, err
	}
	now := s.ai.chats.clock()
	c := aiChat{ID: id, UID: uid, Title: title, Status: aiChatIdle, Created: now, Updated: now, Messages: []aiMessage{}}
	if err := s.aiChatSaveLocked(&c, false); err != nil {
		return aiChat{}, err
	}
	return c, nil
}

// aiChatGet returns chat id of uid, or 404 when it is not there or belongs to someone else.
func (s *Server) aiChatGet(uid, id string) (aiChat, error) {
	s.ai.chats.mu.Lock()
	defer s.ai.chats.mu.Unlock()
	return s.aiChatLoadLocked(uid, id)
}

// aiChatUpdate loads chat id of uid, lets fn change it and writes it back with a new Updated.
// fn returning an error leaves the file alone and passes the error on. The chat after the change
// is returned.
func (s *Server) aiChatUpdate(uid, id string, fn func(c *aiChat) error) (aiChat, error) {
	s.ai.chats.mu.Lock()
	defer s.ai.chats.mu.Unlock()
	c, err := s.aiChatLoadLocked(uid, id)
	if err != nil {
		return aiChat{}, err
	}
	if err := fn(&c); err != nil {
		return aiChat{}, err
	}
	if err := s.aiChatSaveLocked(&c, true); err != nil {
		return aiChat{}, err
	}
	return c, nil
}

// aiChatSearch returns the chat list of uid, the newest first. A non-empty q keeps the chats
// whose title holds q, without regard to case.
func (s *Server) aiChatSearch(uid, q string) ([]aiChatMeta, error) {
	dir := s.aiChatDirOf(uid)
	if dir == "" {
		return nil, errAIChgNoDir
	}
	s.ai.chats.mu.Lock()
	metas := s.aiChatIndexLocked(dir)
	s.ai.chats.mu.Unlock()
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]aiChatMeta, 0, len(metas))
	for _, m := range metas {
		if q == "" || strings.Contains(strings.ToLower(m.Title), q) {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// aiChatDelete removes chat id of uid. The changes the chat produced stay. A chat that runs is
// 409 ai_busy; an unknown id is 404.
func (s *Server) aiChatDelete(uid, id string) error {
	s.ai.chats.mu.Lock()
	defer s.ai.chats.mu.Unlock()
	c, err := s.aiChatLoadLocked(uid, id)
	if err != nil {
		return err
	}
	if c.Status == aiChatRunning {
		return newAIError(http.StatusConflict, aiErrBusy, "the chat is answering; wait for the end of the reply")
	}
	dir := s.aiChatDirOf(uid)
	if err := os.Remove(aiChatFilePath(dir, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return aiPlainError(http.StatusInternalServerError, "chat cannot be removed")
	}
	var kept []aiChatMeta
	for _, m := range s.aiChatIndexLocked(dir) {
		if m.ID != id {
			kept = append(kept, m)
		}
	}
	return s.aiChatWriteIndexLocked(dir, kept)
}
