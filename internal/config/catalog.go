package config

import (
	"net/url"
	"strconv"
	"strings"
)

// MaxSubID is the highest named sub_id the engine stores. OpenAPI for the
// traffic source lists sub_id_1 through sub_id_15; higher numbers are ignored.
const MaxSubID = 15

// ShareRef points a stream at an offer or a landing and carries its share.
type ShareRef struct {
	ID    string `json:"id"`
	Share int    `json:"share"`
}

// Offer is an external destination. Local records stay visible and are never fetched.
type Offer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    bool   `json:"status"`
	Local     bool   `json:"local,omitempty"`
	NetworkID string `json:"network_id,omitempty"`
	// Group is a label stored on this offer. The set of groups is these labels.
	Group string `json:"group,omitempty"`
}

// Domain is a host parked on this tracker.
// Role is admin, links or both. Primary marks the default base for campaign links.
// OK is set only after a check received this tracker's own answer on that host.
type Domain struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Role     string `json:"role"`
	Primary  bool   `json:"primary,omitempty"`
	OK       bool   `json:"ok,omitempty"`
	Checked  string `json:"checked,omitempty"`
}

// AffiliateNetwork is a partner that sends the inbound postback.
// PostbackURL is a note for the partner cabinet. The engine does not call it.
// OfferParam is one catalog token. OfferQuery is appended to the visitor URL
// after the offer has already been identified, for example sub1={subid}.
type AffiliateNetwork struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      bool   `json:"status"`
	PostbackURL string `json:"postback_url"`
	OfferParam  string `json:"offer_param,omitempty"`
	OfferQuery  string `json:"offer_query,omitempty"`
}

// Landing is an external landing page. Local records stay visible and are never fetched.
type Landing struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status bool   `json:"status"`
	Local  bool   `json:"local,omitempty"`
}

// TrafficSource names the query keys a partner uses for keyword and sub_id_1..15.
type TrafficSource struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

// OutboundPostback is a server-to-server template fired after an inbound conversion.
type OutboundPostback struct {
	URL      string   `json:"url"`
	Method   string   `json:"method"`
	Statuses []string `json:"statuses,omitempty"`
}

// MatchesStatus reports whether the template fires for status.
// An empty status list fires for every status, including an empty one.
func (p OutboundPostback) MatchesStatus(status string) bool {
	if len(p.Statuses) == 0 {
		return true
	}
	for _, s := range p.Statuses {
		if strings.EqualFold(strings.TrimSpace(s), status) {
			return true
		}
	}
	return false
}

// ApplySource reads keyword and sub_id_1..15 from the query.
// A nil source uses the macro name itself as the query key.
// The keyword return is empty when the source does not map one or the query has none.
func ApplySource(q url.Values, src *TrafficSource) (keyword string, subs [MaxSubID]string) {
	if src != nil {
		if key := strings.TrimSpace(src.Parameters["keyword"]); key != "" {
			keyword = q.Get(key)
		}
	}
	for i := 1; i <= MaxSubID; i++ {
		name := "sub_id_" + strconv.Itoa(i)
		qk := name
		if src != nil {
			if mapped := strings.TrimSpace(src.Parameters[name]); mapped != "" {
				qk = mapped
			}
		}
		subs[i-1] = q.Get(qk)
	}
	return keyword, subs
}

// AllowedSourceParam reports whether name is a traffic-source parameter the
// engine understands. sub_id_16 and above are rejected.
func AllowedSourceParam(name string) bool {
	if name == "keyword" {
		return true
	}
	n, ok := strings.CutPrefix(name, "sub_id_")
	if !ok {
		return false
	}
	i, err := strconv.Atoi(n)
	return err == nil && i >= 1 && i <= MaxSubID
}
