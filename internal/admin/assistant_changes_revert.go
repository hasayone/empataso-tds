package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/egerkuzma/emtds/internal/security"
)

// revertChange rolls back an applied change in one step.
//
// Only the author may, only while the change is done (a second revert is 409 ai_conflict), and
// only while no Keitaro import, purge or sync runs (409 keitaro_running). A started revert is not
// cut short when ctx ends. The record must still be the one the assistant left: an update is put
// back with the stored copy under the precondition "hash equals after_hash"; a create is deleted
// under the same precondition, after a check that no record links to it (and, for a landing, that
// it holds no files); a delete of a catalog record is restored with the same id, owner, section
// and number through restoreCatalogByRes, a landing with its files unpacked from the snapshot
// into a directory that is absent or empty; a delete of a campaign is restored through
// restoreGroup, which hands out a new number when its own is taken. A record changed by hand, an
// id, alias or slug that is taken, or a link that is gone is 409 ai_conflict and nothing is
// overwritten; any other refusal keeps its own status, code and redacted text. The entry is
// "reverting" while the write runs, and goes back to "done" when the write is refused. The revert
// is written to the audit.
//
// It returns the card, or the error of the check or the write.
func (s *Server) revertChange(ctx context.Context, actor security.Session, id string) (aiChangeView, error) {
	if err := s.aiChgActor(actor); err != nil {
		return aiChangeView{}, err
	}
	ctx = s.aiWithSecrets(context.WithoutCancel(ctx))
	s.ai.applyMu.Lock()
	defer s.ai.applyMu.Unlock()
	var out aiChangeView
	err := s.aiChgGate(func() error {
		v, err := s.aiChgRevertHeld(ctx, actor, id)
		out = v
		return err
	})
	return out, err
}

// aiChgRevertHeld is revertChange with applyMu and the write gate held.
func (s *Server) aiChgRevertHeld(ctx context.Context, actor security.Session, id string) (aiChangeView, error) {
	start := time.Now()
	uid := s.aiUsageUID(actor)
	rec, err := s.aiChgOwn(id, uid)
	if err != nil {
		return aiChangeView{}, err
	}
	c := rec.Change
	if c.Status != aiChangeDone {
		return aiChangeView{}, aiChgConflict(aiReasonState, "the change cannot be reverted (status "+c.Status+")", "status", c.Status)
	}
	spec, ok := aiChgResources[c.Resource]
	if !ok {
		return aiChangeView{}, aiBadPatch("unknown resource")
	}
	sctx, err := s.aiChgScope(ctx, actor, c.Board)
	if err != nil {
		return aiChangeView{}, err
	}
	if _, err := s.aiChgPatch(id, func(r *aiChangeRec) error {
		if r.Change.Status != aiChangeDone {
			return aiChgConflict(aiReasonState, "the change cannot be reverted (status "+r.Change.Status+")", "status", r.Change.Status)
		}
		r.Change.Status = aiChangeReverting
		return nil
	}); err != nil {
		return aiChangeView{}, err
	}
	renumbered := false
	switch c.Op {
	case aiOpUpdate:
		err = s.aiChgRevertCall(ctx, actor, aiChgCall{
			pattern: spec.put(), id: c.EntityID, board: c.Board, body: c.Before, exp: &aiExpect{Hash: c.AfterHash},
		}, spec)
	case aiOpCreate:
		call := aiChgCall{pattern: spec.del(), id: c.EntityID, board: c.Board, exp: &aiExpect{Hash: c.AfterHash}, lockGroups: spec.isCatalog()}
		switch {
		case spec.name == "landings":
			call.exp.OnBefore = func(json.RawMessage) error { return s.aiLandingMustBeFree(c.EntityID) }
		case !spec.isCatalog():
			call.exp.OnBefore = s.aiChgNoLinks(spec.name, c.EntityID, s.aiSecretsFrom(ctx), aiChgLinksConflict)
		}
		err = s.aiChgRevertCall(ctx, actor, call, spec)
	case aiOpDelete:
		renumbered, err = s.aiChgRestore(sctx, spec, c)
	default:
		err = aiBadPatch("unknown operation")
	}
	if err != nil {
		s.aiChgReopen(id)
		return aiChangeView{}, err
	}
	done, err := s.aiChgPatch(id, func(r *aiChangeRec) error {
		r.Change.Status = aiChangeReverted
		r.Change.Reverted = time.Now().UTC()
		r.Renumbered = renumbered
		return nil
	})
	if err != nil {
		return aiChangeView{}, err
	}
	s.aiChgRemoveBlob(c.Blob)
	s.aiChgAudit(aiRouteRevert, uid, id, start)
	return done.view(), nil
}

// aiChgLinksConflict returns the 409 ai_conflict error of a revert that would delete a record
// others link to now.
func aiChgLinksConflict(links []aiLinkView) *aiError {
	return aiChgConflict(aiReasonLinksChanged, "records already link to it: "+aiChgLinksText(links), "links", links)
}

// aiChgReopen puts an entry whose revert was refused back to done. A failure to save is logged;
// startup recovery settles an entry left reverting.
func (s *Server) aiChgReopen(id string) {
	if _, err := s.aiChgPatch(id, func(r *aiChangeRec) error {
		if r.Change.Status == aiChangeReverting {
			r.Change.Status = aiChangeDone
		}
		return nil
	}); err != nil {
		s.logWarn("assistant change not reopened")
	}
}

