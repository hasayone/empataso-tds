package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
)

const (
	namecheapRegistrar     = "namecheap"
	registrarPending       = "pending"
	namecheapBuyTimeout    = 3 * time.Minute
	namecheapSettleTimeout = 45 * time.Second
	namecheapNSTimeout     = 90 * time.Second
	namecheapMinYears      = 1
	namecheapMaxYears      = 10
	namecheapMinNS         = 2
	namecheapMaxNS         = 12
	namecheapPriceSlack    = 0.005
	namecheapRegisterBody  = 1 << 16
	// namecheapHostTTL is the TTL in seconds of the apex record written after a purchase.
	namecheapHostTTL = 1800
	// cloudflarePurchaseTimeout bounds the Cloudflare connection that follows a purchase.
	cloudflarePurchaseTimeout = 60 * time.Second
)

// ncRegisterReq is the body of POST /api/registrars/namecheap/register.
// MaxPrice is the total for Years that the operator saw; the server refuses a higher price.
// Nameservers, when set, are applied after the purchase.
// Cloudflare asks to put the domain behind Cloudflare after the purchase; it excludes Nameservers.
// Reconcile asks only to settle a pending row: Years and MaxPrice are then ignored and the
// request never buys.
type ncRegisterReq struct {
	Domain      string   `json:"domain"`
	Years       int      `json:"years"`
	MaxPrice    float64  `json:"max_price"`
	Group       string   `json:"group"`
	Nameservers []string `json:"nameservers"`
	Cloudflare  bool     `json:"cloudflare"`
	Reconcile   bool     `json:"reconcile"`
}

// writeNC writes an error body with extra fields next to the message.
// code is the HTTP status, msg the error text, extra the additional JSON fields.
// The "code" field is set after extra is copied, so extra cannot replace it.
func writeNC(w http.ResponseWriter, code int, msg string, extra map[string]any) {
	body := map[string]any{"error": msg}
	for k, v := range extra {
		body[k] = v
	}
	body["code"] = errCode(code, msg)
	writeJSON(w, code, body)
}

// handleNamecheapRegister registers a domain and parks it in domains.json.
//
// The server checks availability again, refuses premium names, prices the purchase and
// compares it with max_price. Before it calls domains.create it writes the domain with
// registrar_status "pending" and holds a per-domain lock, so two requests cannot buy one
// name. domains.create is called once and never retried. After any failure the server
// asks getInfo whether the account owns the name: a confirmed name is finished, a
// refused purchase removes the pending row, and an unknown outcome keeps it pending.
// A repeated request for a pending name only reconciles and never buys again.
// A request with "reconcile": true only settles a pending row: it needs no years or
// max_price, answers 404 when the domain is unknown and 409 "not pending" when the row is
// not a pending Namecheap purchase, and never reaches the purchase path.
//
// The price is the exact term price read from Namecheap just before the purchase; a term
// with no price row is refused. The pending row is disabled until the purchase is confirmed.
//
// Answers: 201 with {"domain": row, "charged": amount or 0 when unknown, "expected": price,
// "namecheap_status": text, "over_max_price": true, "ns_error": text, "hosts_error": text,
// "cloudflare": {ns, ns_set, ns_manual}, "cloudflare_error": text};
// 400 bad input, including both cloudflare and nameservers; 409 already exists, cloudflare asked
// while Cloudflare or the public address is not set up, registration in progress, not available, price unknown,
// price changed (with "price"), pending, too early (with "retry_after" seconds), or not
// registered (a pending row whose domain is clearly not in the account and still available,
// older than 15 minutes; the row is removed); 422 premium domain;
// 503 not configured or no store; 502 Namecheap failed (with "pending": true when the
// outcome is unknown).
func (s *Server) handleNamecheapRegister(w http.ResponseWriter, r *http.Request) {
	var req ncRegisterReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, namecheapRegisterBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	switch {
	case !validNCDomain(domain):
		writeErr(w, http.StatusBadRequest, "invalid domain")
		return
	case req.Reconcile:
	case req.Years < namecheapMinYears || req.Years > namecheapMaxYears:
		writeErr(w, http.StatusBadRequest, "years must be 1 to 10")
		return
	case !(req.MaxPrice > 0) || math.IsInf(req.MaxPrice, 0):
		writeErr(w, http.StatusBadRequest, "max_price required")
		return
	}
	servers, err := normalizeNameservers(req.Nameservers, 0)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.Reconcile && req.Cloudflare {
		if len(servers) > 0 {
			writeErr(w, http.StatusBadRequest, "choose cloudflare or nameservers")
			return
		}
		if err := s.cloudflareReady(); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
	}
	// The lock comes before the account is read: a settings PUT waits for it or is refused,
	// so the account read below stays the one the whole purchase uses.
	if !s.ncBuy.acquire(domain) {
		writeErr(w, http.StatusConflict, "registration in progress")
		return
	}
	defer s.ncBuy.release(domain)
	acct := s.namecheapAccount()
	if !acct.ready() || acct.Contacts == nil {
		writeErr(w, http.StatusServiceUnavailable, "namecheap not configured")
		return
	}
	if s.cfg.Domains == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	uid := s.viewerFrom(r.Context()).uid
	// A browser that disconnects must not leave a purchase half recorded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), namecheapBuyTimeout)
	defer cancel()
	p := ncPurchase{s: s, ctx: ctx, w: w, acct: acct, client: s.namecheapClient(acct), domain: domain, servers: servers, maxPrice: req.MaxPrice}
	existing, found, err := s.domainByHost(ctx, domain)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	switch {
	case found && existing.Registrar == namecheapRegistrar && existing.RegistrarStatus == registrarPending:
		p.reconcile(existing)
	case req.Reconcile && !found:
		writeErr(w, http.StatusNotFound, "not found")
	case req.Reconcile:
		writeErr(w, http.StatusConflict, "not pending")
	case found:
		writeErr(w, http.StatusConflict, "already exists")
	default:
		p.buy(req, uid)
	}
}

