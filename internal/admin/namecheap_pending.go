package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

// namecheapPendingHold is how long a pending row must exist before it may be removed. A
// create call that timed out can still be processed by Namecheap later, so an earlier
// removal could hide a real purchase.
const namecheapPendingHold = 15 * time.Minute

// ncCodesNotInAccount are the Namecheap error numbers that mean a domain is not in the
// account: "Domain not found" and "Domain is not associated with your account".
var ncCodesNotInAccount = map[string]bool{"2019166": true, "2016166": true}

// notInAccount reports whether a getInfo result is a clear answer that the account does not
// own the domain. info and err are the result of ncClient.info.
// Without an API error the answer must name the asked domain and say IsOwner is false
// (info.Foreign); an empty or different name is ambiguous. With an error only a definite
// not-found number counts. A transport failure, a timeout or any other API error does not.
func notInAccount(info ncDomainInfo, err error) bool {
	if err == nil {
		return info.Foreign
	}
	var ne *ncError
	return errors.As(err, &ne) && ne.Definite && ncCodesNotInAccount[ne.Code]
}

// nowUTC returns the current time of the purchase flow in UTC.
// It uses ncNow when a test has set it and the system clock otherwise.
func (s *Server) nowUTC() time.Time {
	if s.ncNow != nil {
		return s.ncNow().UTC()
	}
	return time.Now().UTC()
}

// pendingWait returns how long a pending row must still be kept.
// row is the stored record and now the current time. A row without a valid
// registrar_pending_since counts as old, so the result is zero. The result is never negative.
func pendingWait(row config.Domain, now time.Time) time.Duration {
	since, err := time.Parse(time.RFC3339, row.RegistrarPendingSince)
	if err != nil {
		return 0
	}
	if wait := since.Add(namecheapPendingHold).Sub(now); wait > 0 {
		return wait
	}
	return 0
}

// pendingKind is the outcome of judging a pending row.
type pendingKind int

const (
	// pendingOwned means the account owns the domain.
	pendingOwned pendingKind = iota
	// pendingReleased means the purchase never happened and the row was removed.
	pendingReleased
	// pendingTooEarly means the row is clearly unbought but is younger than the hold.
	pendingTooEarly
	// pendingUnknown means no clear answer; the row stays.
	pendingUnknown
	// pendingSaveFailed means the row should have been removed but the save failed.
	pendingSaveFailed
)

// pendingVerdict is the result of judgePending. status is the getInfo status when the
// domain is owned, retryAfter the seconds left of the hold for pendingTooEarly, and err the
// getInfo error when Namecheap gave no clear answer; it is nil when the answer was clear
// but the name is not available for registration.
type pendingVerdict struct {
	kind       pendingKind
	status     string
	retryAfter int
	err        error
}

// judgePending decides what a pending row is, for reconcile and for the forced delete.
// row is the stored pending record. The domain being owned is final at once. The row is
// removed only when getInfo clearly says the domain is not in the account, domains.check
// says it is available, and the hold since registrar_pending_since has passed. Anything
// else leaves the row. The caller holds the host lock and answers the request.
func (p ncPurchase) judgePending(row config.Domain) pendingVerdict {
	info, err := p.client.info(p.ctx, p.domain)
	switch {
	case err == nil && info.Owned:
		return pendingVerdict{kind: pendingOwned, status: info.Status}
	case !notInAccount(info, err):
		return pendingVerdict{kind: pendingUnknown, err: err}
	case !p.available():
		return pendingVerdict{kind: pendingUnknown}
	}
	if wait := pendingWait(row, p.s.nowUTC()); wait > 0 {
		return pendingVerdict{kind: pendingTooEarly, retryAfter: int((wait + time.Second - 1) / time.Second)}
	}
	if err := p.s.removeDomainRow(p.ctx, row.ID); err != nil {
		return pendingVerdict{kind: pendingSaveFailed}
	}
	return pendingVerdict{kind: pendingReleased}
}

// tooEarly answers 409 "too early" with retry_after, the seconds until the row may be removed.
func (p ncPurchase) tooEarly(v pendingVerdict) {
	writeNC(p.w, http.StatusConflict, "too early", map[string]any{"retry_after": v.retryAfter})
}

