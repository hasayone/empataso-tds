package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
)

const (
	cloudflareConnectTimeout = 2 * time.Minute
	connectedAckLimit        = 500
)

// Refusals of a Cloudflare connection. A handler maps them with connectErrStatus.
var (
	errCFNotConfigured = errors.New("cloudflare is not configured")
	errNoPublicIP      = errors.New("public ip is not set")
	errCFBusy          = errors.New("connection in progress")
	errDomainPending   = errors.New("pending")
	errBadHost         = errors.New("host is not a domain name")
)

// cfZoneSettings are the zone settings every connected domain gets: the origin is spoken to
// over TLS and plain HTTP is sent to HTTPS.
var cfZoneSettings = [][2]string{{"ssl", "full"}, {"always_use_https", "on"}}

// cfConnectResult is what a connection did to the name servers.
// NS are the zone name servers. NSSet is true when the registrar now points at them, whether
// the panel changed it or it already did. NSManual is true when the panel cannot change the
// name servers, so the operator has to set NS at the registrar.
type cfConnectResult struct {
	NS       []string `json:"ns"`
	NSSet    bool     `json:"ns_set"`
	NSManual bool     `json:"ns_manual"`
}

// cfConnectView is the body of a successful POST /api/domains/{id}/cloudflare: the domain
// row with the connection result next to it.
type cfConnectView struct {
	domainRow
	cfConnectResult
}

// cfConnectReq is the body of POST /api/domains/{id}/cloudflare. Confirm must be true.
type cfConnectReq struct {
	Confirm bool `json:"confirm"`
}

// connectCloudflare puts a stored domain behind Cloudflare. It is safe to repeat.
// ctx bounds all the calls and id selects the row. The steps are: find or create the zone
// (a stored zone id is read first, and a vanished zone is created again), make the apex a
// proxied record to Config.PublicIP, set ssl full and always_use_https on, point the
// registrar name servers at the zone when the domain is a Namecheap domain of the configured
// account that the account owns, and ask Cloudflare to check the zone. Only then is the row
// written under domainMu: zone id, name servers, status (active or pending), and for a pending
// zone OK false with reason cf_pending. A failure before that write changes nothing, so a row
// marked connect_pending stays so for a later retry. One connection per domain runs at a time.
// It returns the saved row and the name server outcome. Errors are errCFNotConfigured,
// errNoPublicIP, errCFBusy, errDomainStore, errDomainNotFound, errDomainPending, errBadHost,
// errDomainHostChanged, the errors of the store, and the errors of Cloudflare and Namecheap
// whose text is already free of secrets.
func (s *Server) connectCloudflare(ctx context.Context, id string) (config.Domain, cfConnectResult, error) {
	acct := s.cloudflareAccount()
	switch {
	case !acct.ready():
		return config.Domain{}, cfConnectResult{}, errCFNotConfigured
	case s.cfg.PublicIP == "":
		return config.Domain{}, cfConnectResult{}, errNoPublicIP
	case s.cfg.Domains == nil:
		return config.Domain{}, cfConnectResult{}, errDomainStore
	}
	if !s.cfBusy.acquire(id) {
		return config.Domain{}, cfConnectResult{}, errCFBusy
	}
	defer s.cfBusy.release(id)
	d, found, err := s.domainRowByID(ctx, id)
	switch {
	case err != nil:
		return config.Domain{}, cfConnectResult{}, errDomainRead
	case !found:
		return config.Domain{}, cfConnectResult{}, errDomainNotFound
	case d.RegistrarStatus == registrarPending:
		return config.Domain{}, cfConnectResult{}, errDomainPending
	}
	host := strings.ToLower(strings.TrimSpace(d.Host))
	if !validNCDomain(host) {
		return config.Domain{}, cfConnectResult{}, errBadHost
	}
	client := s.cloudflareClient(acct)
	zone, err := connectZone(ctx, client, d, host)
	if err != nil {
		return config.Domain{}, cfConnectResult{}, err
	}
	if err := client.upsertProxiedA(ctx, zone.ID, host, s.cfg.PublicIP); err != nil {
		return config.Domain{}, cfConnectResult{}, err
	}
	for _, st := range cfZoneSettings {
		if err := client.setSetting(ctx, zone.ID, st[0], st[1]); err != nil {
			return config.Domain{}, cfConnectResult{}, err
		}
	}
	res, err := s.delegateNS(ctx, d, host, zone)
	if err != nil {
		return config.Domain{}, cfConnectResult{}, err
	}
	if zone.Status != cfStatusActive {
		_ = client.activationCheck(ctx, zone.ID)
	}
	updated, err := s.editDomain(ctx, id, func(cur *config.Domain) error {
		if cur.Host != d.Host {
			return errDomainHostChanged
		}
		applyZone(cur, zone, res.NS)
		if cur.WatchSince == "" {
			cur.WatchSince = s.nowUTC().Format(time.RFC3339)
		}
		return nil
	})
	if err != nil {
		return config.Domain{}, cfConnectResult{}, err
	}
	s.maybeScheduleAdminDNS(updated)
	return updated, res, nil
}

