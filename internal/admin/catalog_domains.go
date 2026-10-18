package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

func registerDomains(s *Server, mux *http.ServeMux) {
	mountCRUD(s, mux, "/api/domains", s.domainCatalog(prepDomain))
}

// domainCatalog builds the domain catalog used by /api/domains.
// prep normalizes a record before it is validated and stored. The HTTP routes pass
// prepDomain; registration passes prepRegistered so its registrar fields survive.
// It returns the catalog value, which holds no state of its own.
func (s *Server) domainCatalog(prep func(*config.Domain)) crudCatalog[config.Domain] {
	return crudCatalog[config.Domain]{
		s: s, store: s.cfg.Domains,
		idOf:  func(o config.Domain) string { return o.ID },
		setID: func(o *config.Domain, id string) { o.ID = id },
		valid: validDomain,
		hooks: &catalogHooks[config.Domain]{
			seq: s.cfg.SeqDomains, floor: s.domainFloor, prep: prep, tidy: tidyDomains, view: s.domainRows,
			mu:           &s.domainMu,
			keep:         keepDomainCheck,
			guard:        s.guardDomainPut,
			guardDelete:  s.guardDomainDelete,
			beforeInsert: s.guardDomainInsert,
			preDelete:    s.forceDeleteDomain,
			res:          resDomains,
			label:        func(d config.Domain) string { return d.Group },
			owner:        func(d config.Domain) string { return d.CreatedBy },
			setOwner:     func(d *config.Domain, uid string) { d.CreatedBy = uid },
			userLimit:    limitDomainUser,
		},
	}
}

func validDomain(d config.Domain) string {
	if msg := validCatalogID(d.ID); msg != "" {
		return msg
	}
	host := strings.TrimSpace(d.Host)
	if host == "" || strings.Contains(host, "://") || strings.ContainsAny(host, " /\\") {
		return "host required"
	}
	switch d.Role {
	case "admin", "links", "both":
	default:
		return "role must be admin, links or both"
	}
	return ""
}

// prepDomain normalizes host, role and group on d before validation.
// d is the decoded request body. OK, Reason, Checked and the registrar fields are
// server-owned and are cleared here.
func prepDomain(d *config.Domain) {
	d.Host = strings.ToLower(strings.TrimSpace(d.Host))
	if d.Role == "" {
		d.Role = "both"
	}
	d.Group = trimGroupLabel(d.Group)
	d.OK = false
	d.Reason = ""
	d.Checked = ""
	d.Registrar, d.RegistrarAccount, d.RegistrarStatus, d.RegistrarCharged, d.RegistrarExpected = "", "", "", 0, 0
	d.RegistrarPendingSince = ""
}

// prepRegistered is prepDomain for a row created by domain registration.
// d carries the registrar fields set by the server; they are kept through the normalization.
func prepRegistered(d *config.Domain) {
	registrar, account, status, charged, expected := d.Registrar, d.RegistrarAccount, d.RegistrarStatus, d.RegistrarCharged, d.RegistrarExpected
	since := d.RegistrarPendingSince
	prepDomain(d)
	d.Registrar, d.RegistrarAccount, d.RegistrarStatus, d.RegistrarCharged, d.RegistrarExpected = registrar, account, status, charged, expected
	d.RegistrarPendingSince = since
}

// keepDomainCheck carries OK, Reason, Checked and the registrar fields from old into item
// when the host is unchanged.
// item is the prepared replacement; old is the stored record. A different host leaves the
// cleared fields, because the previous probe and the registration belong to another name.
// It returns nothing.
func keepDomainCheck(item *config.Domain, old config.Domain) {
	if item.Host != old.Host {
		return
	}
	item.OK, item.Reason, item.Checked = old.OK, old.Reason, old.Checked
	item.Registrar, item.RegistrarAccount = old.Registrar, old.RegistrarAccount
	item.RegistrarStatus, item.RegistrarCharged, item.RegistrarExpected = old.RegistrarStatus, old.RegistrarCharged, old.RegistrarExpected
	item.RegistrarPendingSince = old.RegistrarPendingSince
}

// purchaseCatalog is the domain catalog used by registration to write the pending row.
// Its insert refuses a host that already has any row, case ignored, so the check and the
// write happen together under domainMu and a duplicate is refused before domains.create.
func (s *Server) purchaseCatalog() crudCatalog[config.Domain] {
	c := s.domainCatalog(prepRegistered)
	c.hooks.beforeInsert = guardPurchaseInsert
	return c
}

