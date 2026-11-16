package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/egerkuzma/emtds/internal/config"
)

// aiError is an error that already knows its HTTP answer: status, stable code and text.
// The text starts with "<code>: ", so errCode recovers the code from the message alone.
// Params are the values a client puts into its own text for the code (for example the links of an
// ai_has_links refusal); the answer carries them as "params" and omits the field when there are
// none. The text stays English and is only the fallback of a client that has no text for the code.
type aiError struct {
	status int
	code   string
	msg    string
	Params map[string]any
}

// Error returns the client message.
func (e *aiError) Error() string { return e.msg }

// newAIError builds an aiError for a callback that must stop a write with a chosen answer.
// status is the HTTP status, code the stable machine-readable code (ai_* or keitaro_running),
// text the human part. The message becomes "<code>: <text>".
func newAIError(status int, code, text string) *aiError {
	return &aiError{status: status, code: code, msg: code + ": " + text}
}

// newAIErrorP is newAIError with params for the client: see aiError.Params.
// status, code and text are as in newAIError and params may be nil.
func newAIErrorP(status int, code, text string, params map[string]any) *aiError {
	e := newAIError(status, code, text)
	e.Params = params
	return e
}

// Reasons of an ai_bad_request error, the "reason" param next to the "field" param.
const (
	// aiReasonInvalid: the value is malformed, or the body is not JSON.
	aiReasonInvalid = "invalid"
	// aiReasonRequired: the value is missing or empty.
	aiReasonRequired = "required"
	// aiReasonTooLong: the text is longer than the limit.
	aiReasonTooLong = "too_long"
	// aiReasonTooMany: the list or the map holds more entries than the limit.
	aiReasonTooMany = "too_many"
	// aiReasonBadPrice: a price entry has a bad model id or a price outside its range.
	aiReasonBadPrice = "bad_price"
	// aiReasonUnknownProvider: the provider is not one of the supported ones.
	aiReasonUnknownProvider = "unknown_provider"
	// aiReasonWrongProvider: the key has the format of another provider, named by the "provider" param.
	aiReasonWrongProvider = "wrong_provider"
)

// aiBadRequest builds the 400 ai_bad_request error of a plain validation failure.
// field is the JSON field of the request body or query that failed, reason one of the
// aiReason* values and text the English fallback. The params are field and reason.
func aiBadRequest(field, reason, text string) *aiError {
	return newAIErrorP(http.StatusBadRequest, aiErrBadRequest, text, map[string]any{"field": field, "reason": reason})
}

// aiBadJSON is the ai_bad_request error for a body that is not JSON: field body, reason invalid.
func aiBadJSON() *aiError {
	return aiBadRequest("body", aiReasonInvalid, "bad json")
}

// aiBadProvider is the ai_bad_request error for the provider named by the JSON or query field
// field that is not a supported one: reason unknown_provider.
func aiBadProvider(field string) *aiError {
	return aiBadRequest(field, aiReasonUnknownProvider, "provider must be anthropic, openai, openrouter or gemini")
}

var (
	// errAIStale means the record changed after the proposal was made (409 ai_stale).
	errAIStale = newAIError(http.StatusConflict, aiErrStale, "the record changed after the proposal was made")
	// errAIConflict means the record state does not match the expectation: it exists when it
	// must be absent, is gone when it must exist, or the expectation names no hash (409 ai_conflict).
	errAIConflict = newAIErrorP(http.StatusConflict, aiErrConflict, "the record state does not match the expectation",
		map[string]any{"reason": aiReasonChanged})
	// errKeitaroRunning means a Keitaro import, purge or sync is running (409 keitaro_running).
	errKeitaroRunning = newAIError(http.StatusConflict, aiErrKeitaroRunning, "a Keitaro import, purge or sync is running")
	// errKeitaroWriting is what an import, purge or sync gets while a write holds the gate (409 keitaro_running).
	errKeitaroWriting = newAIError(http.StatusConflict, aiErrKeitaroRunning, "an assistant write is in progress")
	// errUpdatePending means an update is pending and the Keitaro gate is locked (409 update_pending).
	errUpdatePending = newAIError(http.StatusConflict, "update_pending", "an update is pending")
)

