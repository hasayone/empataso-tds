package admin

import (
	"net/http"
	"sort"
	"strings"
)

// aiChatGate is the common start of the chat routes: the session must be allowed, the assistant
// directory set and the assistant switched on. It writes the refusal and returns false otherwise:
// 403 forbidden, 503 ai_disabled. On success it returns the settings and the chat uid of the
// session.
func (s *Server) aiChatGate(w http.ResponseWriter, r *http.Request) (aiSettings, string, bool) {
	sess := sessionFrom(r.Context())
	if !s.aiAllowed(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return aiSettings{}, "", false
	}
	st, ok := s.aiEnabledOrWrite(w)
	if !ok {
		return aiSettings{}, "", false
	}
	return st, s.aiUsageUID(sess), true
}

// aiEnabledOrWrite loads the settings and checks the assistant is on and has a directory. It
// writes 503 ai_disabled and returns false otherwise.
func (s *Server) aiEnabledOrWrite(w http.ResponseWriter) (aiSettings, bool) {
	st, err := s.aiLoad()
	if err != nil || !st.Enabled || s.cfg.AIDir == "" {
		writeErr(w, http.StatusServiceUnavailable, aiErrDisabled+": the assistant is switched off")
		return st, false
	}
	return st, true
}

// handleAIChatsList answers GET /api/ai/chats?q= with {"chats":[...]}: the caller's chats, the
// newest first, filtered by title when q is set.
func (s *Server) handleAIChatsList(w http.ResponseWriter, r *http.Request) {
	_, uid, ok := s.aiChatGate(w, r)
	if !ok {
		return
	}
	metas, err := s.aiChatSearch(uid, r.URL.Query().Get("q"))
	if err != nil {
		s.failWrite(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": metas})
}

// handleAIChatCreate answers POST /api/ai/chats {"title"?} with the new chat (201). A full
// assistant directory is 507 ai_disk_full.
func (s *Server) handleAIChatCreate(w http.ResponseWriter, r *http.Request) {
	_, uid, ok := s.aiChatGate(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if !aiChgReadBody(w, r, &body) {
		return
	}
	c, err := s.aiChatCreate(uid, body.Title)
	if err != nil {
		s.failWrite(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, aiChatViewOf(c, nil))
}

// handleAIChatGet answers GET /api/ai/chats/{id} with the messages and the changes of the chat.
// A chat of another user is 404.
func (s *Server) handleAIChatGet(w http.ResponseWriter, r *http.Request) {
	_, uid, ok := s.aiChatGate(w, r)
	if !ok {
		return
	}
	c, err := s.aiChatGet(uid, r.PathValue("id"))
	if err != nil {
		s.failWrite(w, err)
		return
	}
	changes, err := s.aiChatChanges(uid, c.ID)
	if err != nil {
		s.failWrite(w, err)
		return
	}
	writeJSON(w, http.StatusOK, aiChatViewOf(c, changes))
}

// handleAIChatDelete answers DELETE /api/ai/chats/{id} with 204. The changes of the chat stay and
// can still be reverted. A chat that is answering is 409 ai_busy.
func (s *Server) handleAIChatDelete(w http.ResponseWriter, r *http.Request) {
	_, uid, ok := s.aiChatGate(w, r)
	if !ok {
		return
	}
	if err := s.aiChatDelete(uid, r.PathValue("id")); err != nil {
		s.failWrite(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// aiChatViewOf builds the answer for chat c with its changes (nil becomes an empty list).
func aiChatViewOf(c aiChat, changes []aiChangeView) aiChatView {
	if changes == nil {
		changes = []aiChangeView{}
	}
	if c.Messages == nil {
		c.Messages = []aiMessage{}
	}
	return aiChatView{ID: c.ID, Title: c.Title, Status: c.Status, Created: c.Created, Updated: c.Updated, Messages: c.Messages, Changes: changes}
}

// aiChatChanges returns the cards of the changes uid made in chat chatID, oldest first.
func (s *Server) aiChatChanges(uid, chatID string) ([]aiChangeView, error) {
	recs, err := s.aiChgRead()
	if err != nil {
		return nil, err
	}
	out := []aiChangeView{}
	for _, rec := range recs {
		if rec.Proposal.UID == uid && rec.Proposal.ChatID == chatID {
			out = append(out, rec.view())
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out, nil
}

// aiUIDExists reports whether the account behind uid still exists: the environment administrator
// always does, a file user while it is in users.json.
func (s *Server) aiUIDExists(uid string) bool {
	if strings.HasPrefix(uid, "env-") {
		return true
	}
	_, ok := s.userByID(uid)
	return ok
}
