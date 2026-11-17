package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

// aiChgCall is one internal write: the route, the id on its path, the body, the section header,
// the precondition and whether the dependents of the record are checked under groupsMu.
type aiChgCall struct {
	pattern string
	id      string
	board   string
	body    []byte
	exp     *aiExpect
	// lockGroups takes groupsMu around the dependents check and the write, so that "no record
	// links to it" and "delete it" are one step (see the lock order of aiExpect).
	lockGroups bool
}

// aiChgGate runs fn while no Keitaro import, purge or sync can start. It returns
// errKeitaroRunning (409 keitaro_running) without calling fn when one is running.
func (s *Server) aiChgGate(fn func() error) error {
	if s.ktJob == nil {
		return fn()
	}
	return s.ktJob.withWrite(fn)
}

// aiChgNoLinks returns an OnBefore callback that refuses the write while a record links to the
// record id of resource. It runs under the write lock of the handler (groupsMu for a campaign),
// reads the lists with List only, and returns refuse(links) as the error. A list that cannot be
// read is a 500 error. secrets is the list of the request, used to redact the names of the links.
func (s *Server) aiChgNoLinks(resource, id string, secrets []string, refuse func([]aiLinkView) *aiError) func(json.RawMessage) error {
	return func(json.RawMessage) error {
		links, err := s.aiChgDeps(context.Background(), resource, id, secrets)
		if err != nil {
			return err
		}
		if len(links) > 0 {
			return refuse(links)
		}
		return nil
	}
}

// aiChgActor checks that actor is a live, allowed session of the assistant.
// It returns 401 for a user that is gone, disabled or has a new password, and 403 for a session
// outside the beta.
func (s *Server) aiChgActor(actor security.Session) error {
	if !s.aiSessionValid(actor) {
		return aiPlainError(http.StatusUnauthorized, "unauthorized")
	}
	if !s.aiAllowed(actor) {
		return aiPlainError(http.StatusForbidden, "forbidden")
	}
	return nil
}