// aiMsgCodeRe matches the "<code>: " prefix that newAIError puts on a message.
var aiMsgCodeRe = regexp.MustCompile(`^(ai_[a-z_]+|keitaro_running|update_pending): `)

// aiCodeOf returns the stable code of a message made by newAIError, or "" for any other text.
// msg is the client message. errCode calls it first, so such a message keeps its code
// whatever the HTTP status.
func aiCodeOf(msg string) string {
	if m := aiMsgCodeRe.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	return ""
}

// conflictMsg returns the ai_conflict message extended with detail, or the bare message for "".
func conflictMsg(detail string) string {
	if detail == "" {
		return errAIConflict.msg
	}
	return errAIConflict.msg + " (" + detail + ")"
}

// aiFail turns an error returned by a precondition or a callback into the HTTP answer.
// An aiError keeps its status and message. Any other error becomes 500 with a fixed text,
// and only the Go type of the error is logged: the text may carry record data.
// err is the error. It returns the status and the client message for writeErr.
func (s *Server) aiFail(err error) (int, string) {
	var ae *aiError
	if errors.As(err, &ae) {
		return ae.status, ae.msg
	}
	s.logWarn("write precondition failed", "error_type", fmt.Sprintf("%T", err))
	return http.StatusInternalServerError, "write precondition failed"
}

// failWrite writes the answer for err returned by a precondition or a callback.
// w receives the status and message of aiFail, and the params of an aiError when it has any.
func (s *Server) failWrite(w http.ResponseWriter, err error) {
	code, msg := s.aiFail(err)
	var params map[string]any
	var ae *aiError
	if errors.As(err, &ae) {
		params = ae.Params
	}
	writeErrParams(w, code, msg, params)
}

// writeMissing answers a PUT or DELETE of a record that does not exist.
// exp is the precondition of the request, or nil. An expectation that wanted the record
// (anything but Absent) is answered 409 ai_conflict; otherwise the answer is 404 "not found".
func (s *Server) writeMissing(w http.ResponseWriter, exp *aiExpect) {
	if err := exp.check(nil, false); err != nil {
		s.failWrite(w, err)
		return
	}
	writeErr(w, http.StatusNotFound, "not found")
}

// aiExpect is the precondition of one write, carried in the request context.
// The handler checks it under its own write lock, so the caller takes no outer lock.
//
// Lock order of the admin writers: seq.mu, then hooks.mu, then the store lock for a
// catalog; groupsMu, then the store lock for campaigns. JSONStore.lock is a plain
// Mutex, so a callback must not take groupsMu, seq.mu or another store's lock; it may
// call List. A caller that needs "no links, then delete" as one step takes groupsMu
// itself before the inner DELETE and checks the links there, not in a callback.
//
// The hash is always recordHash of the stored record: never a hash of the file bytes
// and never of the masked view a user sees.
//
// A write with an aiExpect must say what it expects: Absent for a record that must not
// exist yet, otherwise a non-empty Hash. An aiExpect with neither is ai_conflict.
type aiExpect struct {
	// Hash is the recordHash the stored record must have. A mismatch is errAIStale.
	// With no stored record, or with an empty Hash and no Absent, the write is errAIConflict.
	Hash string
	// Absent requires that no record with the id exists; an existing one is errAIConflict.
	// It is the only mode that needs no Hash.
	Absent bool
	// OnBefore runs under the write lock with the record the write is about, after every
	// check and right before the save. A returned error aborts the write; nothing is saved.
	// For an update or delete the record is the stored one; for a restore it is the record
	// to restore. It is not called on a create, which has no such record.
	OnBefore func(raw json.RawMessage) error
	// OnAfter runs under the write lock with the record as saved, right after the save.
	// A delete does not call it.
	OnAfter func(raw json.RawMessage)

	// refused is the last refusal of check or before. The caller that started the write reads it
	// with refusal after the handler answered, because the answer holds the message but not the
	// params.
	refused error
}

// aiExpectKey is the context key of the aiExpect value.
type aiExpectKey struct{}

