package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// errParts returns the stable code, the text in the language of the turn and the params of err
// for the error event. An *aiError keeps its code and params, a turn deadline is ai_timeout,
// anything else is a bare internal error whose detail never leaves the server. The text is
// the English one of the error, or the table text of the code in another language.
func (t *aiTurn) errParts(err error) (code, text string, params map[string]any) {
	var ae *aiError
	switch {
	case errors.As(err, &ae):
		code = ae.code
		if code == "" {
			code = aiCodeOf(ae.msg)
		}
		if code == "" {
			code = errCode(ae.status, ae.msg)
		}
		plain := aiRedactText(strings.TrimPrefix(ae.msg, code+": "), t.secrets)
		return code, aiErrText(t.lang, code, plain), aiErrParams(ae)
	case errors.Is(err, errAISessionGone):
		return "unauthorized", aiErrText(t.lang, "unauthorized", "the session is no longer valid"), nil
	case errors.Is(err, context.DeadlineExceeded):
		return aiErrTimeout, aiErrText(t.lang, aiErrTimeout, "the reply took too long"), nil
	}
	return "internal", aiErrText(t.lang, "internal", "the assistant failed"), nil
}

// safeLoop is loop that turns a panic into an error, so a defect fails one turn and the chat is
// still stored and left idle.
func (t *aiTurn) safeLoop(ctx context.Context, msgs []aiTurnMsg) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			t.s.logWarn("assistant turn panicked", "chat_id", t.chatID)
			err = errors.New("ai: the turn panicked")
		}
	}()
	return t.loop(ctx, msgs)
}

// persist stores the reply of the turn and the new status of the chat. code, when set, is kept on
// the reply message. A reply with neither text, proposals nor code is not stored. When the chat
// has no room for the text, the message is stored without it; when that fails too, only the
// status is stored, so the chat never stays "running". A stored status sets t.settled.
func (t *aiTurn) persist(status, code string) {
	text := t.joined()
	for attempt := 0; attempt < 3; attempt++ {
		withMessage := attempt < 2 && (text != "" || code != "" || len(t.proposals) > 0)
		_, err := t.s.aiChatUpdate(t.uid, t.chatID, func(c *aiChat) error {
			if withMessage {
				c.Messages = append(c.Messages, aiMessage{
					ID: t.msgID, Role: aiRoleAssistant, Text: text, Model: t.model, Proposals: t.proposals,
					Tokens: int64(t.in + t.out), Error: code, Created: time.Now().UTC(),
				})
			}
			c.Status = status
			return nil
		})
		if err == nil {
			t.settled = true
			return
		}
		t.s.logWarn("assistant reply not stored", "chat_id", t.chatID, "error_type", fmt.Sprintf("%T", err))
		text = ""
	}
}

// conclude ends the turn: it stores the reply, then sends the closing events and writes the log
// line. The reply is stored before done or error goes out, so a client that reloads the chat when
// the stream ends sees it; the error event names that reply in message_id, so the client shows the
// error once. A user who is gone is not given a chat back, and a client that left or a
// stopping process leaves the chat interrupted. started is the start time of the turn.
func (t *aiTurn) conclude(ctx context.Context, runErr error, started time.Time) {
	status, code, text := aiChatIdle, "", ""
	var params map[string]any
	silent := false
	switch {
	case runErr == nil:
	case errors.Is(runErr, context.Canceled):
		status, code, silent = aiChatInterrupted, aiErrInterrupted, true
	case errors.Is(runErr, errAISessionGone):
		status = aiChatInterrupted
		code, text, params = t.errParts(runErr)
	default:
		code, text, params = t.errParts(runErr)
	}
	if !errors.Is(runErr, errAISessionGone) || t.s.aiUIDExists(t.uid) {
		t.persist(status, code)
	} else {
		t.settled = true
	}
	if t.in+t.out > 0 {
		usage := map[string]any{"input": t.in, "output": t.out}
		if t.isPriced {
			usage["usd"] = aiRoundUSD(t.usd)
		}
		t.sse.event("usage", usage)
	}
	switch {
	case runErr == nil:
		t.sse.event("done", map[string]string{"message_id": t.msgID})
	case !silent:
		payload := map[string]any{"code": code, "error": text, "message_id": t.msgID}
		if len(params) > 0 {
			payload["params"] = params
		}
		t.sse.event("error", payload)
	}
	args := []any{"chat_id", t.chatID, "uid", t.uid, "provider", t.st.Provider, "model", t.model,
		"input_tokens", t.in, "output_tokens", t.out, "code", code, "duration_ms", time.Since(started).Milliseconds()}
	if aiProviderCodes[code] {
		args = append(args, "error", aiRedactText(runErr.Error(), t.secrets))
	}
	t.s.logInfo("assistant turn", args...)
}

// aiProviderCodes are the codes of a failed provider call, whose English text (the HTTP status
// and the provider error code, never the key) goes into the log line of the turn.
var aiProviderCodes = map[string]bool{aiErrBadKey: true, aiErrProvider: true, aiErrRateLimited: true, aiErrTimeout: true}