// ncPurchase holds the state of one registration request.
type ncPurchase struct {
	s       *Server
	ctx     context.Context
	w       http.ResponseWriter
	acct    ncAccount
	client  *ncClient
	domain  string
	servers []string
	// maxPrice is the total the operator accepted for the whole term.
	maxPrice float64
	// fresh is true for a purchase made by this request, never for a reconciled row.
	fresh bool
}

// isPriceUnknown reports whether err says the price list has no row for the asked term.
func isPriceUnknown(err error) bool {
	var ne *ncError
	return errors.As(err, &ne) && ne.Code == ncCodePriceUnknown
}

// buy runs the checks, writes the pending row, calls domains.create once and answers.
// req is the validated request and uid the creating user, empty for the environment administrator.
// The price is read fresh from Namecheap for the exact term, never from the cache. A term
// without a price row is refused with 409 "price unknown" before anything is written.
// The pending row is disabled, so it is not offered for campaign links before the
// purchase is confirmed.
func (p ncPurchase) buy(req ncRegisterReq, uid string) {
	p.fresh = true
	rows, err := p.client.check(p.ctx, []string{p.domain})
	if err != nil {
		writeErr(p.w, http.StatusBadGateway, err.Error())
		return
	}
	row, ok := availabilityOf(rows, p.domain)
	switch {
	case !ok || !row.Available:
		writeNC(p.w, http.StatusConflict, "not available", map[string]any{"reason": row.Err})
		return
	case row.Premium:
		writeErr(p.w, http.StatusUnprocessableEntity, "premium domain not allowed")
		return
	}
	_, tld, err := splitNCDomain(p.domain)
	if err != nil {
		writeErr(p.w, http.StatusBadRequest, "invalid domain")
		return
	}
	total, err := p.client.price(p.ctx, tld, req.Years)
	if err != nil {
		if isPriceUnknown(err) {
			writeErr(p.w, http.StatusConflict, "price unknown")
			return
		}
		writeErr(p.w, http.StatusBadGateway, err.Error())
		return
	}
	if total > req.MaxPrice+namecheapPriceSlack {
		writeNC(p.w, http.StatusConflict, "price changed", map[string]any{"price": total})
		return
	}
	pending := config.Domain{
		Host: p.domain, Role: "links", Group: req.Group, Disabled: true,
		Registrar: namecheapRegistrar, RegistrarAccount: p.acct.Name, RegistrarStatus: registrarPending,
		RegistrarExpected: total, RegistrarPendingSince: p.s.nowUTC().Format(time.RFC3339),
	}
	if req.Cloudflare {
		pending.CfStatus = cfStatusConnectPending
	}
	stored, code, msg := p.s.purchaseCatalog().insert(p.ctx, pending, uid)
	if code != http.StatusCreated {
		writeErr(p.w, code, msg)
		return
	}
	res, createErr := p.client.create(p.ctx, p.domain, req.Years, *p.acct.Contacts)
	// Everything after create has its own short deadline, so a create call that used up
	// the request time cannot leave the outcome unchecked.
	settle, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), namecheapSettleTimeout)
	defer cancel()
	p.ctx = settle
	if createErr == nil && res.Registered {
		p.finish(stored, res.Charged, "")
		return
	}
	info, infoErr := p.client.info(p.ctx, p.domain)
	switch {
	case infoErr == nil && info.Owned:
		p.finish(stored, res.Charged, info.Status)
	case createErr != nil && isDefinite(createErr) && isDefinite(infoErr):
		if err := p.s.removeDomainRow(p.ctx, stored.ID); err != nil {
			writeNC(p.w, http.StatusBadGateway, createErr.Error(), map[string]any{"pending": true})
			return
		}
		writeErr(p.w, http.StatusBadGateway, createErr.Error())
	default:
		msg := "registration outcome is not confirmed"
		if createErr != nil {
			msg = createErr.Error()
		}
		writeNC(p.w, http.StatusBadGateway, msg, map[string]any{"pending": true, "domain": stored})
	}
}

