package admin

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/egerkuzma/kuztds/internal/config"
)

// sourceParamField returns the click field a traffic source parameter key feeds:
// "keyword", an extra click field (config.ExtraSourceParams) or "sub_id_N" for the key
// of the same name. ok is false for any other key.
func sourceParamField(key string) (field string, ok bool) {
	key = strings.TrimSpace(key)
	if key == "keyword" || strings.HasPrefix(key, "sub_id_") || slices.Contains(config.ExtraSourceParams, key) {
		return key, true
	}
	return "", false
}

// hiddenSourceKey reports whether the source parameter key feeds a field v must not
// rebind: a field hidden for v, or a Sub ID the journal does not store (macroQueryRead)
// while v has any query-read field hidden, because {sub_id_13}..{sub_id_15} carry
// whatever query parameter the source maps there.
func hiddenSourceKey(v viewer, key string) bool {
	field, ok := sourceParamField(key)
	if !ok {
		return false
	}
	if fieldHidden(v, field) {
		return true
	}
	_, unstored := macroQueryRead["{"+field+"}"]
	return unstored && queryReadHidden(v)
}

// sourceFields lists every click field a traffic source parameter can feed:
// the keyword, sub_id_1..sub_id_15 and the extra click fields.
func sourceFields() []string {
	out := []string{"keyword"}
	for n := 1; n <= config.MaxSubID; n++ {
		out = append(out, config.SubIDRuleKey(n))
	}
	return append(out, config.ExtraSourceParams...)
}

// fieldReadNames returns the query parameter names the engine reads for the source
// field when the source maps it as params says: the mapped name, or the plain field
// name when unmapped. The keyword has no plain name but is also taken from "q", the
// parameter of a request that carries a keyword on its own.
func fieldReadNames(params map[string]string, field string) []string {
	var out []string
	if name := strings.TrimSpace(params[field]); name != "" {
		out = append(out, name)
	} else if field != "keyword" {
		out = append(out, field)
	}
	if field == "keyword" {
		out = append(out, "q")
	}
	return out
}

// hiddenQueryNames returns the query parameter names that carry a field hidden for v,
// as the traffic source parameters params name them (see fieldReadNames).
// The price is not listed: its names are set on the campaign, not on the source.
func hiddenQueryNames(v viewer, params map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, f := range queryReadFields() {
		if f == "cost" || !fieldHidden(v, f) {
			continue
		}
		for _, name := range fieldReadNames(params, f) {
			out[name] = true
		}
	}
	return out
}

// priceNames returns the query parameter names of the click price set on groups:
// the non-empty CostParam and CurrencyParam of each campaign, trimmed.
func priceNames(groups ...config.Group) map[string]bool {
	out := map[string]bool{}
	for _, g := range groups {
		for _, name := range []string{g.CostParam, g.CurrencyParam} {
			if name = strings.TrimSpace(name); name != "" {
				out[name] = true
			}
		}
	}
	return out
}

// hiddenReads returns the source fields visible to v that read, under the mapping
// params, a query parameter that carries a hidden value: the parameter of a keyword,
// Sub ID or extra click field hidden for v (mapped, or by its plain name), or a name in
// prices, the price names of campaigns. The unmapped plain names count. Such a field
// would copy the hidden value into a visible journal column or a {sub_id_N} macro.
func hiddenReads(v viewer, params map[string]string, prices map[string]bool) map[string]bool {
	carried := hiddenQueryNames(v, params)
	for name := range prices {
		carried[name] = true
	}
	out := map[string]bool{}
	for _, f := range sourceFields() {
		if fieldHidden(v, f) {
			continue
		}
		for _, name := range fieldReadNames(params, f) {
			if carried[name] {
				out[f] = true
			}
		}
	}
	return out
}

// newHiddenRead returns the first source field, in sourceFields order, that reads a
// parameter carrying a hidden value under params but did not under before (see
// hiddenReads). It is the one rule behind a source edit and a campaign changing its
// source, so a read stored as it is stays allowed. It returns "" when there is none.
func newHiddenRead(v viewer, params, before map[string]string, prices map[string]bool) string {
	was := hiddenReads(v, before, prices)
	now := hiddenReads(v, params, prices)
	for _, f := range sourceFields() {
		if now[f] && !was[f] {
			return f
		}
	}
	return ""
}