// aiChgRevertCall runs the write of a revert that goes through an internal handler (an update put
// back, a create deleted). With lockGroups it first takes groupsMu and refuses, as 409 ai_conflict,
// a record that others link to. A refusal of the handler is mapped by aiChgRevertAnswerError.
func (s *Server) aiChgRevertCall(ctx context.Context, actor security.Session, call aiChgCall, spec aiChgResource) error {
	if call.lockGroups {
		s.groupsMu.Lock()
		defer s.groupsMu.Unlock()
		links, err := s.aiChgDeps(ctx, spec.name, call.id, s.aiSecretsFrom(ctx))
		if err != nil {
			return err
		}
		if len(links) > 0 {
			return aiChgLinksConflict(links)
		}
	}
	status, answer, err := s.aiChgCallInner(ctx, actor, call)
	if err != nil {
		return err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return s.aiChgRevertFailure(call.exp, call.id, status, answer, s.aiSecretsFrom(ctx), aiReasonChanged)
	}
	return nil
}

// aiChgRestore puts a deleted record back from the copy of change c. sctx is the board scope of
// the actor. A catalog record goes through restoreCatalogByRes (a landing also gets its files back
// from the snapshot, and loses them again when the save fails); a campaign goes through
// restoreGroup. It returns whether the campaign got a new number, or the error: the restore's own
// status and code with its text redacted and its params (409 ai_conflict with reason id_taken,
// slug_taken, links_changed or board_deleted for a taken id, alias or slug, a gone link or a
// deleted section; landing_dir_exists, snapshot_missing or restore_failed for the landing files).
func (s *Server) aiChgRestore(sctx context.Context, spec aiChgResource, c aiChange) (bool, error) {
	exp := &aiExpect{}
	fail := func(code int, msg string) *aiError {
		var ae *aiError
		if errors.As(exp.refusal(), &ae) {
			out := *ae
			out.msg = aiRedactText(ae.msg, s.aiSecretsFrom(sctx))
			return aiChgWithParams(&out, c.EntityID, aiReasonLinksChanged)
		}
		e := &aiError{status: code, code: aiCodeOf(msg), msg: aiRedactText(msg, s.aiSecretsFrom(sctx))}
		return aiChgWithParams(e, c.EntityID, aiReasonLinksChanged)
	}
	if spec.name == "campaigns" {
		_, renumbered, code, msg := s.restoreGroup(sctx, c.Before)
		if code != http.StatusOK {
			return false, fail(code, msg)
		}
		return renumbered, nil
	}
	unpacked := false
	if spec.name == "landings" {
		exp.OnBefore = func(json.RawMessage) error {
			ok, err := s.aiLandingRestore(c.EntityID, s.aiBlobPath(c.Blob))
			unpacked = ok
			return err
		}
	}
	code, msg := s.restoreCatalogByRes(withAIExpect(sctx, exp), spec.res, c.Before)
	if code != http.StatusCreated {
		if unpacked {
			s.removeLandingFiles(c.EntityID)
		}
		return false, fail(code, msg)
	}
	return false, nil
}

// revertMessage rolls back every applied change of one reply of a chat, last applied first, and
// stops at the first one that cannot be reverted. actor is the session of the user, chatID and
// msgID name the reply. The report and the errors are those of aiRevertRun.
func (s *Server) revertMessage(ctx context.Context, actor security.Session, chatID, msgID string) (aiRevertReport, error) {
	return s.aiRevertRun(ctx, actor, func(p aiProposal) bool { return p.ChatID == chatID && p.MessageID == msgID })
}

// revertChat rolls back every applied change of one chat, last applied first, and stops at the
// first one that cannot be reverted. actor is the session of the user, chatID names the chat. The
// report and the errors are those of aiRevertRun.
func (s *Server) revertChat(ctx context.Context, actor security.Session, chatID string) (aiRevertReport, error) {
	return s.aiRevertRun(ctx, actor, func(p aiProposal) bool { return p.ChatID == chatID })
}

// aiRevertRun reverts, one by one through revertChange, the changes of the actor with status
// done whose proposal match accepts, last applied first; a change already reverted, rejected or
// never applied is skipped. Each revert takes the locks and checks the hash on its own, so a
// stopped run leaves every change either fully reverted or untouched. The run stops at the first
// change that cannot be reverted (409 ai_conflict, keitaro_running and so on): the report lists
// the ids reverted, the change that stopped the run with the reason and the ids not tried; a
// stopped run is not an error. It returns an error only when the actor or the change file is
// unusable.
func (s *Server) aiRevertRun(ctx context.Context, actor security.Session, match func(aiProposal) bool) (aiRevertReport, error) {
	report := aiRevertReport{Reverted: []string{}}
	if err := s.aiChgActor(actor); err != nil {
		return report, err
	}
	ctx = s.aiWithSecrets(ctx)
	recs, err := s.aiChgRead()
	if err != nil {
		return report, err
	}
	uid := s.aiUsageUID(actor)
	type item struct {
		id      string
		applied time.Time
		order   int
	}
	var items []item
	for i, r := range recs {
		if r.Proposal.UID == uid && r.Change.Status == aiChangeDone && match(r.Proposal) {
			items = append(items, item{id: r.Proposal.ID, applied: r.Change.Applied, order: i})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].applied.Equal(items[j].applied) {
			return items[i].applied.After(items[j].applied)
		}
		return items[i].order > items[j].order
	})
	for n, it := range items {
		if _, err := s.revertChange(ctx, actor, it.id); err != nil {
			report.StoppedAt, report.Error = it.id, err.Error()
			report.ErrorCode, report.ErrorParams = aiErrInfo(err)
			for _, rest := range items[n+1:] {
				report.Remaining = append(report.Remaining, rest.id)
			}
			return report, nil
		}
		report.Reverted = append(report.Reverted, it.id)
	}
	return report, nil
}