// reconcile settles a pending row without buying again.
// row is the stored pending record. When getInfo confirms the account owns the name the
// row is finished at once. When getInfo clearly says the name is not in the account and
// domains.check says it is still available, the purchase never happened: the row is removed
// and the answer is 409 "not registered" with "pending": false, but only after
// namecheapPendingHold since registrar_pending_since; before that the answer is 409
// "too early" with "retry_after" seconds and the row stays. In every other case, such as a
// network failure, an ambiguous answer or a name that is taken, the answer is 409 pending and
// the row stays.
func (p ncPurchase) reconcile(row config.Domain) {
	v := p.judgePending(row)
	switch v.kind {
	case pendingOwned:
		p.finish(row, 0, v.status)
	case pendingReleased:
		writeNC(p.w, http.StatusConflict, "not registered", map[string]any{"pending": false})
	case pendingTooEarly:
		p.tooEarly(v)
	case pendingSaveFailed:
		writeErr(p.w, http.StatusInternalServerError, "save error")
	default:
		writeNC(p.w, http.StatusConflict, "pending", map[string]any{"domain": row})
	}
}

// available reports whether domains.check answers that the domain can be registered.
// A failed call counts as not available.
func (p ncPurchase) available() bool {
	rows, err := p.client.check(p.ctx, []string{p.domain})
	if err != nil {
		return false
	}
	row, ok := availabilityOf(rows, p.domain)
	return ok && row.Available
}

// finish clears the pending state, enables the domain, applies the name servers and
// answers 201. row is the stored pending record, charged the ChargedAmount Namecheap
// reported (0 when it did not report one) and status the getInfo status, empty when it
// was not asked. Only a reported amount is written to registrar_charged; the answer
// carries "charged" (0 when unknown) and "expected", the price taken before the purchase.
// A reported amount above max_price is logged and marked with "over_max_price".
// A failed name-server call does not undo the registration; its text is returned as
// ns_error. A row marked connect_pending is connected to Cloudflare now with a deadline of a
// minute: success adds "cloudflare" and the updated "domain", a failure keeps connect_pending
// for the domain watch and adds "cloudflare_error". A fresh purchase without name servers and
// without Cloudflare gets an apex address record at the registrar when Config.PublicIP is set;
// a failure is returned as hosts_error. A failed save answers 500 and the row stays pending, so a repeated request
// reconciles it.
func (p ncPurchase) finish(row config.Domain, charged float64, status string) {
	updated, err := p.s.updateDomainRow(p.ctx, row.ID, func(d *config.Domain) {
		clearPending(d)
		if charged > 0 {
			d.RegistrarCharged = charged
		}
		d.WatchSince = p.s.nowUTC().Format(time.RFC3339)
	})
	if err != nil {
		writeNC(p.w, http.StatusInternalServerError, "registered but not saved", map[string]any{"pending": true})
		return
	}
	out := map[string]any{"domain": updated, "charged": updated.RegistrarCharged, "expected": updated.RegistrarExpected}
	if status != "" {
		out["namecheap_status"] = status
	}
	if p.maxPrice > 0 && charged > p.maxPrice+namecheapPriceSlack {
		p.s.logWarn("namecheap charged more than max_price", "domain", p.domain, "charged", charged, "max_price", p.maxPrice)
		out["over_max_price"] = true
	}
	switch {
	case len(p.servers) > 0:
		if err := p.client.setCustom(p.ctx, p.domain, p.servers); err != nil {
			out["ns_error"] = err.Error()
		}
	case updated.CfStatus == cfStatusConnectPending:
		p.connectAfterBuy(updated.ID, out)
	case p.fresh && p.s.cfg.PublicIP != "":
		host := ncHost{Name: "@", Type: addressRecordType(p.s.cfg.PublicIP), Address: p.s.cfg.PublicIP, TTL: namecheapHostTTL}
		if err := p.client.setHosts(p.ctx, p.domain, []ncHost{host}); err != nil {
			out["hosts_error"] = err.Error()
		}
	}
	writeJSON(p.w, http.StatusCreated, out)
}