// connectZone returns the zone a domain is connected to. d is the stored row and host its
// lower-case name. A stored zone id is read first and used when the zone still has that name;
// a vanished zone, which Cloudflare answers with 404, is created again like a missing one.
// An error is a failed call.
func connectZone(ctx context.Context, client *cfClient, d config.Domain, host string) (*cfZone, error) {
	if d.CfZoneID != "" {
		z, err := client.zone(ctx, d.CfZoneID)
		switch {
		case err == nil && strings.EqualFold(z.Name, host):
			return z, nil
		case err != nil && !isCFNotFound(err):
			return nil, err
		}
	}
	return client.ensureZone(ctx, host)
}

// applyZone writes the state of a zone into row. ns are the zone name servers; an empty list
// falls back to the ones the zone reports. An active zone is marked active; any other is
// marked pending and the row gets OK false with reason cf_pending and the name servers as the
// detail, because the host cannot answer before Cloudflare serves the zone.
func applyZone(row *config.Domain, zone *cfZone, ns []string) {
	if len(ns) == 0 {
		ns = zone.NameServers
	}
	prevZone := row.CfZoneID
	row.CfZoneID = zone.ID
	if prevZone != "" && prevZone != row.CfZoneID {
		row.AdminDNS = ""
	}
	row.CfNS = slices.Clone(ns)
	if zone.Status == cfStatusActive {
		row.CfStatus = cfStatusActive
		return
	}
	row.CfStatus = cfStatusPending
	row.OK = false
	row.Reason, row.ReasonDetail = reasonCFPending, strings.Join(row.CfNS, ",")
}

// delegateNS points the registrar name servers of d at the zone when the panel may.
// d is the stored row, host its name and zone the Cloudflare zone. The panel changes the name
// servers only for a Namecheap domain of the configured account that Namecheap confirms the
// account owns; any other domain gets NSManual. A domain whose name servers already are the
// zone's is left alone and counts as set. An error is a failed Namecheap call.
func (s *Server) delegateNS(ctx context.Context, d config.Domain, host string, zone *cfZone) (cfConnectResult, error) {
	res := cfConnectResult{NS: zone.NameServers}
	acct := s.namecheapAccount()
	servers, err := normalizeNameservers(zone.NameServers, namecheapMinNS)
	if d.Registrar != namecheapRegistrar || !acct.ready() || d.RegistrarAccount != acct.Name || err != nil {
		res.NSManual = true
		return res, nil
	}
	res.NS = servers
	client := s.namecheapClient(acct)
	info, err := client.info(ctx, host)
	switch {
	case err != nil:
		return res, err
	case !info.Owned:
		res.NSManual = true
		return res, nil
	case sameHosts(info.Nameservers, servers):
		res.NSSet = true
		return res, nil
	}
	if err := client.setCustom(ctx, host, servers); err != nil {
		return res, err
	}
	s.logInfo("namecheap nameservers changed for cloudflare", "domain", host)
	res.NSSet = true
	return res, nil
}

// sameHosts reports whether a and b hold the same host names, in any order.
// Both lists are lower case without a trailing dot.
func sameHosts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// connectErrStatus maps an error of connectCloudflare to an HTTP status and a client message:
// 409 for a missing setup, a pending purchase, a running connection or a changed host,
// 404 for an unknown domain, 400 for a host that is not a domain name, 503 without a store,
// 500 for a store failure, and 502 with the text of the Cloudflare or Namecheap error otherwise.
func connectErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, errCFNotConfigured), errors.Is(err, errNoPublicIP), errors.Is(err, errCFBusy),
		errors.Is(err, errDomainPending):
		return http.StatusConflict, err.Error()
	case errors.Is(err, errBadHost):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, errDomainStore), errors.Is(err, errDomainNotFound), errors.Is(err, errDomainHostChanged),
		errors.Is(err, errDomainRead), errors.Is(err, errDomainSave):
		return domainErrStatus(err)
	}
	return http.StatusBadGateway, err.Error()
}

