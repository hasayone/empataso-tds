package config

import (
	"crypto/sha1"
	"encoding/hex"
	"math"
	"net/url"
	"sort"
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
	// CreatedBy is the users.json id of the panel user who created this record.
	// Empty means the environment administrator or a record older than this field.
	// The engine does not read it.
	CreatedBy string `json:"created_by,omitempty"`
	// Countries lists ISO 3166-1 alpha-2 codes in lowercase, for example ru.
	Countries []string `json:"countries,omitempty"`
	// PayoutModel is the partner payout type: cpa or cpc.
	PayoutModel string `json:"payout_model,omitempty"`
	// Payout is the payout amount in PayoutCurrency.
	Payout float64 `json:"payout,omitempty"`
	// PayoutCurrency is a three-letter currency code, for example USD.
	PayoutCurrency string `json:"payout_currency,omitempty"`
	// PayoutParam reports whether the payout value is sent as a query parameter.
	PayoutParam bool `json:"payout_param,omitempty"`
}

// Domain is a host parked on this tracker.
// Role is admin, links or both. Primary marks the default base for campaign links.
// OK is set only after a check received this tracker's own answer on that host.
// Reason is the code of the last failed check (dns, private, connect, foreign).
// It is empty when OK is true.
type Domain struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Group   string `json:"group,omitempty"`
	Role    string `json:"role"`
	Primary bool   `json:"primary,omitempty"`
	OK      bool   `json:"ok,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Checked string `json:"checked,omitempty"`
	// CreatedBy is the users.json id of the panel user who created this record.
	// Empty means the environment administrator or a record older than this field.
	// The engine does not read it.
	CreatedBy string `json:"created_by,omitempty"`
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
	// Group is a label stored on this record. The set of groups is these labels.
	Group string `json:"group,omitempty"`
	// CreatedBy is the users.json id of the panel user who created this record.
	// Empty means the environment administrator or a record older than this field.
	// The engine does not read it.
	CreatedBy string `json:"created_by,omitempty"`
}

// Landing is a page shown before an offer.
// A remote landing is fetched by URL. A local landing is a directory of files
// served by the engine; Entry names the file inside that directory.
type Landing struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status bool   `json:"status"`
	Local  bool   `json:"local,omitempty"`
	// Entry is the file served from the landing directory, for example
	// index.php or index.html. Empty selects index.php when that file
	// exists, and index.html otherwise.
	Entry string `json:"entry,omitempty"`
	// Group is a label stored on this record. The set of groups is these labels.
	Group string `json:"group,omitempty"`
	// CreatedBy is the users.json id of the panel user who created this record.
	// Empty means the environment administrator or a record older than this field.
	// The engine does not read it.
	CreatedBy string `json:"created_by,omitempty"`
}

// TrafficSource names the query keys a partner uses for keyword and sub_id_1..15.
type TrafficSource struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Parameters map[string]string `json:"parameters,omitempty"`
	// Group is a label stored on this record. The set of groups is these labels.
	Group string `json:"group,omitempty"`
	// CreatedBy is the users.json id of the panel user who created this record.
	// Empty means the environment administrator or a record older than this field.
	// The engine does not read it.
	CreatedBy string `json:"created_by,omitempty"`
}

// OutboundPostback is a server-to-server template fired after an inbound conversion.
// ID and Name are empty on campaign templates saved before the shared catalog.
type OutboundPostback struct {
	ID       string   `json:"id,omitempty"`
	Name     string   `json:"name,omitempty"`
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

// Signature returns the canonical text of a template.
// p is the receiver. Method case, status case and the order of statuses do not change the result.
// An empty method is GET. The URL and each status are trimmed. Statuses are lowercased. Blank statuses are dropped.
// The return is method, URL and the sorted statuses, separated by newlines; statuses are comma-separated.
// There is no error.
func (p OutboundPostback) Signature() string {
	method := strings.ToUpper(strings.TrimSpace(p.Method))
	if method == "" {
		method = "GET"
	}
	stats := make([]string, 0, len(p.Statuses))
	for _, s := range p.Statuses {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		stats = append(stats, s)
	}
	sort.Strings(stats)
	return method + "\n" + strings.TrimSpace(p.URL) + "\n" + strings.Join(stats, ",")
}

// PostbackID returns the catalog id for a template signature.
// sig is the text from OutboundPostback.Signature. Any other string is hashed as given.
// The return is "pb-" plus the first 10 hex characters of SHA-1(sig).
// There is no error.
func PostbackID(sig string) string {
	sum := sha1.Sum([]byte(sig))
	return "pb-" + hex.EncodeToString(sum[:])[:10]
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

// IncomingCost reads a click price from the query.
// costKey and curKey are the campaign's query names. It returns cents and the
// currency when the value is a positive number and the currency is one the
// spend list knows. Otherwise ok is false and the click has no price of its own.
func IncomingCost(q url.Values, costKey, curKey string) (cents int64, cur string, ok bool) {
	costKey = strings.TrimSpace(costKey)
	curKey = strings.TrimSpace(curKey)
	if q == nil || costKey == "" || curKey == "" {
		return 0, "", false
	}
	cur = strings.ToUpper(strings.TrimSpace(q.Get(curKey)))
	if !AllowedCurrency(cur) {
		return 0, "", false
	}
	raw := strings.TrimSpace(q.Get(costKey))
	if raw == "" {
		return 0, "", false
	}
	if c, err := ParseCents(raw); err == nil {
		return c, cur, true
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", "."), 64)
	if err != nil || f <= 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, "", false
	}
	cents = int64(math.Round(f * 100))
	if cents <= 0 {
		return 0, "", false
	}
	return cents, cur, true
}

// ExtraSourceParams are the click fields a traffic source may rename.
// The admin and the import accept this list together with keyword and
// sub_id_1 through sub_id_15. Without a mapping each name is the query key.
var ExtraSourceParams = []string{"site", "ad_campaign_id", "creative_id", "external_id"}

// extraValueMax is the longest stored value of one extra field, in bytes.
const extraValueMax = 255

// Extras is the site, ad campaign, creative and external id of one click.
// Each field is trimmed and at most extraValueMax bytes. An empty field
// means the query did not carry that value.
type Extras struct {
	Site         string
	AdCampaignID string
	CreativeID   string
	ExternalID   string
}

// sourceParamKey returns the query key for a traffic-source parameter.
// src may be nil. A non-empty Parameters value replaces name; otherwise
// name itself is the key. There is no error.
func sourceParamKey(src *TrafficSource, name string) string {
	if src != nil {
		if mapped := strings.TrimSpace(src.Parameters[name]); mapped != "" {
			return mapped
		}
	}
	return name
}

// limitBytes returns s cut to at most n bytes.
// n <= 0 or an empty s yields an empty string. A shorter s is unchanged.
// There is no error.
func limitBytes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SourceExtras reads site, ad_campaign_id, creative_id and external_id.
// q is the request query and may be nil. src is the campaign traffic
// source, or nil when the campaign has none. A non-empty mapped name
// selects that query key instead of the field name. Values are trimmed
// and cut to 255 bytes. There is no error.
func SourceExtras(q url.Values, src *TrafficSource) Extras {
	var out Extras
	for _, name := range ExtraSourceParams {
		v := limitBytes(strings.TrimSpace(q.Get(sourceParamKey(src, name))), extraValueMax)
		switch name {
		case "site":
			out.Site = v
		case "ad_campaign_id":
			out.AdCampaignID = v
		case "creative_id":
			out.CreativeID = v
		case "external_id":
			out.ExternalID = v
		}
	}
	return out
}

// AllowedSourceParam reports whether name is a traffic-source parameter the
// engine understands. keyword, ExtraSourceParams and sub_id_1 through
// sub_id_15 are accepted. sub_id_16 and any other name are rejected.
func AllowedSourceParam(name string) bool {
	if name == "keyword" {
		return true
	}
	for _, p := range ExtraSourceParams {
		if name == p {
			return true
		}
	}
	n, ok := strings.CutPrefix(name, "sub_id_")
	if !ok {
		return false
	}
	i, err := strconv.Atoi(n)
	return err == nil && i >= 1 && i <= MaxSubID
}
