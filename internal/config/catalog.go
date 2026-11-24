package config

import (
	"crypto/sha1"
	"encoding/hex"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxSubID is the highest named sub_id the engine stores. OpenAPI for the
// traffic source lists sub_id_1 through sub_id_30; higher numbers are ignored.
const MaxSubID = 30

// ShareRef points a stream at an offer or a landing and carries its share.
type ShareRef struct {
	ID    string `json:"id"`
	Share int    `json:"share"`
}

// Offer is an external destination. Local records stay visible and are never fetched.
type Offer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL        string `json:"url"`
	Status     bool   `json:"status"`
	Local      bool   `json:"local,omitempty"`
	// Action is the click answer instead of a redirect to URL.
	// Empty means the URL redirect. The engine matches the token and does not expand it.
	// 404 answers 404, text and html show ActionBody, none does not redirect.
	Action string `json:"action,omitempty"`
	// ActionBody is the text or HTML of a text or html action.
	// The engine expands macros in it. Empty for 404 and none.
	ActionBody string `json:"action_body,omitempty"`
	NetworkID  string `json:"network_id,omitempty"`
	// Group is a label stored on this offer. The set of groups is these labels.
	Group string `json:"group,omitempty"`
	// CreatedBy is the users.json id of the panel user who created this record.
	// Empty means the environment administrator or a record older than this field.
	// The engine does not read it.
	CreatedBy string `json:"created_by,omitempty"`
	// BoardID is the panel section id for this record. Empty means the main section.
	// The engine does not read it.
	BoardID string `json:"board_id,omitempty"`
	// Countries lists ISO 3166-1 alpha-2 codes in lowercase, for example ru.
	Countries []string `json:"countries,omitempty"`
	// PayoutModel is the partner payout type: cpa or cpc.
	PayoutModel string `json:"payout_model,omitempty"`
	// Payout is the payout amount in PayoutCurrency.
	Payout float64 `json:"payout,omitempty"`
	// PayoutCurrency is a three-letter currency code, for example USD.
	PayoutCurrency string `json:"payout_currency,omitempty"`
	// PayoutParam is the "payout parameter" tick: the conversion takes its payout
	// from the payout parameter of the postback, and Payout in PayoutCurrency is
	// the reserve for a postback that has none.
	PayoutParam bool `json:"payout_param,omitempty"`
	// Values are the named values of the offer. The engine puts the encoded value
	// in place of {offer_value:name} in the offer URL.
	Values []OfferValue `json:"values,omitempty"`
	// Notes is a free text of the operator. The engine does not read it.
	Notes string `json:"notes,omitempty"`
	// Upsell counts a later sale of the same click as another sale when the postback
	// carries a tid this click has not had. Off means a repeat of the click is not another sale.
	Upsell bool `json:"upsell,omitempty"`
	// ConvCap turns the daily conversion limit on. ConvCapLimit is its number N:
	// once N leads and sales have been written for the offer in the current day
	// of CapTZ, the offer hands the click to FallbackID until the next day there.
	ConvCap      bool `json:"conv_cap,omitempty"`
	ConvCapLimit int  `json:"conv_cap_limit,omitempty"`
	// ConvCapOfferID is a hand-over offer saved before the single alternative.
	// The engine uses FallbackID. The field is kept so a save does not drop it.
	ConvCapOfferID string `json:"conv_cap_offer_id,omitempty"`
	// ConvTotal turns the total conversion limit on. ConvTotalLimit is its number N: once the
	// offer has N leads and sales over all time, a click goes to FallbackID.
	ConvTotal      bool `json:"conv_total,omitempty"`
	ConvTotalLimit int  `json:"conv_total_limit,omitempty"`
	// ConvTotalOfferID is a hand-over offer saved before the single alternative.
	// The engine uses FallbackID. The field is kept so a save does not drop it.
	ConvTotalOfferID string `json:"conv_total_offer_id,omitempty"`
	// CapTZ is the IANA zone whose midnight ends the daily limit. Empty means the
	// counters stay on the UTC day they were written on, until the panel stores
	// the statistics zone.
	CapTZ string `json:"cap_tz,omitempty"`
	// FallbackID is the alternative offer. A click meant for this offer goes there
	// when this offer is stopped or a conversion limit is used up, and is counted
	// there. Empty means the offer is skipped. A random offer is never chosen.
	FallbackID string `json:"fallback_id,omitempty"`
}

