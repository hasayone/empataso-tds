package main

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/store"
)

// visitUnique decides uniqueness of one request inside the group's window.
// w receives the cookies. r is the request. grp supplies the plan and the window.
// ip is the visitor address. ownStep marks the second step of a click: an /offer
// request that carries a click id this engine issued. A direct visit to /offer
// is a first step and passes false.
// route is the flag for routing, macros and the response: always the key of
// the first step (uniq: in Redis, ztu_ cookie), as on the landing step, so
// the stream choice does not depend on the step.
// logged is the flag written to the event. It equals route except on the
// second step, where it is the first move to the offer in the window: its own
// Redis key (uniqo:) and cookie (ztuo_). While events has no step column the
// second step cannot be told from the first in reports, so it logs route and a
// campaign with a landing does not count a visitor twice.
// Redis failures count as unique. Without Redis and without a cookie both flags
// are true. A live signed cookie is a repeat even when Redis has forgotten the visitor.
func (d *engineDeps) visitUnique(w http.ResponseWriter, r *http.Request, grp *config.Group, ip netip.Addr, ownStep bool) (route, logged bool) {
	route = d.stepUnique(w, r, grp, ip, "ztu_"+grp.ID, false)
	if !ownStep || d.noStepWave.Load() {
		return route, route
	}
	logged = d.stepUnique(w, r, grp, ip, "ztuo_"+grp.ID, true)
	return route, logged
}

// stepUnique decides one uniqueness flag.
//
// w receives a cookie, r is the request, grp is the campaign and its plan,
// ip is the visitor address for Redis. name is the cookie. offer selects the
// offer-step Redis prefix. The user-agent is read from r.
// A legacy cookie campaign uses the plain cookie and does not touch Redis.
// A signed cookie that is still live is a repeat and is not renewed.
// Otherwise Redis decides, capped at its ceiling, and a campaign with the cookie
// flag then sets a signed cookie for the hours field. It returns true when the
// visitor is new.
func (d *engineDeps) stepUnique(w http.ResponseWriter, r *http.Request, grp *config.Group, ip netip.Addr, name string, offer bool) bool {
	plan := config.UniqPlanOf(grp)
	if plan.LegacyCookie {
		return cookieUniqueNamed(w, r, grp, name)
	}
	now := time.Now()
	if plan.Cookie && readUniqCookie(r, d.cids, grp.ID, name, now) {
		return false
	}
	unique := true
	if d.counters != nil {
		prefix := "uniq:"
		if offer {
			prefix = "uniqo:"
		}
		visitor := store.UniqIdentity(ip, r.Header.Get("User-Agent"), plan.ByUA)
		unique, _ = d.counters.Touch(r.Context(), prefix, grp.ID, visitor, secs(grp.UniqSeconds))
	}
	if plan.Cookie {
		setUniqCookie(w, r, d.cids, grp, name, now)
	}
	return unique
}