// connectAfterBuy connects a purchased domain to Cloudflare and records the outcome in out.
// id is the stored row. The call has its own deadline, so the time the purchase used up does
// not cut it. On success out gets "cloudflare" and the updated "domain"; on failure out gets
// "cloudflare_error" and the row stays connect_pending, which the domain watch retries.
func (p ncPurchase) connectAfterBuy(id string, out map[string]any) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), cloudflarePurchaseTimeout)
	defer cancel()
	row, res, err := p.s.connectCloudflare(ctx, id)
	if err != nil {
		out["cloudflare_error"] = err.Error()
		return
	}
	out["domain"], out["cloudflare"] = row, res
}

// cloudflareReady reports why a domain cannot be connected to Cloudflare now: Cloudflare is
// not configured (errCFNotConfigured) or the public address is not set (errNoPublicIP).
// It returns nil when both are set.
func (s *Server) cloudflareReady() error {
	switch {
	case !s.cloudflareAccount().ready():
		return errCFNotConfigured
	case s.cfg.PublicIP == "":
		return errNoPublicIP
	}
	return nil
}

// availabilityOf finds the row for domain in a check answer.
func availabilityOf(rows []ncAvailability, domain string) (ncAvailability, bool) {
	for _, r := range rows {
		if r.Domain == domain {
			return r, true
		}
	}
	return ncAvailability{}, false
}

// isDefinite reports whether err is a Namecheap answer that refused the command.
func isDefinite(err error) bool {
	var ne *ncError
	return errors.As(err, &ne) && ne.Definite
}

// domainByHost returns the stored domain with the given host, case ignored.
// When several rows match, a pending row wins, so a stuck purchase is always the one found.
// found is false when none matches. An error comes from the store.
func (s *Server) domainByHost(ctx context.Context, host string) (config.Domain, bool, error) {
	list, err := s.cfg.Domains.List(ctx)
	if err != nil {
		return config.Domain{}, false, err
	}
	var found config.Domain
	ok := false
	for _, d := range list {
		if !strings.EqualFold(d.Host, host) {
			continue
		}
		if d.RegistrarStatus == registrarPending {
			return d, true, nil
		}
		if !ok {
			found, ok = d, true
		}
	}
	return found, ok, nil
}

// Failures of a domain row write. They are fixed texts: a handler maps them to a status with
// domainErrStatus and never sends the wrapped cause to a client.
var (
	errDomainStore       = errors.New("store unavailable")
	errDomainNotFound    = errors.New("not found")
	errDomainRead        = errors.New("read error")
	errDomainSave        = errors.New("save error")
	errDomainHostChanged = errors.New("host changed")
)

// domainErrStatus maps an error of editDomain or recheckDomain to an HTTP status and a client
// message: 503, 404, 409 for a changed host, and 500 for a read or save failure or any other error.
func domainErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, errDomainStore):
		return http.StatusServiceUnavailable, errDomainStore.Error()
	case errors.Is(err, errDomainNotFound):
		return http.StatusNotFound, errDomainNotFound.Error()
	case errors.Is(err, errDomainHostChanged):
		return http.StatusConflict, errDomainHostChanged.Error()
	case errors.Is(err, errDomainSave):
		return http.StatusInternalServerError, errDomainSave.Error()
	}
	return http.StatusInternalServerError, errDomainRead.Error()
}

// updateDomainRow changes one stored domain under domainMu.
// id selects the row and mutate edits it. It returns the saved row.
// An error means the row is gone or the store failed.
func (s *Server) updateDomainRow(ctx context.Context, id string, mutate func(*config.Domain)) (config.Domain, error) {
	return s.editDomain(ctx, id, func(d *config.Domain) error {
		mutate(d)
		return nil
	})
}

// editDomain changes one stored domain under domainMu.
// id selects the row and mutate edits it; a non-nil error from mutate leaves the file untouched
// and is returned as it is. It returns the saved row.
// Other errors are errDomainStore, errDomainNotFound, errDomainRead and errDomainSave.
func (s *Server) editDomain(ctx context.Context, id string, mutate func(*config.Domain) error) (config.Domain, error) {
	if s.cfg.Domains == nil {
		return config.Domain{}, errDomainStore
	}
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	list, err := s.cfg.Domains.List(ctx)
	if err != nil {
		return config.Domain{}, fmt.Errorf("%w: %v", errDomainRead, err)
	}
	for i := range list {
		if list[i].ID != id {
			continue
		}
		if err := mutate(&list[i]); err != nil {
			return config.Domain{}, err
		}
		if err := s.cfg.Domains.Save(ctx, list); err != nil {
			return config.Domain{}, fmt.Errorf("%w: %v", errDomainSave, err)
		}
		return list[i], nil
	}
	return config.Domain{}, errDomainNotFound
}