// CapHit tells which conversion limits of an offer are used up: a set of CapDaily and CapTotal.
type CapHit uint8

const (
	// CapDaily marks the daily limit as used up in the offer's current day.
	CapDaily CapHit = 1 << iota
	// CapTotal marks the total limit as used up.
	CapTotal
)

// Limits of the free fields of an offer.
const (
	// MaxOfferValues is the number of named values one offer may carry.
	MaxOfferValues = 50
	// MaxOfferValueName and MaxOfferValueText are the byte limits of a value name and a value.
	MaxOfferValueName = 64
	MaxOfferValueText = 1024
	// MaxOfferNotes is the byte limit of the notes.
	MaxOfferNotes = 10000
	// MaxConvCapLimit is the highest conversion limit, daily or total.
	MaxConvCapLimit = 1000000
	// OfferAction404 answers the click with HTTP 404.
	OfferAction404 = "404"
	// OfferActionText shows ActionBody as text/plain.
	OfferActionText = "text"
	// OfferActionHTML shows ActionBody as text/html, without wrapping it in a page.
	OfferActionHTML = "html"
	// OfferActionNone does not redirect and does not replace the page.
	OfferActionNone = "none"
	// MaxOfferActionBody is the byte limit of the text or HTML of an action.
	MaxOfferActionBody = 100000
)

// OfferValue is one named value of an offer.
type OfferValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// OfferValueToken returns the macro that stands for the value called name in an offer URL.
func OfferValueToken(name string) string {
	return "{offer_value:" + name + "}"
}

// ApplyValues puts the values of the offer into raw.
// raw is the offer URL template. Every {offer_value:name} of a value the offer
// has is replaced by that value, percent-encoded as a query component, so the
// result never holds a brace or bracket that the macro pass could read again.
// A placeholder of a name the offer does not have stays as written. It does
// not fail.
func (o Offer) ApplyValues(raw string) string {
	if len(o.Values) == 0 || !strings.Contains(raw, "{offer_value:") {
		return raw
	}
	pairs := make([]string, 0, len(o.Values)*2)
	for _, v := range o.Values {
		name := strings.TrimSpace(v.Name)
		if name == "" {
			continue
		}
		pairs = append(pairs, OfferValueToken(name), url.QueryEscape(v.Value))
	}
	return strings.NewReplacer(pairs...).Replace(raw)
}

// OfferCountryUnknown is the entry of Offer.Countries for a click whose country the engine could not tell.
const OfferCountryUnknown = "unknown"

// AllowsCountry reports whether the offer may be served to a click from the country.
// country is the click country as the engine holds it: two letters in any case, or anything else
// (the "no data" dash included) for a click of unknown country. An offer without countries takes every
// click. With countries it takes a click whose code is in the list; a click of unknown country is taken
// only when the list has OfferCountryUnknown. Codes in the list are compared without case and space.
func (o Offer) AllowsCountry(country string) bool {
	if len(o.Countries) == 0 {
		return true
	}
	cc := strings.ToLower(strings.TrimSpace(country))
	if len(cc) != 2 {
		cc = OfferCountryUnknown
	}
	for _, code := range o.Countries {
		if strings.ToLower(strings.TrimSpace(code)) == cc {
			return true
		}
	}
	return false
}

// offerActionKind is the stored action token: trimmed, compared without case.
func offerActionKind(action string) string {
	return strings.ToLower(strings.TrimSpace(action))
}

// ActionReady reports whether the offer answers a click without its URL.
// 404 and none are ready on their own. text and html are ready only when
// ActionBody has a non-space character. An unknown action is not ready:
// the click stays a URL redirect. It does not fail.
func (o Offer) ActionReady() bool {
	switch offerActionKind(o.Action) {
	case OfferAction404, OfferActionNone:
		return true
	case OfferActionText, OfferActionHTML:
		return strings.TrimSpace(o.ActionBody) != ""
	default:
		return false
	}
}

