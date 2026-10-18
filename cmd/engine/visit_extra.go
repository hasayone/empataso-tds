package main

import (
	"net/http"
	"net/url"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/router"
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

// sourceReads reads what the traffic source of grp takes from the query.
//
// grp is the serving group, q the request query, v the visitor and base the
// keyword the request carried on its own. The visitor's Subs and Key are set:
// Key is the keyword the group's source maps, or base when the source gives
// none. It returns the sub ids and the extra parameters (site, ad campaign,
// creative, external id) under the names that group's source maps; without a
// source the plain names are read.
func (d *engineDeps) sourceReads(grp *config.Group, q url.Values, v *router.Visitor, base string) ([config.MaxSubID]string, config.Extras) {
	src, _ := d.sourceOf(grp)
	kw, subs := config.ApplySource(q, src)
	v.Subs, v.Key = subs, base
	if kw != "" {
		v.Key = kw
	}
	return subs, config.SourceExtras(q, src)
}
