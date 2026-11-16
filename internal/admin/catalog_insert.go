package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/egerkuzma/emtds/internal/config"
)

// insert numbers, normalizes, validates and appends one new record.
// item is the decoded body. uid is the creating user, or empty for the environment administrator.
// It runs prep, takes an id from the counter when the id is empty, runs valid, sets the owner,
// and saves under hooks.mu. Both the HTTP create and server-side creators such as domain
// registration go through it.
// It returns the stored record and http.StatusCreated, or the zero record with an error status
// (400 invalid, 409 duplicate id, 500 store failure) and the client message.
func (c crudCatalog[T]) insert(ctx context.Context, item T, uid string) (T, int, string) {
	var zero T
	if c.hooks != nil && c.hooks.seq != nil {
		c.hooks.seq.mu.Lock()
		defer c.hooks.seq.mu.Unlock()
	}
	if c.hooks != nil && c.hooks.prep != nil {
		c.hooks.prep(&item)
	}
	if c.idOf(item) == "" && c.hooks != nil && c.hooks.seq != nil {
		floor := 1
		if c.hooks.floor != nil {
			n, err := c.hooks.floor()
			if err != nil {
				return zero, http.StatusInternalServerError, "read error"
			}
			floor = n
		}
		n, err := c.hooks.seq.take(floor)
		if err != nil {
			return zero, http.StatusInternalServerError, "save error"
		}
		c.setID(&item, strconv.Itoa(n))
	} else if c.hooks != nil && c.hooks.seq != nil {
		c.hooks.seq.note(c.idOf(item))
	}
	if msg := c.valid(item); msg != "" {
		return zero, http.StatusBadRequest, msg
	}
	if c.hooks != nil && c.hooks.setOwner != nil {
		c.hooks.setOwner(&item, uid)
	}
	if c.hooks != nil && c.hooks.setBoard != nil {
		c.hooks.setBoard(&item, boardFrom(ctx))
	}
	return c.appendRecord(ctx, item, aiExpectFrom(ctx), nil)
}

// appendRecord appends a finished record to the catalog under lockWrite and saves it.
// item is stored as it is. check is the precondition: nil keeps the plain 409 "already exists"
// for a taken id; otherwise a taken id, or an expectation that does not accept a new record
// (see aiExpect.check), answers 409 ai_conflict. pre, when set, runs under the lock after
// every check and right before the save; an error from it aborts the write.
// The caller holds hooks.seq.mu when the catalog has a counter.
// The aiExpect of ctx, when set, receives OnAfter with the saved record before the unlock.
// It returns the record and http.StatusCreated, or the zero record with an error status
// (409 taken id, a beforeInsert or pre refusal, 500 store failure) and the client message.
func (c crudCatalog[T]) appendRecord(ctx context.Context, item T, check *aiExpect, pre func() error) (T, int, string) {
	var zero T
	unlock := c.lockWrite()
	defer unlock()
	list, err := c.store.List(ctx)
	if err != nil {
		return zero, http.StatusInternalServerError, "read error"
	}
	taken := false
	for _, cur := range list {
		if c.idOf(cur) == c.idOf(item) {
			taken = true
			break
		}
	}
	if taken && check == nil {
		return zero, http.StatusConflict, "already exists"
	}
	if taken {
		return zero, http.StatusConflict, conflictMsg("id already exists")
	}
	if err := check.check(nil, false); err != nil {
		code, msg := c.s.aiFail(err)
		return zero, code, msg
	}
	if c.hooks != nil && c.hooks.beforeInsert != nil {
		if code, msg := c.hooks.beforeInsert(item, list); code != 0 {
			return zero, code, msg
		}
	}
	if pre != nil {
		if err := pre(); err != nil {
			code, msg := c.s.aiFail(err)
			return zero, code, msg
		}
	}
	list = append(list, item)
	if c.hooks != nil && c.hooks.tidy != nil {
		list = c.hooks.tidy(list, c.idOf(item))
	}
	if err := c.store.Save(ctx, list); err != nil {
		return zero, http.StatusInternalServerError, "save error"
	}
	aiExpectFrom(ctx).after(item)
	unlock()
	if c.hooks != nil && c.hooks.afterWrite != nil {
		c.hooks.afterWrite(item, nil)
	}
	return item, http.StatusCreated, ""
}

