package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/egerkuzma/emtds/internal/security"
)

// aiChgPath returns the path of changes.json, or "" when the assistant has no directory.
func (s *Server) aiChgPath() string {
	if s.cfg.AIDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.AIDir, aiChangesFile)
}

// aiChgLoadLocked reads changes.json. A missing file is an empty list. The caller holds
// s.ai.changesMu. A file that cannot be read or parsed is a 500 error that never holds its content,
// and the file is left alone so that nothing overwrites it.
func (s *Server) aiChgLoadLocked() ([]aiChangeRec, error) {
	path := s.aiChgPath()
	if path == "" {
		return nil, errAIChgNoDir
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, aiPlainError(http.StatusInternalServerError, "changes.json cannot be read")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var doc aiChangesDoc
	if json.Unmarshal(raw, &doc) != nil {
		return nil, aiPlainError(http.StatusInternalServerError, "changes.json is not valid JSON")
	}
	return doc.Changes, nil
}

// aiChgSaveLocked writes recs to changes.json: durable, atomic, mode 0600, directory 0700.
// The caller holds s.ai.changesMu. A failure is a 500 error that is also logged without data.
func (s *Server) aiChgSaveLocked(recs []aiChangeRec) error {
	if recs == nil {
		recs = []aiChangeRec{}
	}
	raw, err := json.Marshal(aiChangesDoc{Changes: recs})
	if err == nil {
		err = aiChgWriteDurable(s.aiChgPath(), append(raw, '\n'))
	}
	if err != nil {
		s.logWarn("assistant changes not saved", "error_type", fmt.Sprintf("%T", err))
		return aiPlainError(http.StatusInternalServerError, "changes.json cannot be written")
	}
	return nil
}

// aiChgWriteDurable writes data to path through a temporary file that is flushed to disk before
// it is renamed into place, so a crash leaves the old file or the new one and never a torn one.
// The file gets mode 0600 and a missing directory is made with mode 0700. The directory itself is
// flushed when the platform allows it. It returns the first error.
func aiChgWriteDurable(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// aiChgMutate runs fn on the entries of changes.json under the file lock and saves them when fn
// reports a change. fn may change the slice and its entries. It returns the error of the load, of
// fn (nothing is saved then) or of the save.
func (s *Server) aiChgMutate(fn func(recs *[]aiChangeRec) (changed bool, err error)) error {
	s.ai.changesMu.Lock()
	defer s.ai.changesMu.Unlock()
	recs, err := s.aiChgLoadLocked()
	if err != nil {
		return err
	}
	changed, err := fn(&recs)
	if err != nil || !changed {
		return err
	}
	return s.aiChgSaveLocked(recs)
}

// aiChgRead returns a copy of every entry of changes.json.
func (s *Server) aiChgRead() ([]aiChangeRec, error) {
	s.ai.changesMu.Lock()
	defer s.ai.changesMu.Unlock()
	return s.aiChgLoadLocked()
}

// aiChgPatch changes the entry with the given id through fn and saves it.
// It returns the entry as saved, 404 for an unknown id, or the error of fn or of the file.
func (s *Server) aiChgPatch(id string, fn func(r *aiChangeRec) error) (aiChangeRec, error) {
	var out aiChangeRec
	err := s.aiChgMutate(func(recs *[]aiChangeRec) (bool, error) {
		for i := range *recs {
			if (*recs)[i].Proposal.ID != id {
				continue
			}
			if err := fn(&(*recs)[i]); err != nil {
				return false, err
			}
			out = (*recs)[i]
			return true, nil
		}
		return false, aiPlainError(http.StatusNotFound, "not found")
	})
	return out, err
}

// aiChgOwn returns the entry with the given id when uid is its author.
// An unknown id and an entry of another user are both 404, so a user cannot probe for ids.
func (s *Server) aiChgOwn(id, uid string) (aiChangeRec, error) {
	recs, err := s.aiChgRead()
	if err != nil {
		return aiChangeRec{}, err
	}
	for _, r := range recs {
		if r.Proposal.ID == id && r.Proposal.UID == uid {
			return r, nil
		}
	}
	return aiChangeRec{}, aiPlainError(http.StatusNotFound, "not found")
}

// aiChgSession returns the session of the request when it may use the assistant.
// Otherwise it writes 403 and returns false.
func (s *Server) aiChgSession(w http.ResponseWriter, r *http.Request) (security.Session, bool) {
	sess := sessionFrom(r.Context())
	if !s.aiAllowed(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return sess, false
	}
	return sess, true
}

// aiChgReadBody decodes an optional small JSON body into v. An empty body leaves v alone.
// It writes 400 ai_bad_request (field body, reason invalid) and returns false for a body that is
// not JSON.
func aiChgReadBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		return true
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		aiWriteErr(w, aiBadJSON())
		return false
	}
	return true
}