// handleDomainCloudflare connects a stored domain to Cloudflare.
// The body is {"confirm": true}. The work runs through connectCloudflare with a deadline that
// a closed browser does not cut.
// Answers: 200 with the domain row plus "ns", "ns_set" and "ns_manual"; 400 without confirm or
// for a host that is not a domain name; 404 for an unknown id; 409 when Cloudflare or the public
// address is not set up, the purchase is pending or a connection of the domain is running;
// 502 with the Cloudflare or Namecheap error text; 503 without a store.
func (s *Server) handleDomainCloudflare(w http.ResponseWriter, r *http.Request) {
	var req cfConnectReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, namecheapRegisterBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if !req.Confirm {
		writeErr(w, http.StatusBadRequest, "confirm required")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), cloudflareConnectTimeout)
	defer cancel()
	row, res, err := s.connectCloudflare(ctx, r.PathValue("id"))
	if err != nil {
		code, msg := connectErrStatus(err)
		writeErr(w, code, msg)
		return
	}
	s.logInfo("cloudflare connected", "domain", row.Host, "status", row.CfStatus)
	view, err := s.domainView(r.Context(), row)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	writeJSON(w, http.StatusOK, cfConnectView{domainRow: view, cfConnectResult: res})
}

// domainRef returns the access reference of a stored domain.
func domainRef(d config.Domain) ref {
	return ref{id: d.ID, label: d.Group, owner: d.CreatedBy}
}

// connectedNotice is one entry of GET /api/domains/connected.
type connectedNotice struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	ConnectedAt string `json:"connected_at"`
}

// handleDomainsConnected lists the domains that became reachable and whose notice nobody has
// acknowledged: ConnectedAt set and ConnectSeen not. The body is a JSON array of
// {"id", "host", "connected_at"}, empty when there is none. A user sees only domains the
// grants allow. Answers 503 without a store and 500 when the file cannot be read.
func (s *Server) handleDomainsConnected(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Domains == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	list, err := s.cfg.Domains.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	v := s.viewerFrom(r.Context())
	out := []connectedNotice{}
	for _, d := range list {
		if d.ConnectedAt != "" && !d.ConnectSeen && v.canSee(resDomains, domainRef(d)) {
			out = append(out, connectedNotice{ID: d.ID, Host: d.Host, ConnectedAt: d.ConnectedAt})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// connectedAckReq is the body of POST /api/domains/connected/ack.
type connectedAckReq struct {
	IDs []string `json:"ids"`
}

// handleDomainsConnectedAck marks the notices of the given domains as seen.
// The body is {"ids": [...]} with at most 500 ids. The write runs under domainMu. An id that is
// unknown, not visible to the caller or without a connected time is skipped. It is safe to repeat:
// the answer {"acked": n} counts the listed domains that have a connected time, whether or not
// they were already seen. Answers 400 for a bad or too long list, 503 without a store, 500 for
// a read or save failure.
func (s *Server) handleDomainsConnectedAck(w http.ResponseWriter, r *http.Request) {
	var req connectedAckReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, namecheapRegisterBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(req.IDs) > connectedAckLimit {
		writeErr(w, http.StatusBadRequest, "too many ids")
		return
	}
	if s.cfg.Domains == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	wanted := make(map[string]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		wanted[id] = struct{}{}
	}
	v := s.viewerFrom(r.Context())
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	list, err := s.cfg.Domains.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	acked, changed := 0, false
	for i := range list {
		if _, ok := wanted[list[i].ID]; !ok || list[i].ConnectedAt == "" || !v.canSee(resDomains, domainRef(list[i])) {
			continue
		}
		acked++
		if !list[i].ConnectSeen {
			list[i].ConnectSeen, changed = true, true
		}
	}
	if changed {
		if err := s.cfg.Domains.Save(r.Context(), list); err != nil {
			writeErr(w, http.StatusInternalServerError, "save error")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"acked": acked})
}