// aiChgCallInner sends one write through the same chain a panel request takes (boardScope, guard,
// requireAction, the handler) with the session of actor, the section header of c and the
// precondition of c in the context. The write is not cut short when ctx ends: a started write
// is finished. It returns the status and the body of the answer, or an error when the route is
// missing or the handler panics.
func (s *Server) aiChgCallInner(ctx context.Context, actor security.Session, c aiChgCall) (int, []byte, error) {
	h := s.inner[c.pattern]
	if h == nil {
		return 0, nil, aiPlainError(http.StatusInternalServerError, "route unavailable")
	}
	method, path, _ := strings.Cut(c.pattern, " ")
	path = strings.Replace(path, "{id}", url.PathEscape(c.id), 1)
	var body io.Reader = http.NoBody
	if c.body != nil {
		body = bytes.NewReader(c.body)
	}
	rctx := withAIExpect(context.WithValue(context.WithoutCancel(ctx), sessionKey, actor), c.exp)
	req, err := http.NewRequestWithContext(rctx, method, path, body)
	if err != nil {
		return 0, nil, aiPlainError(http.StatusInternalServerError, "bad request")
	}
	if strings.Contains(c.pattern, "{id}") {
		req.SetPathValue("id", c.id)
	}
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.board != "" {
		req.Header.Set(boardHeader, c.board)
	}
	w := newAIBoundedWriter(aiChangeAnswerMax)
	if err := s.aiServe(h, w, req, "change"); err != nil {
		return 0, nil, aiPlainError(http.StatusInternalServerError, "the write failed")
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	return status, w.buf.Bytes(), nil
}

// aiChgAnswerError turns the non-2xx answer of an internal write into the error the caller
// returns. The status and the code are the handler's own; the text is its message with the
// secrets cut out, because it may echo a value of the record. secrets is the list of the request.
func (s *Server) aiChgAnswerError(status int, body []byte, secrets []string) *aiError {
	var e struct {
		Error  string         `json:"error"`
		Code   string         `json:"code"`
		Params map[string]any `json:"params"`
	}
	_ = json.Unmarshal(body, &e)
	msg := aiRedactText(e.Error, secrets)
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &aiError{status: status, code: e.Code, msg: msg, Params: e.Params}
}

// aiChgRevertFailure is aiChgFailure for a revert. A stale hash means the record is no longer the
// one the assistant left, so it becomes 409 ai_conflict with reason changed and the panel can say
// it was changed by hand. Every other refusal, a failed validation included, keeps its real
// status, code, params and (redacted) text.
func (s *Server) aiChgRevertFailure(exp *aiExpect, id string, status int, body []byte, secrets []string, fallback string) *aiError {
	e := s.aiChgFailure(exp, id, status, body, secrets, fallback)
	if e.status == http.StatusConflict && (e.code == aiErrStale || aiCodeOf(e.msg) == aiErrStale) {
		return aiChgConflict(aiReasonChanged, "the record was changed after the assistant's edit")
	}
	return e
}

// aiChgAbort marks the entry aborted with the reason err (its text, code and params), drops its snapshot and its raw copy, and
// returns err so the caller can return it. A failure to save the mark is logged only.
func (s *Server) aiChgAbort(id string, err error) error {
	var blob string
	if _, perr := s.aiChgPatch(id, func(r *aiChangeRec) error {
		blob = r.Change.Blob
		r.Change.Status = aiChangeAborted
		r.Change.Error = err.Error()
		r.ErrorCode, r.ErrorParams = aiErrInfo(err)
		r.Change.Before = nil
		r.Change.Blob = ""
		return nil
	}); perr != nil {
		s.logWarn("assistant change not marked aborted")
	}
	s.aiChgRemoveBlob(blob)
	return err
}

// aiChgAudit records an applied or reverted change in the audit file with source "assistant".
// route is aiRouteApply or aiRouteRevert, uid the user of the change and start the time the
// operation began.
func (s *Server) aiChgAudit(route, uid, changeID string, start time.Time) {
	s.recordAudit(start, auditEntry{
		TS: start.UTC().Format(time.RFC3339Nano), Method: http.MethodPost, Route: route,
		Status: http.StatusOK, MS: time.Since(start).Milliseconds(), UID: uid, Source: "assistant", ChangeID: changeID,
	})
}

// aiChgIDOf returns the "id" of a stored record given as raw JSON, or "" when it has none.
func aiChgIDOf(raw json.RawMessage) string {
	var v struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.ID
}

// applyChange writes a proposal the user confirmed. It does not depend on an HTTP request.
//
// actor is the session of the user and id the change. Only the author, only while the proposal is
// proposed, may apply. A delete needs opts.ConfirmDelete and a proposal that brings a new URL host
// needs opts.ConfirmHost, else 400 ai_confirm_required. The whole apply runs under the Keitaro write
// gate, so an import, purge or sync in progress is 409 keitaro_running.
//
// The record is built only from the stored proposal and the stored record: the stored hash must
// still match (409 ai_stale), the entry becomes pending with a durable copy of the record before
// the write, and the write goes through the internal handler of the resource with the session of
// actor, the section of the proposal and an aiExpect: hash for update and delete, absent for create.
// A delete of a catalog record holds groupsMu around the check "no campaign or offer links to it"
// and the delete (409 ai_has_links), and a delete of a landing archives its files first; a failed
// archive stops the delete. The entry becomes done only when the write was confirmed (OnAfter for
// update and create, a 2xx answer for delete), else aborted with the reason. Every apply is
// written to the audit with source "assistant".
//
// It returns the card, or an error: 401 and 403 for the actor, 403 ai_forbidden when the assistant
// permissions no longer allow the operation, 404 for another user's change, 409 for a state that
// does not allow it, and the answer of the failed write.
func (s *Server) applyChange(ctx context.Context, actor security.Session, id string, opts aiApplyOpts) (aiChangeView, error) {
	if err := s.aiChgActor(actor); err != nil {
		return aiChangeView{}, err
	}
	ctx = s.aiWithSecrets(ctx)
	s.ai.applyMu.Lock()
	defer s.ai.applyMu.Unlock()
	var out aiChangeView
	err := s.aiChgGate(func() error {
		v, err := s.aiChgApplyHeld(ctx, actor, id, opts)
		out = v
		return err
	})
	return out, err
}

// aiApplyPlan is what one apply writes and what its callbacks learn on the way. call is the
// internal write, blob the name of the landing snapshot made before a delete, blocked the
// dependents the write found under its lock, afterRaw the record as the catalog saved it.
type aiApplyPlan struct {
	call     aiChgCall
	blob     string
	blocked  []aiLinkView
	afterRaw json.RawMessage
}

// aiChgApplyHeld is applyChange with applyMu and the write gate held.
func (s *Server) aiChgApplyHeld(ctx context.Context, actor security.Session, id string, opts aiApplyOpts) (aiChangeView, error) {
	start := time.Now()
	uid := s.aiUsageUID(actor)
	rec, err := s.aiChgOwn(id, uid)
	if err != nil {
		return aiChangeView{}, err
	}
	spec, err := aiChgCheckApply(rec, opts)
	if err != nil {
		return aiChangeView{}, err
	}
	p := rec.Proposal
	if err := s.aiPermCheck(p.Resource, p.Op); err != nil {
		return aiChangeView{}, err
	}
	if _, err := s.aiChgScope(ctx, actor, p.Board); err != nil {
		return aiChangeView{}, err
	}
	raw, exists, err := s.aiChgStored(ctx, p.Resource, p.EntityID)
	if err != nil {
		return aiChangeView{}, err
	}
	plan, err := s.aiChgPlanApply(ctx, spec, rec, raw, exists)
	if err != nil {
		return aiChangeView{}, err
	}
	if plan.call.lockGroups {
		release, err := s.aiChgHoldGroups(ctx, rec)
		if err != nil {
			return aiChangeView{}, err
		}
		defer release()
	}
	done, err := s.aiChgRunApply(ctx, actor, rec, raw, plan)
	if err != nil {
		return aiChangeView{}, err
	}
	s.aiChgAudit(aiRouteApply, uid, p.ID, start)
	return done.view(), nil
}

// aiChgCheckApply checks that the entry waits for confirmation, that its resource is known and
// that the user gave the confirmations the card asked for. It returns the table row of the
// resource, 409 ai_conflict for a change that is not proposed, 400 ai_bad_patch for an unknown
// resource, or 400 ai_confirm_required for a missing confirmation.
func aiChgCheckApply(rec aiChangeRec, opts aiApplyOpts) (aiChgResource, error) {
	if rec.Change.Status != aiChangeProposed {
		return aiChgResource{}, aiChgConflict(aiReasonState, "the change is not waiting for confirmation (status "+rec.Change.Status+")",
			"status", rec.Change.Status)
	}
	spec, ok := aiChgResources[rec.Proposal.Resource]
	if !ok {
		return aiChgResource{}, aiBadPatch("unknown resource")
	}
	if rec.Proposal.Op == aiOpDelete && !opts.ConfirmDelete {
		return aiChgResource{}, aiChgConfirmRequired("delete", "a delete needs confirm_delete")
	}
	if rec.ExternalHost && !opts.ConfirmHost {
		return aiChgResource{}, aiChgConfirmRequired("host", "a new external address needs confirm_host")
	}
	return spec, nil
}

// aiChgPlanApply builds the write of a proposal from the stored record raw (exists tells whether
// there is one). A create must not meet a record with its id; an update or delete needs the record
// with the hash the proposal saw. The plan carries the route, the precondition and, for a create
// or update, the body and the callback that marks the entry done. It returns 409 ai_conflict or
// ai_stale for a record that moved, 400 ai_bad_patch for an unknown operation or a body that
// fails its checks, or the error of aiChgPlanDelete.
func (s *Server) aiChgPlanApply(ctx context.Context, spec aiChgResource, rec aiChangeRec, raw json.RawMessage, exists bool) (*aiApplyPlan, error) {
	p := rec.Proposal
	plan := &aiApplyPlan{call: aiChgCall{board: p.Board, id: p.EntityID, exp: &aiExpect{}}}
	switch p.Op {
	case aiOpCreate:
		if p.EntityID != "" && exists {
			return nil, aiChgConflict(aiReasonIDTaken, "the id already exists", "id", p.EntityID)
		}
		plan.call.exp.Absent = true
		plan.call.pattern = spec.createPattern()
	case aiOpUpdate, aiOpDelete:
		if !exists {
			return nil, errAIConflict
		}
		if rawHash(raw) != p.BeforeHash {
			return nil, errAIStale
		}
		plan.call.exp.Hash = p.BeforeHash
		plan.call.pattern = spec.put()
		if p.Op == aiOpDelete {
			if err := s.aiChgPlanDelete(ctx, spec, rec, plan); err != nil {
				return nil, err
			}
		}
	default:
		return nil, aiBadPatch("unknown operation")
	}
	if p.Op != aiOpDelete {
		body, err := s.aiChgBody(spec, p, raw, s.aiSecretsFrom(ctx))
		if err != nil {
			return nil, err
		}
		plan.call.body = body
		plan.call.exp.OnAfter = s.aiChgOnSaved(p.ID, plan)
	}
	return plan, nil
}

// aiChgPlanDelete sets the route and the checks of a delete on plan. A catalog delete is run with
// the dependents check under groupsMu (lockGroups); a campaign delete repeats the check in OnBefore
// and records what blocked it in plan.blocked. A landing is archived first, and a failed archive
// stops the delete. It returns 409 ai_has_links when records use the target now, or the error of
// the dependents lookup.
func (s *Server) aiChgPlanDelete(ctx context.Context, spec aiChgResource, rec aiChangeRec, plan *aiApplyPlan) error {
	p := rec.Proposal
	plan.call.pattern = spec.del()
	plan.call.lockGroups = spec.isCatalog()
	if !spec.isCatalog() {
		plan.call.exp.OnBefore = s.aiChgNoLinks(p.Resource, p.EntityID, s.aiSecretsFrom(ctx), func(links []aiLinkView) *aiError {
			plan.blocked = links
			return aiChgHasLinks(rec, links).aiError
		})
		links, err := s.aiChgDeps(ctx, p.Resource, p.EntityID, s.aiSecretsFrom(ctx))
		if err != nil {
			return err
		}
		if len(links) > 0 {
			return aiChgHasLinks(rec, links)
		}
	}
	if spec.name == "landings" {
		plan.blob = p.ID + ".tar.gz"
		path := s.aiBlobPath(plan.blob)
		plan.call.exp.OnBefore = func(json.RawMessage) error { return s.aiLandingSnapshot(p.EntityID, path) }
	}
	return nil
}

// aiChgOnSaved returns the OnAfter callback of the write of entry id: it keeps the saved record in
// plan.afterRaw and marks the entry done at once, so the entry is settled even when the answer is
// lost. A failure to save the mark is logged only.
func (s *Server) aiChgOnSaved(id string, plan *aiApplyPlan) func(json.RawMessage) {
	return func(saved json.RawMessage) {
		plan.afterRaw = saved
		if _, err := s.aiChgPatch(id, func(r *aiChangeRec) error { aiChgMarkDone(r, saved); return nil }); err != nil {
			s.logWarn("assistant change not marked done")
		}
	}
}

// aiChgHoldGroups takes groupsMu and checks that no record links to the target of the delete in
// rec, so that "no record links to it" and "delete it" are one step. It returns the function that
// releases the lock, 409 ai_has_links with the lock already released, or the error of the lookup.
func (s *Server) aiChgHoldGroups(ctx context.Context, rec aiChangeRec) (func(), error) {
	s.groupsMu.Lock()
	links, err := s.aiChgDeps(ctx, rec.Proposal.Resource, rec.Proposal.EntityID, s.aiSecretsFrom(ctx))
	if err == nil && len(links) > 0 {
		err = aiChgHasLinks(rec, links)
	}
	if err != nil {
		s.groupsMu.Unlock()
		return nil, err
	}
	return s.groupsMu.Unlock, nil
}

// aiChgRunApply marks the entry pending with a durable copy of the stored record raw, runs the
// write of plan and marks the entry done when the write was confirmed. A failed write, a refused
// one or one nobody confirmed aborts the entry with the reason (see aiChgAbort). It returns the
// entry as saved, or the error of the write.
func (s *Server) aiChgRunApply(ctx context.Context, actor security.Session, rec aiChangeRec, raw json.RawMessage, plan *aiApplyPlan) (aiChangeRec, error) {
	p := rec.Proposal
	if _, err := s.aiChgPatch(p.ID, func(r *aiChangeRec) error {
		if r.Change.Status != aiChangeProposed {
			return aiChgConflict(aiReasonState, "the change is not waiting for confirmation", "status", r.Change.Status)
		}
		r.Change.Status = aiChangePending
		r.Change.Before = raw
		r.Change.Blob = plan.blob
		r.Change.Error = ""
		return nil
	}); err != nil {
		return aiChangeRec{}, err
	}
	status, answer, err := s.aiChgCallInner(ctx, actor, plan.call)
	if err != nil {
		return aiChangeRec{}, s.aiChgAbort(p.ID, err)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		if plan.blocked != nil {
			return aiChangeRec{}, s.aiChgAbort(p.ID, aiChgHasLinks(rec, plan.blocked))
		}
		return aiChangeRec{}, s.aiChgAbort(p.ID, s.aiChgFailure(plan.call.exp, p.EntityID, status, answer, s.aiSecretsFrom(ctx), aiReasonChanged))
	}
	if p.Op != aiOpDelete && plan.afterRaw == nil {
		return aiChangeRec{}, s.aiChgAbort(p.ID, aiChgConflict(aiReasonChanged, "the write was not confirmed"))
	}
	return s.aiChgPatch(p.ID, func(r *aiChangeRec) error { aiChgMarkDone(r, plan.afterRaw); return nil })
}

// aiChgMarkDone sets the entry to done. saved is the record as written, or nil for a delete.
// It stores the hash of the saved record and, for a create, the id the catalog gave it.
// Calling it again with the same data changes nothing but the time.
func aiChgMarkDone(r *aiChangeRec, saved json.RawMessage) {
	r.Change.Status = aiChangeDone
	r.Change.Error, r.ErrorCode, r.ErrorParams = "", "", nil
	if r.Change.Applied.IsZero() {
		r.Change.Applied = time.Now().UTC()
	}
	if saved == nil {
		return
	}
	r.Change.AfterHash = rawHash(saved)
	if id := aiChgIDOf(saved); id != "" {
		r.Change.EntityID = id
		r.Proposal.EntityID = id
	}
}

// aiChgBody builds the JSON body of an update or create from the stored proposal and the stored
// record raw (nil for a create), with secrets the list of the request. The patch is checked against the white list again, because the
// body must never hold what a proposal may not. For an update, a value the model got in a
// redacted form and sent back as it was takes the stored value (aiChgUnmask); any other mark is
// refused. It returns 400 ai_bad_patch or ai_redacted_value for a patch or a record that fails.
func (s *Server) aiChgBody(spec aiChgResource, p aiProposal, raw json.RawMessage, secrets []string) ([]byte, error) {
	patch, err := aiChgObject(p.Patch)
	if err != nil {
		return nil, aiBadPatch("the stored patch is not valid")
	}
	if err := aiChgCheckKeys(spec, patch); err != nil {
		return nil, err
	}
	var before map[string]any
	if p.Op == aiOpUpdate {
		if before, err = aiChgObject(raw); err != nil {
			return nil, aiBadPatch("the stored record is not valid")
		}
		shown := aiChgShow(newAIRedactor(secrets, false), before, spec.name == "campaigns")
		patch = aiChgAsObject(aiChgUnmask(patch, shown, before))
	}
	if err := aiChgCheckMarks(patch); err != nil {
		return nil, err
	}
	body, err := aiMarshal(aiChgMerge(spec, p.Op, p.EntityID, before, patch))
	if err != nil {
		return nil, aiBadPatch("the record cannot be encoded")
	}
	return body, nil
}

// rejectChange turns a proposal the user declined into rejected. Only the author may, and only
// while the proposal is proposed (409 ai_conflict otherwise). Nothing else is written.
// It returns the card, or 401, 403, 404 or 409.
func (s *Server) rejectChange(_ context.Context, actor security.Session, id string) (aiChangeView, error) {
	if err := s.aiChgActor(actor); err != nil {
		return aiChangeView{}, err
	}
	s.ai.applyMu.Lock()
	defer s.ai.applyMu.Unlock()
	if _, err := s.aiChgOwn(id, s.aiUsageUID(actor)); err != nil {
		return aiChangeView{}, err
	}
	rec, err := s.aiChgPatch(id, func(r *aiChangeRec) error {
		if r.Change.Status != aiChangeProposed {
			return aiChgConflict(aiReasonState, "the change is not waiting for confirmation (status "+r.Change.Status+")",
				"status", r.Change.Status)
		}
		r.Change.Status = aiChangeRejected
		return nil
	})
	if err != nil {
		return aiChangeView{}, err
	}
	return rec.view(), nil
}