// handleAIChangesList answers GET /api/ai/changes?chat_id=&status= with the changes of the
// caller only, oldest first, as {"changes":[card...]}. Both filters are optional.
// A session outside the beta is 403.
func (s *Server) handleAIChangesList(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.aiChgSession(w, r)
	if !ok {
		return
	}
	recs, err := s.aiChgRead()
	if err != nil {
		s.failWrite(w, err)
		return
	}
	uid, chat, status := s.aiUsageUID(sess), r.URL.Query().Get("chat_id"), r.URL.Query().Get("status")
	out := []aiChangeView{}
	for _, rec := range recs {
		if rec.Proposal.UID != uid || (chat != "" && rec.Proposal.ChatID != chat) || (status != "" && rec.Change.Status != status) {
			continue
		}
		out = append(out, rec.view())
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

// handleAIChangeApply answers POST /api/ai/changes/{id}/apply. The optional body is
// {"confirm_delete":bool,"confirm_host":bool}; the record to write is never read from it.
// The answer is the card, or the error of applyChange.
func (s *Server) handleAIChangeApply(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.aiChgSession(w, r)
	if !ok {
		return
	}
	var body struct {
		ConfirmDelete bool `json:"confirm_delete"`
		ConfirmHost   bool `json:"confirm_host"`
	}
	if !aiChgReadBody(w, r, &body) {
		return
	}
	view, err := s.applyChange(r.Context(), sess, r.PathValue("id"), aiApplyOpts{ConfirmDelete: body.ConfirmDelete, ConfirmHost: body.ConfirmHost})
	if err != nil {
		s.failWrite(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleAIChangeReject answers POST /api/ai/changes/{id}/reject with the rejected card.
func (s *Server) handleAIChangeReject(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.aiChgSession(w, r)
	if !ok {
		return
	}
	view, err := s.rejectChange(r.Context(), sess, r.PathValue("id"))
	if err != nil {
		s.failWrite(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleAIChangeRevert answers POST /api/ai/changes/{id}/revert with the reverted card, or 409
// ai_conflict or keitaro_running.
func (s *Server) handleAIChangeRevert(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.aiChgSession(w, r)
	if !ok {
		return
	}
	view, err := s.revertChange(r.Context(), sess, r.PathValue("id"))
	if err != nil {
		s.failWrite(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleAIMessageRevert answers POST /api/ai/messages/{message_id}/revert with the body
// {"chat_id"}: every applied change of that reply is rolled back, last first, and the run stops
// at the first one that cannot be. The answer is {"reverted":[ids],"stopped_at"?,"error"?}.
func (s *Server) handleAIMessageRevert(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.aiChgSession(w, r)
	if !ok {
		return
	}
	var body struct {
		ChatID string `json:"chat_id"`
	}
	if !aiChgReadBody(w, r, &body) {
		return
	}
	if body.ChatID == "" {
		aiWriteErr(w, aiBadRequest("chat_id", aiReasonRequired, "chat_id required"))
		return
	}
	report, err := s.revertMessage(r.Context(), sess, body.ChatID, r.PathValue("message_id"))
	if err != nil {
		s.failWrite(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleAIChatRevert answers POST /api/ai/chats/{id}/revert: every applied change of the chat of
// the caller is rolled back, last first, and the run stops at the first one that cannot be. The
// answer is 200 {"reverted":[ids],"remaining"?,"stopped_at"?,"error"?,"error_code"?,"error_params"?};
// 403 for a session outside the beta or an API key.
func (s *Server) handleAIChatRevert(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.aiChgSession(w, r)
	if !ok {
		return
	}
	report, err := s.revertChat(r.Context(), sess, r.PathValue("id"))
	if err != nil {
		s.failWrite(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