// guardPurchaseInsert refuses the pending row of a purchase when its host is already stored.
// item is the prepared row and list the stored domains. It returns 409 "already exists",
// or 0 when the host is free.
func guardPurchaseInsert(item config.Domain, list []config.Domain) (int, string) {
	for _, d := range list {
		if strings.EqualFold(d.Host, item.Host) {
			return http.StatusConflict, "already exists"
		}
	}
	return 0, ""
}

// guardDomainInsert refuses a new domain on a host that is being bought.
// item is the prepared row and list the stored domains. A running purchase of the host or
// a pending row with the same host, case ignored, answers 409 "registration in progress".
// It returns 0 when the host is free.
func (s *Server) guardDomainInsert(item config.Domain, list []config.Domain) (int, string) {
	if s.ncBuy.has(item.Host) {
		return http.StatusConflict, "registration in progress"
	}
	for _, d := range list {
		if d.RegistrarStatus == registrarPending && strings.EqualFold(d.Host, item.Host) {
			return http.StatusConflict, "registration in progress"
		}
	}
	return 0, ""
}

// domainBusy reports whether d is a purchase that has not been confirmed: its row is
// pending or a registration of its host is running.
func (s *Server) domainBusy(d config.Domain) bool {
	return d.RegistrarStatus == registrarPending || s.ncBuy.has(d.Host)
}

// guardDomainPut protects a domain that is being bought.
// item is the prepared replacement and old the stored row. A pending or running purchase
// keeps its host and stays disabled, so a rename answers 409 "pending" and a PUT cannot
// enable the row; the other fields are saved. A rename onto a host that is being bought
// answers 409 "registration in progress". It returns 0 when the write may go on.
func (s *Server) guardDomainPut(item *config.Domain, old config.Domain) (int, string) {
	if item.Host != old.Host && s.ncBuy.has(item.Host) {
		return http.StatusConflict, "registration in progress"
	}
	if !s.domainBusy(old) {
		return 0, ""
	}
	if item.Host != old.Host {
		return http.StatusConflict, registrarPending
	}
	item.Disabled = true
	return 0, ""
}

// guardDomainDelete refuses to delete a domain that is being bought.
// old is the stored row. It returns 409 "pending" for a pending row or a running
// purchase and 0 otherwise.
func (s *Server) guardDomainDelete(old config.Domain) (int, string) {
	if s.domainBusy(old) {
		return http.StatusConflict, registrarPending
	}
	return 0, ""
}

const domainUserLimitMsg = "primary and admin role are set by an administrator"

// limitDomainUser rejects domain fields a panel user may not set.
// item is the request body; old is nil on POST and the stored row on PUT.
// It returns a 403 message, or an empty string when the fields are allowed.
// prepDomain runs after this hook on POST, so empty Role becomes links here first.
func limitDomainUser(item *config.Domain, old *config.Domain) string {
	if old == nil {
		if item.Role == "" {
			item.Role = "links"
		}
		if item.Primary || item.Role == "admin" || item.Role == "both" {
			return domainUserLimitMsg
		}
		return ""
	}
	item.Primary = old.Primary
	item.Role = old.Role
	return ""
}

// tidyDomains enforces a single primary domain. list is the full catalog; id is the row
// create or replace touched. It returns the updated slice. A disabled domain is never primary.
func tidyDomains(list []config.Domain, id string) []config.Domain {
	for i := range list {
		if list[i].ID == id && list[i].Disabled {
			list[i].Primary = false
		}
	}
	primary := false
	for _, d := range list {
		if d.ID == id && d.Primary {
			primary = true
		}
	}
	if !primary {
		return list
	}
	for i := range list {
		if list[i].ID != id {
			list[i].Primary = false
		}
	}
	return list
}

// domainRow is one parked domain and the number of campaigns that use it.
type domainRow struct {
	config.Domain
	Campaigns int `json:"campaigns"`
}

// domainRows attaches a campaign count to each domain for GET /api/domains.
// list is the stored domains. A nil Groups store counts every domain as zero.
// It returns a non-nil slice, empty when list is empty, so the JSON body is an array.
// An error comes from Groups.List.
func (s *Server) domainRows(ctx context.Context, list []config.Domain) (any, error) {
	counts := map[string]int{}
	if s.cfg.Groups != nil {
		groups, err := s.cfg.Groups.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if g.DomainID != "" {
				counts[g.DomainID]++
			}
		}
	}
	rows := make([]domainRow, 0, len(list))
	for _, d := range list {
		rows = append(rows, domainRow{Domain: d, Campaigns: counts[d.ID]})
	}
	return rows, nil
}