// forceDeleteDomain handles DELETE /api/domains/{id}?force=1 for a pending purchase.
// It runs before the common delete and returns true when it has answered.
// It answers only for an administrator with a cookie session, never for an API key, and only
// for a row that is pending; every other request goes on to the common delete, which
// refuses a pending row with 409.
// The host is locked like a purchase, so nothing can buy it during the check. The row is
// judged by the same rules as a repeated register request. Answers: domain is the
// account's, so the purchase is confirmed and the answer is 409 "registered at namecheap"
// with "confirmed": true; clearly not the account's and the name is available, so the row is
// deleted and the answer is 200; the same but the row is younger than 15 minutes, 409
// "too early" with "retry_after"; no clear answer, 502 when Namecheap did not answer and
// 409 "pending" when it answered ambiguously, the row stays. Other answers: 409
// "registration in progress" while the host is being bought, 409 "state changed" when the
// row was settled meanwhile, 404 when it is gone, 503 when Namecheap is not configured.
func (s *Server) forceDeleteDomain(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("force") != "1" || s.cfg.Domains == nil || !s.cookieAdmin(r.Context()) {
		return false
	}
	id := r.PathValue("id")
	row, found, err := s.domainRowByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return true
	}
	if !found || row.RegistrarStatus != registrarPending {
		return false
	}
	if !s.ncBuy.acquire(row.Host) {
		writeErr(w, http.StatusConflict, "registration in progress")
		return true
	}
	defer s.ncBuy.release(row.Host)
	acct := s.namecheapAccount()
	if !acct.ready() {
		writeErr(w, http.StatusServiceUnavailable, "namecheap not configured")
		return true
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), namecheapSettleTimeout)
	defer cancel()
	row, found, err = s.domainRowByID(ctx, id)
	switch {
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "read error")
		return true
	case !found:
		writeErr(w, http.StatusNotFound, "not found")
		return true
	case row.RegistrarStatus != registrarPending:
		writeErr(w, http.StatusConflict, "state changed")
		return true
	}
	p := ncPurchase{s: s, ctx: ctx, w: w, acct: acct, client: s.namecheapClient(acct), domain: row.Host}
	v := p.judgePending(row)
	switch v.kind {
	case pendingOwned:
		s.confirmPending(ctx, w, row.ID, v.status)
	case pendingReleased:
		s.logInfo("pending domain removed", "domain", row.Host)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	case pendingTooEarly:
		p.tooEarly(v)
	case pendingSaveFailed:
		writeErr(w, http.StatusInternalServerError, "save error")
	case pendingUnknown:
		if v.err != nil {
			writeErr(w, http.StatusBadGateway, v.err.Error())
		} else {
			writeNC(w, http.StatusConflict, "pending", map[string]any{"domain": row})
		}
	}
	return true
}

// cookieAdmin reports whether ctx belongs to an administrator signed in with a cookie session.
// API keys never qualify, so a key cannot start the account lookup of a purchase.
func (s *Server) cookieAdmin(ctx context.Context) bool {
	return !sessionFrom(ctx).API && s.viewerFrom(ctx).admin
}

// confirmPending turns a pending row into a confirmed purchase and answers 409 so the
// caller sees that nothing was deleted. id is the row, status the getInfo status.
func (s *Server) confirmPending(ctx context.Context, w http.ResponseWriter, id, status string) {
	updated, err := s.updateDomainRow(ctx, id, clearPending)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeNC(w, http.StatusConflict, "registered at namecheap", map[string]any{
		"confirmed": true, "domain": updated, "namecheap_status": status,
	})
}

// clearPending marks d as a confirmed purchase: no pending status, no pending time, enabled.
func clearPending(d *config.Domain) {
	d.RegistrarStatus = ""
	d.RegistrarPendingSince = ""
	d.Disabled = false
}

// domainRowByID reads one stored domain.
// id selects the row. found is false when none matches. An error comes from the store.
func (s *Server) domainRowByID(ctx context.Context, id string) (config.Domain, bool, error) {
	list, err := s.cfg.Domains.List(ctx)
	if err != nil {
		return config.Domain{}, false, err
	}
	row, found := domainByID(list, id)
	return row, found, nil
}
