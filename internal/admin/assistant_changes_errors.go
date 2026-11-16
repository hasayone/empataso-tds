package admin

import (
	"errors"
	"net/http"
	"strings"
)

// Reasons of an ai_conflict refusal, sent as params.reason.
const (
	// aiReasonChanged marks a record that is not the one the assistant saw or left.
	aiReasonChanged = "changed"
	// aiReasonIDTaken marks an id that a record holds (params.id).
	aiReasonIDTaken = "id_taken"
	// aiReasonSlugTaken marks a campaign id or alias that another campaign uses (params.slug).
	aiReasonSlugTaken = "slug_taken"
	// aiReasonLinksChanged marks records that link to the record now (params.links), or a
	// restored record whose link points at a record that is gone.
	aiReasonLinksChanged = "links_changed"
	// aiReasonBoardDeleted marks a record whose section was deleted.
	aiReasonBoardDeleted = "board_deleted"
	// aiReasonLandingDir marks a landing directory that holds files or is not a directory.
	aiReasonLandingDir = "landing_dir_exists"
	// aiReasonState marks a change that is not in the state the operation needs (params.status).
	aiReasonState = "state"
	// aiReasonSnapshot marks a landing snapshot that is missing or damaged.
	aiReasonSnapshot = "snapshot_missing"
	// aiReasonRestore marks landing files that could not be written back.
	aiReasonRestore = "restore_failed"
)

// Reasons of an ai_bad_patch refusal, sent as params.reason.
const (
	// aiReasonServerField marks a field the server sets (params.field).
	aiReasonServerField = "server_field"
	// aiReasonNotEditable marks a field outside the white list of the resource (params.field and
	// params.resource).
	aiReasonNotEditable = "not_editable"
	// aiReasonHiddenField marks a campaign key the assistant may not set (params.field).
	aiReasonHiddenField = "hidden_field"
	// aiReasonFetchRedirect marks a redirect mode that fetches a page on the server (params.field).
	aiReasonFetchRedirect = "fetch_redirect"
	// aiReasonNoChange marks a patch that changes nothing.
	aiReasonNoChange = "no_change"
	// aiReasonStreamDropped marks a patch that drops a stream with settings the model cannot see
	// (params.stream and params.fields).
	aiReasonStreamDropped = "stream_dropped"
)

// Reasons of an ai_landing_too_large refusal, sent as params.reason.
const (
	aiReasonSize    = "size"
	aiReasonFiles   = "files"
	aiReasonSymlink = "symlink"
)

// aiParams builds the params of an error from key and value pairs. A trailing key without a value
// is ignored. It returns nil for no pairs.
func aiParams(kv ...any) map[string]any {
	if len(kv) < 2 {
		return nil
	}
	out := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if key, ok := kv[i].(string); ok {
			out[key] = kv[i+1]
		}
	}
	return out
}

// aiChgConflict returns the 409 ai_conflict error with detail added to the standard text.
// reason is one of the aiReason* values of a conflict and kv are more params as key and value
// pairs (id, slug, status, links).
func aiChgConflict(reason, detail string, kv ...any) *aiError {
	return &aiError{status: http.StatusConflict, code: aiErrConflict, msg: conflictMsg(detail),
		Params: aiParams(append([]any{"reason", reason}, kv...)...)}
}

// aiBadPatchR returns the 400 ai_bad_patch error with text and params that start with reason (one
// of the aiReason* values of a patch) followed by kv as key and value pairs.
func aiBadPatchR(reason, text string, kv ...any) *aiError {
	return newAIErrorP(http.StatusBadRequest, aiErrBadPatch, text, aiParams(append([]any{"reason", reason}, kv...)...))
}

// aiChgConfirmRequired returns the 400 ai_confirm_required error for the confirmation need, "delete"
// or "host", that the apply did not carry.
func aiChgConfirmRequired(need, text string) *aiError {
	return newAIErrorP(http.StatusBadRequest, aiErrConfirmRequired, text, map[string]any{"need": need})
}

// aiConflictParams returns the params of an ai_conflict message that an internal handler wrote as
// text. msg is the message, id the record the write was about (set on an id_taken answer when not
// empty) and fallback the reason of a message that is none of the standard ones. It returns nil for
// a message that is not an ai_conflict one.
func aiConflictParams(msg, id, fallback string) map[string]any {
	if aiCodeOf(msg) != aiErrConflict {
		return nil
	}
	detail, ok := strings.CutPrefix(msg, errAIConflict.msg)
	switch {
	case ok && detail == "":
		return aiParams("reason", aiReasonChanged)
	case ok && strings.HasPrefix(detail, " (alias is taken: ") && strings.HasSuffix(detail, ")"):
		return aiParams("reason", aiReasonSlugTaken, "slug", strings.TrimSuffix(strings.TrimPrefix(detail, " (alias is taken: "), ")"))
	case ok && (detail == " (id already exists)" || detail == " (campaign id is taken)"):
		if id == "" {
			return aiParams("reason", aiReasonIDTaken)
		}
		return aiParams("reason", aiReasonIDTaken, "id", id)
	case ok && detail == " (the section was deleted)":
		return aiParams("reason", aiReasonBoardDeleted)
	}
	return aiParams("reason", fallback)
}

// aiChgWithParams returns a copy of e with the params a conflict message implies, when e is an
// ai_conflict error that carries none (see aiConflictParams). Any other error is returned as it is.
// e is not changed, since refusals such as errAIConflict are shared.
func aiChgWithParams(e *aiError, id, fallback string) *aiError {
	if e == nil || e.Params != nil || (e.code != aiErrConflict && aiCodeOf(e.msg) != aiErrConflict) {
		return e
	}
	out := *e
	out.Params = aiConflictParams(e.msg, id, fallback)
	return &out
}

// aiErrInfo returns the code and the params of err for a card or a report: the code of an aiError
// (or the one in its text), else the code errCode gives its status; an error that is not an aiError
// is "internal" without params.
func aiErrInfo(err error) (string, map[string]any) {
	var ae *aiError
	if !errors.As(err, &ae) {
		return "internal", nil
	}
	if ae.code != "" {
		return ae.code, ae.Params
	}
	return errCode(ae.status, ae.msg), ae.Params
}

// aiChgFailure returns the error of a refused internal write. exp is the precondition of the write,
// whose own refusal (with its params) wins over the text of the handler's answer; id is the record
// the write was about and fallback the reason of an ai_conflict message that has none of its own.
// status and body are the answer of the handler and secrets the list that is cut out of its text.
// A create that met an existing record is ai_conflict with reason id_taken.
func (s *Server) aiChgFailure(exp *aiExpect, id string, status int, body []byte, secrets []string, fallback string) *aiError {
	var ae *aiError
	if errors.As(exp.refusal(), &ae) {
		out := *ae
		out.msg = aiRedactText(ae.msg, secrets)
		if ae == errAIConflict && exp != nil && exp.Absent {
			out.Params = aiParams("reason", aiReasonIDTaken, "id", id)
		}
		return aiChgWithParams(&out, id, fallback)
	}
	return aiChgWithParams(s.aiChgAnswerError(status, body, secrets), id, fallback)
}