// withAIExpect returns ctx carrying e. A nil e returns ctx unchanged.
func withAIExpect(ctx context.Context, e *aiExpect) context.Context {
	if e == nil {
		return ctx
	}
	return context.WithValue(ctx, aiExpectKey{}, e)
}

// aiExpectFrom returns the precondition stored in ctx, or nil when none is set.
func aiExpectFrom(ctx context.Context) *aiExpect {
	e, _ := ctx.Value(aiExpectKey{}).(*aiExpect)
	return e
}

// rawHash returns the sha256 (hex) of the canonical form of raw JSON.
// The value is decoded and encoded again with sorted object keys and numbers kept as
// written, so field order and whitespace do not change the hash.
// It returns "" when raw is not valid JSON.
func rawHash(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return ""
	}
	canon, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// recordHash returns the hash that identifies one stored record: rawHash of its JSON.
// v is the record as the store holds it (a config struct), not a masked or reshaped copy.
// It returns "" when v cannot be encoded.
func recordHash(v any) string {
	return rawHash(aiRaw(v))
}

// aiRaw marshals one stored record. A record that cannot be encoded gives nil.
func aiRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// check compares the expectation with the stored record.
// cur is the stored record, ignored when exists is false. A nil receiver accepts everything.
// Absent accepts a missing record and rejects an existing one. Any other expectation
// rejects a missing record and an empty Hash with errAIConflict, and a different record
// with errAIStale. It returns nil when the write may go on.
func (e *aiExpect) check(cur any, exists bool) error {
	if e == nil {
		return nil
	}
	err := e.verdict(cur, exists)
	if err != nil {
		e.refused = err
	}
	return err
}

// refusal returns the error that check or before last returned, or nil. A nil receiver has none.
func (e *aiExpect) refusal() error {
	if e == nil {
		return nil
	}
	return e.refused
}

// verdict is the comparison of check: nil, errAIStale or errAIConflict.
func (e *aiExpect) verdict(cur any, exists bool) error {
	if e.Absent {
		if exists {
			return errAIConflict
		}
		return nil
	}
	if e.Hash == "" || !exists {
		return errAIConflict
	}
	if recordHash(cur) != e.Hash {
		return errAIStale
	}
	return nil
}

// before calls OnBefore with rec. A nil receiver or a nil callback returns nil.
func (e *aiExpect) before(rec any) error {
	if e == nil || e.OnBefore == nil {
		return nil
	}
	err := e.OnBefore(aiRaw(rec))
	if err != nil {
		e.refused = err
	}
	return err
}

// after calls OnAfter with the saved record. A nil receiver or a nil callback does nothing.
func (e *aiExpect) after(saved any) {
	if e == nil || e.OnAfter == nil {
		return
	}
	e.OnAfter(aiRaw(saved))
}

// canRestoreOnBoard reports whether the viewer in ctx may put a record into board.
// v is the viewer of ctx. The section must be visible to v and openable by the session.
func (s *Server) canRestoreOnBoard(ctx context.Context, v viewer, board string) bool {
	if !v.seesBoard(board) {
		return false
	}
	if v.admin {
		return true
	}
	meta, err := s.boardMeta(ctx, sessionFrom(ctx))
	return err == nil && meta.canOpen(config.NormalizeBoardID(board))
}

// restoreBoardCheck refuses a restore into a section that no longer exists.
// board is the section id of the snapshot; the main section always exists.
// It returns 0 and "" when the section is there, 409 ai_conflict when it was deleted,
// and 500 when boards.json cannot be read.
func (s *Server) restoreBoardCheck(ctx context.Context, board string) (int, string) {
	id := config.NormalizeBoardID(board)
	if id == config.MainBoardID {
		return 0, ""
	}
	boards, err := listBoards(ctx, s.cfg.Boards)
	if err != nil {
		return http.StatusInternalServerError, "read error"
	}
	for _, b := range boards {
		if config.SameBoard(b.ID, id) {
			return 0, ""
		}
	}
	return http.StatusConflict, conflictMsg("the section was deleted")
}
