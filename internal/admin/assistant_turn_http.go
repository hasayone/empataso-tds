package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hasayone/empataso-tds/internal/security"
)

// aiMessageIn is a user message that passed every check before the turn: the model, the provider
// client, the text with secrets cut out, the language key of the answer, the page context and the
// section it names. A message that failed a check still carries its lang, for the refusal.
type aiMessageIn struct {
	model   string
	prov    aiProvider
	text    string
	lang    string
	secrets []string
	page    aiPageContext
	board   string
}

// aiParseMessage reads and checks the body of a message request against the settings st.
// It returns 400 ai_bad_request for a body that is not JSON (field body), an empty text (field
// text, reason required) or an over-long one (reason too_long), 400 ai_bad_model for a model
// outside allowed_models, and ai_bad_key when the provider has no key. The section is the
// one of the page context, else the one of the request. The lang of the body is reduced to a
// supported key by aiLangOf; the returned message holds it even with an error.
func (s *Server) aiParseMessage(w http.ResponseWriter, r *http.Request, st aiSettings) (aiMessageIn, error) {
	var body aiMessageBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, aiTurnBodyMax)).Decode(&body); err != nil {
		return aiMessageIn{}, aiBadJSON()
	}
	lang := aiLangOf(body.Lang)
	model, err := aiPickModel(st, body.Model)
	if err != nil {
		return aiMessageIn{lang: lang}, err
	}
	prov, err := s.aiProviderFor(st)
	if err != nil {
		return aiMessageIn{lang: lang}, err
	}
	text := strings.TrimSpace(body.Text)
	switch {
	case text == "":
		return aiMessageIn{lang: lang}, aiBadRequest("text", aiReasonRequired, "text is required")
	case utf8.RuneCountInString(text) > aiUserTextMax:
		return aiMessageIn{lang: lang}, aiBadRequest("text", aiReasonTooLong, "text is too long")
	}
	secrets := s.processSecrets()
	in := aiMessageIn{model: model, prov: prov, text: aiRedactText(text, secrets), lang: lang, secrets: secrets}
	_ = json.Unmarshal(body.Context, &in.page)
	in.board = strings.TrimSpace(in.page.Board)
	if in.board == "" {
		in.board = requestBoard(r)
	}
	return in, nil
}

// aiNewTurn makes the state of a turn of the user sess on chat chatID for the message in, with
// the tools both the user and the assistant permissions of st allow; ctx carries the access of the
// user. It returns a 500 error when no message id can be generated.
func (s *Server) aiNewTurn(ctx context.Context, sess security.Session, uid, chatID string, st aiSettings, in aiMessageIn) (*aiTurn, error) {
	msgID, err := aiChgNewID()
	if err != nil {
		return nil, err
	}
	t := &aiTurn{s: s, sess: sess, uid: uid, chatID: chatID, board: in.board, lang: in.lang, model: in.model, st: st, prov: in.prov,
		secrets: in.secrets, persona: s.aiPersonaBlock(in.secrets), msgID: msgID, tools: map[string]aiTool{}}
	t.priced, t.isPriced = s.aiPriceOf(st, in.model)
	tools := s.aiToolsFor(ctx, sess, in.board, st.Perms)
	for _, tool := range tools {
		t.tools[tool.Name] = tool
	}
	t.specs = aiToolSpecs(tools)
	return t, nil
}

// aiStartStream writes the status and the headers of an event stream that no proxy buffers.
func aiStartStream(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// handleAIMessage answers POST /api/ai/chats/{id}/messages {text, context, model, lang}: one turn of the
// assistant as a server-sent event stream.
//
// Refusals before the stream starts are JSON errors: 403 for a session outside the beta, 406
// without Accept: text/event-stream, 503 ai_disabled when the assistant is off, 400 for a bad
// body, an empty or over-long text, a model outside allowed_models (ai_bad_model) or an input
// that cannot fit turn_input_tokens, 400 ai_bad_key without a key, 404 for a chat that is not the
// caller's, 409 ai_busy when the user already has a turn, 429 ai_busy when the process runs two
// turns, 413 ai_chat_full and 507 ai_disk_full when the history has no room. After them the user
// message is on disk and the chat is running, and from then on every failure is an error event.
//
// The stream starts with ": open", carries ": ping" every 15 s and the events status, tool, text,
// proposal, usage, and done or error. The model never applies a change: a propose call becomes a
// card the user applies later.
func (s *Server) handleAIMessage(w http.ResponseWriter, r *http.Request) {
	if !aiWantsSSE(r) {
		aiWriteErr(w, newAIError(http.StatusNotAcceptable, "ai_not_acceptable", "send Accept: text/event-stream"))
		return
	}
	st, uid, ok := s.aiChatGate(w, r)
	if !ok {
		return
	}
	in, err := s.aiParseMessage(w, r, st)
	if err != nil {
		s.aiWriteErrIn(w, err, in.lang)
		return
	}
	chat, err := s.aiChatGet(uid, r.PathValue("id"))
	if err != nil {
		s.aiWriteErrIn(w, err, in.lang)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.ai.turns.limit())
	defer cancel()
	run, err := s.aiTurnBegin(uid, chat.ID, cancel)
	if err != nil {
		s.aiWriteErrIn(w, err, in.lang)
		return
	}
	defer s.aiTurnEnd(uid, run)

	t, err := s.aiNewTurn(r.Context(), sessionFrom(r.Context()), uid, chat.ID, st, in)
	if err != nil {
		s.aiWriteErrIn(w, err, in.lang)
		return
	}
	msgs, err := s.aiOpenTurn(&chat, t, in.text, aiFitContext(aiRedactContextWith(in.page, in.secrets)))
	if err != nil {
		s.aiWriteErrIn(w, err, in.lang)
		return
	}

	defer func() {
		if !t.settled {
			t.persist(aiChatInterrupted, aiErrInterrupted)
		}
	}()
	aiStartStream(w)
	t.sse = newAISSE(w)
	defer t.sse.finish()
	t.sse.comment("open")
	defer t.sse.heartbeat(s.ai.turns.beat())()
	started := time.Now()
	runErr := t.safeLoop(ctx, msgs)
	t.conclude(ctx, runErr, started)
}
