package main

import (
	"net/http"
	"net/url"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/router"
)

// applyModelHint lets the Sec-CH-UA-Model header override the model read from
// the User-Agent.
//
// info is the parsed User-Agent and is changed in place; r is the request;
// apiMode marks an api request, whose headers belong to the calling server, not
// the visitor, so there the User-Agent alone decides. The header counts only
// when the browser sent it on its own: the engine never asks for it.
func applyModelHint(info *detect.Info, r *http.Request, apiMode bool) {
	if apiMode {
		return
	}
	if m := detect.HintModel(r.Header.Get("Sec-CH-UA-Model")); m != "" {
		info.Model = m
	}
}

// baseKeyword picks the keyword a request carries under the plain names.
//
// q is the parsed request query, src the group's traffic source (nil when it has
// none) and costParam and currencyParam the group's price parameter names. The
// first non-empty value of config.KeywordParams (keyword, q, utm_term) wins, in
// that order; an empty value counts as absent, and so does an alias (keyword,
// utm_term) that config.KeywordNameTaken reports, which keeps a value the source
// routes to another field out of the keyword. q is read as it always was, even when
// the source binds it to a Sub ID too: it was the keyword before the aliases, and
// links that depend on it keep their key routing. Only the first value of a repeated
// parameter is read.
// The result is cut to config.MaxValueBytes bytes at a rune boundary and is empty
// when no name qualifies. It does not allocate.
func baseKeyword(q url.Values, src *config.TrafficSource, costParam, currencyParam string) string {
	var params map[string]string
	if src != nil {
		params = src.Parameters
	}
	for _, name := range config.KeywordParams {
		if config.KeywordNameTaken(name, params, costParam, currencyParam) {
			continue
		}
		if v := q.Get(name); v != "" {
			return config.LimitBytes(v, config.MaxValueBytes)
		}
	}
	return ""
}

// sourceReads reads what the traffic source of grp takes from the query.
//
// grp is the serving group, q the request query, v the visitor, apiMode marks an
// api request and apiKey is its keyword (already cut to the limit). The visitor's
// Subs and Key are set. Key is the keyword the group's source maps when that
// parameter is present and non-empty; otherwise apiKey in api mode, or the base
// keyword of the query (see baseKeyword) for this group's source and price names.
// It returns the sub ids and the extra parameters (site, ad campaign, creative,
// external id) under the names that group's source maps, with the campaign's own
// parameter names laid over them; without either, the plain names are read.
func (d *engineDeps) sourceReads(grp *config.Group, q url.Values, v *router.Visitor, apiMode bool, apiKey string) ([config.MaxSubID]string, config.Extras) {
	src, _ := d.sourceOf(grp)
	src = config.EffectiveSource(grp, src)
	kw, subs := config.ApplySource(q, src)
	v.Subs = subs
	switch {
	case kw != "":
		v.Key = kw
	case apiMode:
		v.Key = apiKey
	default:
		v.Key = baseKeyword(q, src, grp.CostParam, grp.CurrencyParam)
	}
	return subs, config.SourceExtras(q, src)
}

// collectedClick is the sub ids and extra fields already gathered for one click.
// The keyword is stored on the visitor, not here. An empty field is not collected.
type collectedClick struct {
	subs   [config.MaxSubID]string
	extras config.Extras
}

// fillCollected reads grp's traffic source from q and fills only the keyword,
// sub ids and extras that are still empty.
//
// grp is the campaign whose parameter names are applied to q. q is the request
// query. v is the visitor and is updated: Key and Subs become the merged
// values. prev holds the sub ids and extras already collected on this chain.
// apiMode and apiKey are the same arguments as for sourceReads. A value already
// collected stays, even when grp's mapping reads a different value or reads
// nothing. A field that is still empty takes grp's value when that value is
// non-empty. It returns the merged sub ids and extras. It does not fail.
func (d *engineDeps) fillCollected(grp *config.Group, q url.Values, v *router.Visitor, prev collectedClick, apiMode bool, apiKey string) collectedClick {
	prevKey := v.Key
	nextSubs, nextExtras := d.sourceReads(grp, q, v, apiMode, apiKey)
	if prevKey != "" {
		v.Key = prevKey
	}
	var subs [config.MaxSubID]string
	for i := range subs {
		if prev.subs[i] != "" {
			subs[i] = prev.subs[i]
			continue
		}
		subs[i] = nextSubs[i]
	}
	v.Subs = subs
	return collectedClick{
		subs: subs,
		extras: config.Extras{
			Site:         keepCollected(prev.extras.Site, nextExtras.Site),
			AdCampaignID: keepCollected(prev.extras.AdCampaignID, nextExtras.AdCampaignID),
			CreativeID:   keepCollected(prev.extras.CreativeID, nextExtras.CreativeID),
			ExternalID:   keepCollected(prev.extras.ExternalID, nextExtras.ExternalID),
		},
	}
}

// keepCollected returns prev when it is already set and next otherwise.
// prev and next are one click field. There is no error.
func keepCollected(prev, next string) string {
	if prev != "" {
		return prev
	}
	return next
}
