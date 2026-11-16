package main

import (
	"net/http"
	"net/url"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/detect"
	"github.com/egerkuzma/emtds/internal/router"
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