// restoreCatalog puts a deleted record back with the id, owner and section it had.
// raw is the stored record taken before the delete. The record is not prepared and gets
// no new owner or section; after a successful save the id counter is moved past a numeric id.
// The viewer of ctx needs the create right, the section and the write right on the
// restored record; for a non-admin userLimit and userCheck run as on create, and valid always runs.
// Under the lock, a taken id, a section that was deleted, or a link that points at a missing
// record (hooks.restoreCheck) is 409 ai_conflict. The aiExpect of ctx, when set, gets OnBefore
// with the record to restore, right before the save, and may refuse it (for example a landing
// whose file directory is not empty); OnAfter gets the saved record.
// It returns the record and http.StatusCreated, 409 ai_conflict as above,
// 400 for an unreadable or invalid record, 403 for a missing right, 500 for a store failure.
func (c crudCatalog[T]) restoreCatalog(ctx context.Context, raw json.RawMessage) (T, int, string) {
	var zero, item T
	if c.store == nil {
		return zero, http.StatusServiceUnavailable, "store unavailable"
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return zero, http.StatusBadRequest, "bad json"
	}
	v := c.s.viewerFrom(ctx)
	if c.hooks != nil && c.hooks.res != "" {
		if !v.canCreate(c.hooks.res) || !v.canWrite(c.hooks.res, catalogItemRef(c.idOf, c.hooks, item)) {
			return zero, http.StatusForbidden, "forbidden"
		}
	}
	if c.hooks != nil && c.hooks.boardID != nil {
		board := c.hooks.boardID(item)
		if !c.s.canRestoreOnBoard(ctx, v, board) {
			return zero, http.StatusForbidden, "forbidden"
		}
		if code, msg := c.s.restoreBoardCheck(ctx, board); code != 0 {
			return zero, code, msg
		}
	}
	if !v.admin && c.hooks != nil {
		if c.hooks.userLimit != nil {
			if msg := c.hooks.userLimit(&item, nil); msg != "" {
				return zero, http.StatusForbidden, msg
			}
		}
		if c.hooks.userCheck != nil {
			if msg := c.hooks.userCheck(ctx, v, item, nil); msg != "" {
				return zero, http.StatusBadRequest, msg
			}
		}
	}
	if msg := c.valid(item); msg != "" {
		return zero, http.StatusBadRequest, msg
	}
	if c.hooks != nil && c.hooks.seq != nil {
		c.hooks.seq.mu.Lock()
		defer c.hooks.seq.mu.Unlock()
	}
	pre := func() error {
		if c.hooks != nil && c.hooks.restoreCheck != nil {
			if msg := c.hooks.restoreCheck(ctx, item); msg != "" {
				return newAIError(http.StatusConflict, aiErrConflict, msg)
			}
		}
		return aiExpectFrom(ctx).before(item)
	}
	stored, code, msg := c.appendRecord(ctx, item, &aiExpect{Absent: true}, pre)
	if code == http.StatusCreated && c.hooks != nil && c.hooks.seq != nil {
		c.hooks.seq.note(c.idOf(stored))
	}
	return stored, code, msg
}

// offerRestoreCheck refuses to restore an offer whose affiliate network is gone.
// ctx is the request context and item the offer to restore.
// It returns an empty string when the offer has no network, or the network exists,
// and the client message otherwise. A network list that cannot be read is also refused.
func (s *Server) offerRestoreCheck(ctx context.Context, item config.Offer) string {
	if item.NetworkID == "" || s.cfg.Networks == nil {
		return ""
	}
	list, err := s.cfg.Networks.List(ctx)
	if err != nil {
		return "affiliate networks cannot be read"
	}
	for _, n := range list {
		if n.ID == item.NetworkID {
			return ""
		}
	}
	return "affiliate network " + item.NetworkID + " was deleted"
}

// restoreCatalogByRes restores one deleted catalog record of the resource res.
// res is resOffers, resLandings, resSources or resNetworks; raw is the record taken before the delete.
// It returns the status and message of restoreCatalog, or 400 "unknown resource" for any other res.
// The status is http.StatusCreated on success.
func (s *Server) restoreCatalogByRes(ctx context.Context, res string, raw json.RawMessage) (int, string) {
	var code int
	var msg string
	switch res {
	case resOffers:
		_, code, msg = s.offerCatalog().restoreCatalog(ctx, raw)
	case resLandings:
		_, code, msg = s.landingCatalog().restoreCatalog(ctx, raw)
	case resSources:
		_, code, msg = s.sourceCatalog().restoreCatalog(ctx, raw)
	case resNetworks:
		_, code, msg = s.networkCatalog().restoreCatalog(ctx, raw)
	default:
		return http.StatusBadRequest, "unknown resource"
	}
	return code, msg
}