// editDomainSnap changes one stored domain under domainMu when Host and CfZoneID still match snap.
// id selects the row, snap is the row read before an external call, and mutate edits the record.
// It returns errDomainHostChanged when the row changed during the call.
func (s *Server) editDomainSnap(ctx context.Context, id string, snap config.Domain, mutate func(*config.Domain) error) (config.Domain, error) {
	return s.editDomain(ctx, id, func(cur *config.Domain) error {
		if cur.Host != snap.Host || cur.CfZoneID != snap.CfZoneID {
			return errDomainHostChanged
		}
		return mutate(cur)
	})
}

// removeDomainRow deletes one stored domain under domainMu.
// id selects the row. A missing row is not an error. An error comes from the store.
func (s *Server) removeDomainRow(ctx context.Context, id string) error {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	list, err := s.cfg.Domains.List(ctx)
	if err != nil {
		return err
	}
	kept := make([]config.Domain, 0, len(list))
	for _, d := range list {
		if d.ID != id {
			kept = append(kept, d)
		}
	}
	if len(kept) == len(list) {
		return nil
	}
	return s.cfg.Domains.Save(ctx, kept)
}

// normalizeNameservers lower-cases, validates and de-duplicates name server host names.
// least is the smallest accepted count; 0 also accepts an empty list. At most 12 are kept.
// It returns the list or an error naming the defect.
func normalizeNameservers(in []string, least int) ([]string, error) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		ns := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if !validNCDomain(ns) {
			return nil, errors.New("invalid nameserver: " + truncateText(raw, 64))
		}
		if _, dup := seen[ns]; dup {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	if len(out) > namecheapMaxNS {
		return nil, errors.New("at most 12 nameservers")
	}
	if (len(out) > 0 && len(out) < namecheapMinNS) || len(out) < least {
		return nil, errors.New("at least 2 nameservers required")
	}
	return out, nil
}

// ncNSReq is the body of POST /api/domains/{id}/ns. Confirm must be true.
type ncNSReq struct {
	Nameservers []string `json:"nameservers"`
	Confirm     bool     `json:"confirm"`
}

// handleDomainNS points a domain registered through this account to custom name servers.
//
// Only domains whose registrar is Namecheap and whose registrar_account equals the stored
// account name are accepted, and Namecheap must also report the account as the owner.
// The operator confirms with "confirm": true.
// Answers: 200 {"status": "ok", "nameservers": [...]}; 400 bad input or no confirm;
// 403 not registered through this account; 404 unknown id; 409 the registration is pending;
// 503 not configured or no store; 502 Namecheap failed.
func (s *Server) handleDomainNS(w http.ResponseWriter, r *http.Request) {
	var req ncNSReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, namecheapRegisterBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if !req.Confirm {
		writeErr(w, http.StatusBadRequest, "confirm required")
		return
	}
	servers, err := normalizeNameservers(req.Nameservers, namecheapMinNS)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	acct := s.namecheapAccount()
	if !acct.ready() {
		writeErr(w, http.StatusServiceUnavailable, "namecheap not configured")
		return
	}
	if s.cfg.Domains == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	list, err := s.cfg.Domains.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	row, found := domainByID(list, r.PathValue("id"))
	switch {
	case !found:
		writeErr(w, http.StatusNotFound, "not found")
		return
	case row.RegistrarStatus == registrarPending:
		writeErr(w, http.StatusConflict, "pending")
		return
	case row.Registrar != namecheapRegistrar || row.RegistrarAccount == "" || row.RegistrarAccount != acct.Name:
		writeErr(w, http.StatusForbidden, "domain is not registered through this account")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), namecheapNSTimeout)
	defer cancel()
	client := s.namecheapClient(acct)
	info, err := client.info(ctx, row.Host)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !info.Owned {
		writeErr(w, http.StatusForbidden, "domain is not registered through this account")
		return
	}
	if err := client.setCustom(ctx, row.Host, servers); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.logInfo("namecheap nameservers changed", "domain", row.Host)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "nameservers": servers})
}

// domainByID returns the domain with the given id and whether it exists.
func domainByID(list []config.Domain, id string) (config.Domain, bool) {
	for _, d := range list {
		if d.ID == id {
			return d, true
		}
	}
	return config.Domain{}, false
}
