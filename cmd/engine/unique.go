package main

import (
	"net/http"
	"net/netip"

	"github.com/egerkuzma/kuztds/internal/config"
)

// visitUnique decides uniqueness of one request inside the group's window.
// w receives the cookies for the cookie method. r is the request. grp supplies
// the method and the window. ip is the visitor address. offerStep marks the
// offer step.
// route is the flag for routing, macros and the response: always the key of
// the first step (uniq: in Redis, ztu_ cookie), as on the landing step, so
// the stream choice does not depend on the step.
// logged is the flag written to the event. It equals route except on the
// offer step, where it is the first move to the offer in the window: its own
// Redis key (uniqo:) and cookie (ztuo_).
// Redis failures count as unique. Without Redis both flags are true.
func (d *engineDeps) visitUnique(w http.ResponseWriter, r *http.Request, grp *config.Group, ip netip.Addr, offerStep bool) (route, logged bool) {
	cookie := grp.UniqMethod == "cookie"
	ttl := secs(grp.UniqSeconds)
	route = true
	switch {
	case cookie:
		route = cookieUnique(w, r, grp)
	case d.counters != nil:
		route, _ = d.counters.Unique(r.Context(), grp.ID, ip, ttl)
	}
	if !offerStep {
		return route, route
	}
	logged = true
	switch {
	case cookie:
		logged = cookieUniqueNamed(w, r, grp, "ztuo_"+grp.ID)
	case d.counters != nil:
		logged, _ = d.counters.UniqueOffer(r.Context(), grp.ID, ip, ttl)
	}
	return route, logged
}