// ClickAnswer tells how a click of this offer is answered.
// redirect is the render type, or empty when out is the URL and the caller
// keeps the stream redirect. out is that URL or the action body.
// ok is false when the offer cannot take the click. An unknown action, and
// text or html with an empty body, fall back to the URL. It does not fail.
func (o Offer) ClickAnswer() (redirect, out string, ok bool) {
	switch offerActionKind(o.Action) {
	case OfferAction404:
		return "404_not_found", "", true
	case OfferActionText:
		if strings.TrimSpace(o.ActionBody) != "" {
			return "show_text", o.ActionBody, true
		}
	case OfferActionHTML:
		if strings.TrimSpace(o.ActionBody) != "" {
			return "show_html", o.ActionBody, true
		}
	case OfferActionNone:
		return "stop", "", true
	}
	if o.Local || strings.TrimSpace(o.URL) == "" {
		return "", "", false
	}
	return "", o.URL, true
}

// SendsURL reports whether this offer sends the visitor to its URL.
// A ready action reports false, so a network query is not appended to a page.
// It does not fail.
func (o Offer) SendsURL() bool {
	kind, _, ok := o.ClickAnswer()
	return ok && kind == ""
}

// CapLimit is the daily conversion limit of the offer.
// It returns the number N and true when the limit is on and N is above zero;
// an offer with the limit off reports false whatever N holds.
func (o Offer) CapLimit() (int, bool) {
	if !o.ConvCap || o.ConvCapLimit <= 0 {
		return 0, false
	}
	return o.ConvCapLimit, true
}

// TotalLimit is the total conversion limit of the offer.
// It returns the number N and true when the limit is on and N is above zero;
// an offer with the limit off reports false whatever N holds.
func (o Offer) TotalLimit() (int, bool) {
	if !o.ConvTotal || o.ConvTotalLimit <= 0 {
		return 0, false
	}
	return o.ConvTotalLimit, true
}

// CapRedirect is the alternative offer that takes the clicks while a limit is used up.
// hit holds the limits that are used up. The alternative is FallbackID, the same offer
// a stopped offer hands its clicks to. It returns "" when hit is empty or FallbackID is
// empty, which means the offer is skipped. A random offer is never chosen.
func (o Offer) CapRedirect(hit CapHit) string {
	if hit == 0 {
		return ""
	}
	return strings.TrimSpace(o.FallbackID)
}

// ClickRevenue is the payout a click of this offer earns at once.
// It returns the amount and true only for cpc, a finite amount above zero and
// a currency the revenue is kept in: USD or none. cpa and an empty model return
// false, so that amount waits for a conversion and is not written on the click.
func (o Offer) ClickRevenue() (float64, bool) {
	if strings.ToLower(strings.TrimSpace(o.PayoutModel)) != "cpc" {
		return 0, false
	}
	if !(o.Payout > 0) || math.IsInf(o.Payout, 0) {
		return 0, false
	}
	switch strings.ToUpper(strings.TrimSpace(o.PayoutCurrency)) {
	case "", "USD":
		return o.Payout, true
	}
	return 0, false
}

// ReservePayout is the payout a conversion of this offer gets when its postback
// carries no payout number.
// It returns the amount and true only when the offer has the payout parameter
// tick, a finite amount above zero and a currency the revenue is kept in: USD
// or none. Conversions have no currency column and no rates exist, so an amount
// in another currency is not written as dollars.
func (o Offer) ReservePayout() (float64, bool) {
	if strings.ToLower(strings.TrimSpace(o.PayoutModel)) == "cpc" {
		return 0, false
	}
	if !o.PayoutParam || !(o.Payout > 0) || math.IsInf(o.Payout, 0) {
		return 0, false
	}
	switch strings.ToUpper(strings.TrimSpace(o.PayoutCurrency)) {
	case "", "USD":
		return o.Payout, true
	}
	return 0, false
}