// sourceCheck is the userCheck of traffic sources. A viewer with a keyword, a Sub ID,
// an extra click field or the price hidden may not set, change or clear the parameter
// name of a field hidden from them, nor of {sub_id_13}..{sub_id_15}: the name decides
// which click parameter fills the field, so changing it would let a curly macro already
// present in a link read the hidden value. They may not map any other parameter onto
// the query name of a hidden field either, which would copy the hidden value into a
// field they can see. With the price hidden they may not bind a visible field to the
// query name of the price (cost_param or currency_param) of any campaign, nor clear a
// binding so that the field falls back to such a name. A binding stored as it is stays.
// ctx is the request context; the campaign list is read through it, and only when the
// parameters changed. old is nil for a new record. A campaign list that cannot be read
// answers "read error".
// It returns the client message, or "" when allowed.
func (s *Server) sourceCheck(ctx context.Context, v viewer, item config.TrafficSource, old *config.TrafficSource) string {
	if !queryReadHidden(v) {
		return ""
	}
	var before map[string]string
	if old != nil {
		before = old.Parameters
	}
	keys := make([]string, 0, len(item.Parameters)+len(before))
	for k := range item.Parameters {
		keys = append(keys, k)
	}
	for k := range before {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changed := false
	for _, k := range keys {
		if sameText(item.Parameters[k], before[k]) {
			continue
		}
		changed = true
		if hiddenSourceKey(v, k) {
			return "source parameter " + strings.TrimSpace(k) + " belongs to a hidden field"
		}
	}
	if !changed {
		return ""
	}
	var prices map[string]bool
	if fieldHidden(v, "cost") && s.cfg.Groups != nil {
		groups, err := s.cfg.Groups.List(ctx)
		if err != nil {
			return "read error"
		}
		prices = priceNames(groups...)
	}
	if f := newHiddenRead(v, item.Parameters, before, prices); f != "" {
		return "source maps " + f + ", a hidden field"
	}
	return ""
}

// validateGroupParams refuses a campaign change that rebinds a hidden value to another
// click parameter. For a viewer with the price hidden, CostParam and CurrencyParam must
// stay as stored (empty on a new campaign). For a campaign whose source changed, the
// new source must not name a parameter of a keyword or Sub ID hidden from the viewer,
// and must not make a visible field read the query name of a hidden field or, with the
// price hidden, of the price of this campaign, mapped or by its plain name, unless the
// previous source already did (newHiddenRead). A campaign saved on a source without a
// change of source is not checked.
// stored is the file before the save, merged the candidate list, sources the stored
// traffic sources. Campaigns that did not change are skipped.
// It returns the client message, or "" when allowed.
func validateGroupParams(v viewer, stored, merged []config.Group, sources []config.TrafficSource) string {
	if !queryReadHidden(v) {
		return ""
	}
	storedByID := make(map[string]config.Group, len(stored))
	for _, g := range stored {
		storedByID[g.ID] = g
	}
	sourceByID := make(map[string]config.TrafficSource, len(sources))
	for _, s := range sources {
		sourceByID[s.ID] = s
	}
	for _, g := range merged {
		old, had := storedByID[g.ID]
		if had && groupsEqual(old, g) {
			continue
		}
		if fieldHidden(v, "cost") && (!sameText(g.CostParam, old.CostParam) || !sameText(g.CurrencyParam, old.CurrencyParam)) {
			return "campaign price parameter belongs to a hidden field"
		}
		if g.SourceID == old.SourceID {
			continue
		}
		for key, val := range sourceByID[g.SourceID].Parameters {
			if strings.TrimSpace(val) != "" && hiddenSourceKey(v, key) {
				return "source maps " + strings.TrimSpace(key) + ", a hidden field"
			}
		}
		var prices map[string]bool
		if fieldHidden(v, "cost") {
			prices = priceNames(g)
		}
		next, prev := sourceByID[g.SourceID].Parameters, sourceByID[old.SourceID].Parameters
		if f := newHiddenRead(v, next, prev, prices); f != "" {
			return "source maps " + f + ", a hidden field"
		}
	}
	return ""
}

// hiddenSourceList returns the stored traffic sources for a viewer who has a keyword, a
// Sub ID or the price hidden, and nil for everyone else. A missing store gives no
// sources. It returns the read error of the store.
func (s *Server) hiddenSourceList(ctx context.Context, v viewer) ([]config.TrafficSource, error) {
	if !queryReadHidden(v) || s.cfg.Sources == nil {
		return nil, nil
	}
	return s.cfg.Sources.List(ctx)
}

// errNetworkQuery is the client message for an offer on a network whose offer_query
// holds a macro the viewer may not write, when the viewer may not read that network.
const errNetworkQuery = "network offer_query holds a macro of a hidden field"

// offerCheck is the userCheck of offers. It refuses an offer text that breaks the macro
// rules and an offer that sends clicks through a network whose offer_query holds a macro
// the viewer may not write: the engine appends that query to the offer address before
// expanding macros, so a new offer, a changed network or a changed address would carry
// the hidden value to the address of the viewer's choice. A change that touches neither
// the network nor the address, such as a rename, is allowed. old is nil for a new offer.
// The answer names the macro only when the viewer has the networks section and may
// read that network; otherwise it is the fixed text errNetworkQuery, so a closed
// record is not disclosed. ctx is the request context for reading the networks.
// It returns the client message, or "" when allowed.
func (s *Server) offerCheck(ctx context.Context, v viewer, item config.Offer, old *config.Offer) string {
	if msg := macroCheck(v, item, old); msg != "" {
		return msg
	}
	g := newMacroGuard(v)
	if g == nil || item.NetworkID == "" || s.cfg.Networks == nil {
		return ""
	}
	if old != nil && old.NetworkID == item.NetworkID && old.URL == item.URL {
		return ""
	}
	list, err := s.cfg.Networks.List(ctx)
	if err != nil {
		return "network read error"
	}
	for _, n := range list {
		if n.ID != item.NetworkID {
			continue
		}
		if msg := g.find(n.OfferQuery); msg != "" {
			if !v.hasSection(secNetworks) || !v.canSee(resNetworks, ref{id: n.ID, label: n.Group, owner: n.CreatedBy}) {
				return errNetworkQuery
			}
			return "network offer_query: " + msg
		}
	}
	return ""
}

// frozenStreamOut refuses a change of the output of a stream whose Curl, BotCurl or
// Remote.URL holds a macro the viewer may not write. Those texts send the hidden value to
// the partner or into the page, so the address they act on must stay as the
// administrator set it: Out, and the bot output Bots.Redirect, Bots.Out, Bots.Header.
// old is the stored campaign and cand the candidate. Streams are paired by name when it
// is unique on both sides, otherwise by position; a stream with no pair is not checked.
// It returns the client message, or "" when allowed.
func frozenStreamOut(g *macroGuard, old, cand config.Group) string {
	for i, st := range old.Streams {
		if g.find(st.Curl) == "" && g.find(st.BotCurl) == "" && g.find(st.Remote.URL) == "" {
			continue
		}
		j := pairStream(old.Streams, cand.Streams, i)
		if j < 0 {
			continue
		}
		if outputBytes(st) != outputBytes(cand.Streams[j]) {
			return "stream " + st.Name + " sends a hidden field: its output cannot change"
		}
	}
	return ""
}

// outputBytes returns the JSON of the output settings of st that frozenStreamOut compares.
func outputBytes(st config.Stream) string {
	b, _ := json.Marshal(struct {
		Out                      config.Output
		Redirect, BotOut, Header string
	}{st.Out, st.Bots.Redirect, st.Bots.Out, st.Bots.Header})
	return string(b)
}

// pairStream returns the index in cand of the stream that matches old[i], or -1.
// A name that is non-empty and unique in both lists pairs by name; otherwise the
// stream at the same position pairs.
func pairStream(old, cand []config.Stream, i int) int {
	name := old[i].Name
	if name != "" && countStreams(old, name) == 1 && countStreams(cand, name) == 1 {
		for j, st := range cand {
			if st.Name == name {
				return j
			}
		}
	}
	if i < len(cand) {
		return i
	}
	return -1
}

// countStreams counts the streams called name.
func countStreams(list []config.Stream, name string) int {
	n := 0
	for _, st := range list {
		if st.Name == name {
			n++
		}
	}
	return n
}

// sameText reports whether a and b are equal after trimming spaces.
func sameText(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }
