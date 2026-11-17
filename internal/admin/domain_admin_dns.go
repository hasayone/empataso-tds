package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
)

// adminDNSTimeout bounds one admin DNS create or update through Cloudflare.
const adminDNSTimeout = time.Minute

// errAdminDNSHost means the parked host is not the Cloudflare zone root name.
var errAdminDNSHost = errors.New("host is not the cloudflare zone root")

// errAdminDNSBusy means an admin DNS update for the same domain is already running.
var errAdminDNSBusy = errors.New("admin dns update in progress")

// domainAPIView builds the API row for d with a computed admin_host and campaign count.
func domainAPIView(d config.Domain, campaigns int) domainRow {
	return domainRow{Domain: d, Campaigns: campaigns, AdminHost: config.AdminHostOf(d)}
}

// scheduleAdminDNS runs ensureAdminDNS in the background without blocking the caller.
// A busy host is queued for one retry after the current run finishes.
func (s *Server) scheduleAdminDNS(id string) {
	ctx := context.Background()
	row, found, err := s.domainRowByID(ctx, id)
	if err != nil || !found {
		return
	}
	host := config.NormalizeHTTPHost(row.Host)
	if host == "" {
		return
	}
	if !s.adminDNSBusy.acquire(host) {
		s.adminDNSBusy.noteRetry(host, id)
		return
	}
	run := func() {
		defer func() {
			if retryID := s.adminDNSBusy.releaseAndRetry(host); retryID != "" {
				s.scheduleAdminDNS(retryID)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), adminDNSTimeout)
		defer cancel()
		if err := s.ensureAdminDNS(ctx, id); err != nil {
			s.logWarn("admin dns background", "id", id, "err", err)
		}
	}
	if s.adminDNSSync {
		run()
		return
	}
	s.adminDNSWG.Add(1)
	go func() {
		defer s.adminDNSWG.Done()
		run()
	}()
}

// maybeScheduleAdminDNS starts a background admin DNS update when the domain qualifies.
func (s *Server) maybeScheduleAdminDNS(d config.Domain) {
	if !config.IsAdminDomain(d) || d.CfZoneID == "" {
		return
	}
	if d.AdminDNS != "ok" || d.AdminReason == "dns" {
		s.scheduleAdminDNS(d.ID)
	}
}

// ensureAdminDNS creates or updates the proxied admin.<host> record and always writes admin_dns.
// After a successful Cloudflare write it probes admin.<host> and stores AdminOK and AdminReason.
// ctx bounds Cloudflare calls and id selects the row. The return is a store or host-changed error only.
func (s *Server) ensureAdminDNS(ctx context.Context, id string) error {
	snap, found, err := s.domainRowByID(ctx, id)
	if err != nil || !found {
		return err
	}
	msg := s.adminDNSMessage(ctx, snap)
	var adminOK bool
	var adminReason string
	if msg == "ok" {
		probe := snap
		probe.AdminDNS = "ok"
		adminOK, adminReason = s.checkAdminHost(ctx, probe)
	}
	_, err = s.editDomainSnap(ctx, id, snap, func(cur *config.Domain) error {
		cur.AdminDNS = msg
		if msg == "ok" {
			cur.AdminOK = adminOK
			cur.AdminReason = adminReason
		}
		return nil
	})
	return err
}

// adminDNSMessage decides the admin_dns text for a row and may call Cloudflare.
// d is the stored row at the start of the update. The return is ok or an error text without secrets.
func (s *Server) adminDNSMessage(ctx context.Context, d config.Domain) string {
	acct := s.cloudflareAccount()
	switch {
	case !config.IsAdminDomain(d):
		return "not an admin domain"
	case !acct.ready():
		return "cloudflare is not configured"
	case s.cfg.PublicIP == "":
		return "public ip is not set"
	case d.CfZoneID == "":
		return "cloudflare zone is not set"
	}
	host := config.NormalizeHTTPHost(d.Host)
	if host == "" {
		return "host required"
	}
	client := s.cloudflareClient(acct)
	z, err := client.zone(ctx, d.CfZoneID)
	if err != nil {
		return sanitizeAdminDNSErr(err)
	}
	if !strings.EqualFold(z.Name, host) {
		return errAdminDNSHost.Error()
	}
	if err := client.upsertProxiedA(ctx, d.CfZoneID, "admin."+host, s.cfg.PublicIP); err != nil {
		return sanitizeAdminDNSErr(err)
	}
	return "ok"
}

// sanitizeAdminDNSErr returns a client-safe Cloudflare error text without secrets.
func sanitizeAdminDNSErr(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, errCFRecordForeign) {
		return errCFRecordForeign.Error()
	}
	var ce *cfError
	if errors.As(err, &ce) {
		return truncateText(ce.Message, cloudflareErrLimit)
	}
	msg := err.Error()
	return truncateText(strings.TrimPrefix(msg, "cloudflare: "), cloudflareErrLimit)
}

// handleDomainAdminDNS creates or updates the admin.<host> Cloudflare record for one domain.
// It returns the updated API row. Errors are 409 when setup is incomplete or an update is running,
// 404 for an unknown id, 403 when the caller may not write the domain, and 502 when Cloudflare fails.
func (s *Server) handleDomainAdminDNS(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Domains == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	id := r.PathValue("id")
	row, found, err := s.domainRowByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	v := s.viewerFrom(r.Context())
	dref := domainRef(row)
	if !v.canSee(resDomains, dref) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !v.canWrite(resDomains, dref) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	host := config.NormalizeHTTPHost(row.Host)
	if host == "" {
		writeErr(w, http.StatusConflict, "host required")
		return
	}
	if !s.adminDNSBusy.acquire(host) {
		writeErr(w, http.StatusConflict, errAdminDNSBusy.Error())
		return
	}
	defer func() {
		if retryID := s.adminDNSBusy.releaseAndRetry(host); retryID != "" {
			s.scheduleAdminDNS(retryID)
		}
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), adminDNSTimeout)
	defer cancel()
	if err := s.ensureAdminDNS(ctx, id); err != nil {
		code, msg := domainErrStatus(err)
		writeErr(w, code, msg)
		return
	}
	updated, found, err := s.domainRowByID(r.Context(), id)
	if err != nil || !found {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	if updated.AdminDNS != "ok" {
		code, msg := adminDNSMessageStatus(updated.AdminDNS)
		writeErr(w, code, msg)
		return
	}
	s.writeDomainRow(w, r.Context(), http.StatusOK, updated)
}

// adminDNSMessageStatus maps a stored admin_dns message to an HTTP status and client text.
func adminDNSMessageStatus(msg string) (int, string) {
	switch msg {
	case "cloudflare is not configured", "public ip is not set", "cloudflare zone is not set",
		errAdminDNSHost.Error(), "not an admin domain":
		return http.StatusConflict, msg
	case "ok":
		return http.StatusOK, msg
	default:
		return http.StatusBadGateway, msg
	}
}

// afterDomainWrite schedules admin DNS when a saved row needs the admin.<host> record.
// item is the stored row; old is nil on create and the previous row on replace.
func (s *Server) afterDomainWrite(item config.Domain, old *config.Domain) {
	if !config.IsAdminDomain(item) || item.CfZoneID == "" {
		return
	}
	became := old == nil || !config.IsAdminDomain(*old)
	if became || item.AdminDNS != "ok" {
		s.scheduleAdminDNS(item.ID)
	}
}