// Domain is a host parked on this tracker.
// Role is admin, links or both. Primary marks the default base for campaign links.
// Disabled is a catalog mark only.
// OK is set only after a check received this tracker's own answer on that host.
// Reason is the code of the last failed check (dns, private, connect, tls, untrusted, foreign,
// elsewhere, cf_pending). untrusted means this server answered and the certificate is not trusted.
// ReasonDetail explains it: the resolved addresses for elsewhere,
// the Cloudflare name servers for cf_pending. Both are empty when OK is true.
type Domain struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Group   string `json:"group,omitempty"`
	Role    string `json:"role"`
	Primary bool   `json:"primary,omitempty"`
	// Disabled hides the host from new campaign links. The engine still reads the
	// host when it decides whether a redirect stays on this tracker, and a
	// disabled host counts as its own. Clicks on the host are still accepted.
	Disabled bool   `json:"disabled,omitempty"`
	OK       bool   `json:"ok,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Checked  string `json:"checked,omitempty"`
	// CreatedBy is the users.json id of the panel user who created this record.
	// Empty means the environment administrator or a record older than this field.
	// The engine does not read it.
	CreatedBy string `json:"created_by,omitempty"`
	// Registrar and RegistrarAccount name the registrar and the account that registered
	// the domain through the panel. RegistrarStatus is "pending" while the outcome of a
	// registration is not confirmed. RegistrarCharged is the amount the registrar reported
	// as charged and stays zero while that amount is unknown. RegistrarExpected is the total
	// the panel priced before the purchase. RegistrarPendingSince is the UTC time in RFC 3339
	// at which the pending row was written; a pending row without it counts as old.
	// The admin API owns these fields and the engine does not read them.
	Registrar         string  `json:"registrar,omitempty"`
	RegistrarAccount  string  `json:"registrar_account,omitempty"`
	RegistrarStatus   string  `json:"registrar_status,omitempty"`
	RegistrarCharged  float64 `json:"registrar_charged,omitempty"`
	RegistrarExpected float64 `json:"registrar_expected,omitempty"`
	// RegistrarPendingSince is documented above.
	RegistrarPendingSince string `json:"registrar_pending_since,omitempty"`
	// ReasonDetail is the text that goes with Reason, set by the admin check.
	ReasonDetail string `json:"reason_detail,omitempty"`
	// CfZoneID is the Cloudflare zone of the host. CfStatus is connect_pending while the
	// panel still has to create the zone and records, pending while Cloudflare waits for the
	// name servers, and active once the zone is live. CfNS lists the zone name servers.
	CfZoneID string   `json:"cf_zone_id,omitempty"`
	CfStatus string   `json:"cf_status,omitempty"`
	CfNS     []string `json:"cf_ns,omitempty"`
	// ConnectedAt is the UTC time in RFC 3339 of the first check that found the host
	// answering as this tracker, and stays after later failures. ConnectSeen is set once the
	// operator has seen the notice about it.
	ConnectedAt string `json:"connected_at,omitempty"`
	ConnectSeen bool   `json:"connect_seen,omitempty"`
	// WatchSince is the UTC time in RFC 3339 at which the host entered the background
	// watch: the confirmed purchase or the first Cloudflare connection. It sets the watch
	// window and the point where checks slow down.
	// The admin API owns all of these fields and the engine does not read them.
	WatchSince string `json:"watch_since,omitempty"`
	// AdminOK is set after a check received this admin API on admin.<host>.
	AdminOK bool `json:"admin_ok,omitempty"`
	// AdminReason is the code of the last failed admin check: dns, tls, connect or foreign.
	AdminReason string `json:"admin_reason,omitempty"`
	// AdminDNS is ok when the Cloudflare record admin.<host> exists, or the error text otherwise.
	AdminDNS string `json:"admin_dns,omitempty"`
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
	// BoardID is the panel section id for this record. Empty means the main section.
	// The engine does not read it.
	BoardID string `json:"board_id,omitempty"`
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
	// BoardID is the panel section id for this record. Empty means the main section.
	// The engine does not read it.
	BoardID string `json:"board_id,omitempty"`
}

// TrafficSource names the query keys a partner uses for keyword and sub_id_1..30.
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
	// BoardID is the panel section id for this record. Empty means the main section.
	// The engine does not read it.
	BoardID string `json:"board_id,omitempty"`
}

// OutboundPostback is a server-to-server template fired after an inbound conversion.
// ID and Name are empty on campaign templates saved before the shared catalog.
type OutboundPostback struct {
	ID       string   `json:"id,omitempty"`
	Name     string   `json:"name,omitempty"`
	URL      string   `json:"url"`
	Method   string   `json:"method"`
	Statuses []string `json:"statuses,omitempty"`
	// BoardID is the panel section id for this record. Empty means the main section.
	// The engine does not read it.
	BoardID string `json:"board_id,omitempty"`
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

// EffectiveSource returns the traffic source a campaign reads, with the
// campaign's own parameter names laid over the source.
//
// grp may be nil. src may be nil when the campaign has no source. A non-empty
// campaign parameter replaces the source name of that key. An empty campaign
// parameter removes the source name, so the field is read under its own name.
// A missing key keeps the source name. The result is nil when src is nil and
// the campaign names nothing. src is not modified. When the campaign adds no
// change, src itself is returned. There is no error.
func EffectiveSource(grp *Group, src *TrafficSource) *TrafficSource {
	var over map[string]string
	if grp != nil {
		over = grp.Parameters
	}
	if len(over) == 0 {
		return src
	}
	merged := map[string]string{}
	if src != nil {
		for k, v := range src.Parameters {
			if t := strings.TrimSpace(v); t != "" {
				merged[k] = t
			}
		}
	}
	changed := false
	for k, v := range over {
		t := strings.TrimSpace(v)
		if t == "" {
			if _, ok := merged[k]; ok {
				delete(merged, k)
				changed = true
			}
			continue
		}
		if merged[k] != t {
			merged[k] = t
			changed = true
		}
	}
	if !changed {
		return src
	}
	out := TrafficSource{}
	if src != nil {
		out = *src
	}
	out.Parameters = merged
	return &out
}

// ApplySource reads keyword and sub_id_1..30 from the query.
// A nil source uses the macro name itself as the query key.
// The keyword return is empty when the source does not map one or the query has none.
// The keyword and every sub id are cut to MaxValueBytes bytes at a rune boundary:
// they are visitor input and go into the click log and the Sub ID rules.
func ApplySource(q url.Values, src *TrafficSource) (keyword string, subs [MaxSubID]string) {
	if src != nil {
		if key := strings.TrimSpace(src.Parameters["keyword"]); key != "" {
			keyword = LimitBytes(q.Get(key), MaxValueBytes)
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
		subs[i-1] = LimitBytes(q.Get(qk), MaxValueBytes)
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
// sub_id_1 through sub_id_30. Without a mapping each name is the query key.
var ExtraSourceParams = []string{"site", "ad_campaign_id", "creative_id", "external_id"}

// LegacyKeywordParam is the one keyword name that was read before the aliases existed.
// It is the keyword whatever else the campaign binds to it, so existing links keep
// their keyword, key routing and separation.
const LegacyKeywordParam = "q"

// KeywordParams are the query parameter names a request carries its keyword under,
// in the order the engine tries them: the first non-empty value wins. They apply when
// the parameter the traffic source maps as keyword is absent or empty. keyword and
// utm_term are aliases added later and are skipped when another field of the campaign
// owns the name (see KeywordNameTaken); LegacyKeywordParam is never skipped. The engine
// and the admin access rules read this one array, so a name the engine accepts is also
// a name the admin treats as the keyword. It is an array so that no caller can change it.
var KeywordParams = [...]string{"keyword", LegacyKeywordParam, "utm_term"}

// KeywordNameTaken reports whether a query name belongs to another field of the
// campaign and so must not double as its keyword. It is the one rule of the engine
// and of the admin access checks.
//
// name is a candidate from KeywordParams. params are the parameters of the campaign's
// traffic source (nil when it has none); costParam and currencyParam are the campaign's
// price names. An alias (keyword, utm_term) is taken when the source maps any parameter
// other than keyword onto it (sub_id_N, site, ad_campaign_id, creative_id, external_id),
// or when it is the price or currency name; names are compared after trimming.
// LegacyKeywordParam is never taken: it was the keyword before the aliases, and links
// that bind it to a Sub ID as well keep reading it as the keyword. It does not allocate.
func KeywordNameTaken(name string, params map[string]string, costParam, currencyParam string) bool {
	if name == LegacyKeywordParam {
		return false
	}
	if strings.TrimSpace(costParam) == name || strings.TrimSpace(currencyParam) == name {
		return true
	}
	for field, mapped := range params {
		if field != "keyword" && strings.TrimSpace(mapped) == name {
			return true
		}
	}
	return false
}

// MaxValueBytes is the longest stored value of one visitor-supplied field, in
// bytes: a sub id, the keyword, an extra field and the value of a Sub ID rule.
const MaxValueBytes = 255

// Extras is the site, ad campaign, creative and external id of one click.
// Each field is trimmed and at most MaxValueBytes bytes. An empty field
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

// LimitBytes returns s cut to at most n bytes without splitting a UTF-8 rune:
// a cut that would fall inside a multi-byte character backs up to its start.
// n <= 0 or an empty s yields an empty string. A shorter s is unchanged.
// There is no error.
func LimitBytes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
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
		v := LimitBytes(strings.TrimSpace(q.Get(sourceParamKey(src, name))), MaxValueBytes)
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
// sub_id_30 are accepted. sub_id_31 and any other name are rejected.
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
