package main

import (
	"log/slog"
	"math/rand"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/fetch"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/ipindex"
	"github.com/egerkuzma/kuztds/internal/logbuf"
	"github.com/egerkuzma/kuztds/internal/render"
	"github.com/egerkuzma/kuztds/internal/router"
	"github.com/egerkuzma/kuztds/internal/seplist"
	"github.com/egerkuzma/kuztds/internal/server"
	"github.com/egerkuzma/kuztds/internal/store"
)

// engineDeps — hot-path dependencies assembled in main() and passed to the
// handler. Extracted from a closure so the pipeline can be exercised through
// httptest (see handler_test.go).
type engineDeps struct {
	log          *slog.Logger
	lists        *ipindex.Set
	sigs         *detect.Signatures
	seps         *seplist.Set
	geores       geo.Resolver
	counters     *store.Counters
	groups       *config.Groups
	offers       *config.Book[config.Offer]
	landings     *config.Book[config.Landing]
	sources      *config.Book[config.TrafficSource]
	networks     *config.Book[config.AffiliateNetwork]
	s2s          *config.Book[config.OutboundPostback]
	outbound     chan outboundJob
	sentMu       sync.Mutex
	sent         map[string]struct{}
	logs         *logbuf.Buffer
	chConn       *store.CH
	fetcher      *fetch.Client
	dataDir      string
	keysDir      string
	postbackKey  string
	keys         apiKeys
	trashMode    string
	trashURL     string
	curlCacheMin int
	botPath      string
	cids         *cidSigner
	pages        *pageCache
	landServe    *landingServer

	// noStepWave is true when ClickHouse is connected but events has no step
	// column. The /offer step then logs the first step's uniqueness, so a campaign
	// with a landing does not count one visitor twice.
	noStepWave bool
}

// ipListFiles collects the names of .dat lists referenced by stream ip_list
// filters (without the extension) so the engine loads them into ipindex.Set
// alongside the standard lists. Otherwise the per-stream IP filter would
// silently fail to fire.
func ipListFiles(groups *config.Groups) []string {
	if groups == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range groups.List() {
		for _, s := range g.Streams {
			f := strings.TrimSuffix(s.Rules.IPList.File, ".dat")
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// separationFiles collects the .dat names referenced by stream separation
// rules, so seplist.Set can hold them in memory. Same reason as ipListFiles: a
// file the engine never read matches nothing, and separation failing to fire is
// invisible — the stream just serves its default output.
func separationFiles(groups *config.Groups) []string {
	if groups == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range groups.List() {
		for _, s := range g.Streams {
			if !s.Separation.Enabled {
				continue
			}
			f := strings.TrimSuffix(s.Separation.File, ".dat")
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// isProxy reports whether the visitor looks like a proxy, VPN, or hosting network.
// d carries the IP lists and the signature holder; either field may be nil.
// ip is the visitor address. org is the geo organization name.
// It returns true when ip is in ip_proxy, otherwise when org matches a pattern.
// It does not report an error.
func isProxy(d *engineDeps, ip netip.Addr, org string) bool {
	if d.lists != nil {
		if _, hit := d.lists.Lookup("ip_proxy", ip); hit {
			return true
		}
	}
	patterns := detect.DefaultProxyOrg()
	if d.sigs != nil {
		patterns = d.sigs.ProxyOrg()
	}
	return detect.ProxyByOrg(org, patterns)
}

// hitPercent reports whether a p-percent chance fires: p <= 0 never, p >= 100
// always, and exactly p times in 100 in between.
//
// Written out because "p > rand.Intn(100)+1" reads right and is not: rand+1 is
// uniform over [1,100], so that form fires (p-1) times in 100 — prob 100 gives
// 99% and prob 1 never fires at all.
func hitPercent(p int) bool {
	if p <= 0 {
		return false
	}
	if p >= 100 {
		return true
	}
	return rand.Intn(100)+1 <= p
}

// root — hot-path handler. Full request pipeline:
// postback → api mode → blacklist → group/trash → antiflood → geo/detect →
// uniqueness → router → bots → separation/remote/chance → distribution →
// render → async log → key collection.
func (d *engineDeps) root(w http.ResponseWriter, r *http.Request) {
	// Postback pixel: ?pb=KEY&cid=<id;cid>&profit=X.
	if pb := r.URL.Query().Get("pb"); pb != "" {
		if d.postbackKey != "" && pb == d.postbackKey && d.chConn != nil {
			cid := r.URL.Query().Get("cid")
			profit, _ := strconv.ParseFloat(r.URL.Query().Get("profit"), 64)
			status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
			if cid != "" {
				gid, err := d.chConn.RecordPostback(r.Context(), cid, profit, status)
				if err != nil {
					d.log.Warn("postback failed", "cid", cid, "err", err)
				} else {
					d.queueOutbound(gid, cid, profit, status, r.URL.Query())
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// API-client mode: ?api=base64(JSON). Input is taken from the client request.
	var apiReq *apiRequest
	apiMode := false
	if a := r.URL.Query().Get("api"); a != "" {
		req, ok := parseAPIRequest(a, d.keys.verify)
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		apiReq, apiMode = req, true
	}

	var ip netip.Addr
	if apiMode {
		ip, _ = netip.ParseAddr(strings.TrimSpace(apiReq.IP))
	} else {
		ip = server.ClientIP(r.Context())
	}
	if !ip.IsValid() {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 1) Blacklist — instant rejection.
	if _, blocked := d.lists.Lookup("ip_blacklist", ip); blocked {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	rctx := r.Context()

	// Group: by ID/alias — from api.id, or from the FIRST segment of the request
	// path. Everything after it is ignored, so "/promo", "/promo/" and
	// "/promo/iphone-15-sale.html" all resolve to the group "promo".
	//
	// Matching the whole path instead would send every deep link to trash mode —
	// an empty 200 by default, indistinguishable from nothing happening. TDS
	// links are routinely dressed up as real pages, so the extra segments are
	// decoration and must not change routing.
	gid, rest, _ := strings.Cut(strings.Trim(r.URL.Path, "/"), "/")
	offerStep := rest == "offer" || strings.HasPrefix(rest, "offer/")
	if apiMode {
		gid = apiReq.ID
	}
	var grp *config.Group
	if d.groups != nil {
		grp, _ = d.groups.Get(gid)
	} else {
		grp = demoGroup
	}
	if grp == nil || !grp.Status {
		trashResult(d.trashMode, d.trashURL).Write(w)
		return
	}

	// 1.5) Antiflood (phase 4): no more than N requests from an IP per window.
	if d.counters != nil && grp.Firewall.Enabled {
		fw := grp.Firewall
		if ok, _ := d.counters.Firewall(rctx, grp.ID, ip, fw.Queries, secs(fw.Seconds)); !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	ua := r.Header.Get("User-Agent")
	ref := r.Header.Get("Referer")
	if apiMode {
		ua, ref = apiReq.UserAgent, apiReq.Referer
	}

	// 2) Device/OS/browser/brand and geo (phases 2, 7).
	info := detect.Parse(ua)
	applyModelHint(&info, r, apiMode)
	device := info.Device
	g := d.geores.Resolve(ip)
	proxy := isProxy(d, ip, g.Org)
	// Country has two sources: the geo database and the CDN's header. The
	// group says which one wins when both answer; the other is the fallback.
	//
	// The header is believed only when the request came through a trusted
	// proxy — exactly the rule X-Forwarded-For follows. From anyone else it is
	// text the visitor typed, and a visitor who can name their own country
	// picks their own stream. api.cf_country is the api client's statement
	// and is covered by the api key. Either way a country is two characters;
	// anything else is dropped rather than passed into the log, the response
	// header and — through [RANDLINE-([COUNTRY].dat)-1] — a file name.
	country := g.Country
	hdr := geo.Empty
	if apiMode {
		hdr = isoCode(apiReq.CFCountry)
	} else if server.ViaTrustedProxy(r.Context()) {
		hdr = isoCode(r.Header.Get("CF-IPCountry"))
	}
	if hdr != geo.Empty && (apiMode || country == geo.Empty || grp.Geo == "cf") {
		country = hdr
	}
	tzOffset := g.UTCOffset(time.Now())

	// 3.4) Click id. The offer step keeps the landing's id only when it is a
	// signed one this engine issued; that request is the second step of a click
	// (ownStep). A direct visit to /offer is a first step of its own: new id,
	// the first step's uniqueness and the full price.
	cid := d.cids.newCID()
	ownStep := false
	if offerStep {
		if c := r.URL.Query().Get("cid"); d.cids.reusedCID(c) {
			cid, ownStep = c, true
		}
	}

	// 3.5) Uniqueness: the api client sends its own; otherwise cookie/Redis.
	unique, loggedUnique := true, true
	if apiMode {
		unique = apiReq.Uniq == "yes"
		loggedUnique = unique
	} else {
		unique, loggedUnique = d.visitUnique(w, r, grp, ip, ownStep)
	}

	// Input (key/lang/domain/extra params): from the api request or from HTTP.
	key := config.LimitBytes(r.URL.Query().Get("q"), config.MaxValueBytes)
	lang := langOf(r)
	domain := domainOf(ref)
	pars := extraParams(r)
	if apiMode {
		key, lang, domain, pars = config.LimitBytes(apiReq.Key, config.MaxValueBytes), apiReq.Lang, apiReq.Domain, apiReq.Pars
	}

	// 4) Stream selection (phase 3). Operator — by the label from the wap list.
	operator := router.Empty
	if label, ok := d.lists.Lookup("wap", ip); ok && label != "" {
		operator = label
	}
	v := router.Visitor{
		Lang:       lang,
		Country:    country,
		City:       g.City,
		Region:     g.Region,
		UA:         ua,
		Referer:    refOrEmpty(ref),
		Domain:     domain,
		Key:        key,
		Device:     device,
		Operator:   operator,
		OS:         info.OS,
		OSVersion:  info.OSVersion,
		Browser:    info.Browser,
		BrowserVer: info.BrowserVer,
		Brand:      info.Brand,
		Model:      info.Model,
		Unique:     unique,
		Proxy:      proxy,
		IP:         ip,
		ASN:        g.ASN,
		Org:        g.Org,
		Timezone:   g.Timezone,
		TZOffset:   tzOffset,
		Query:      r.URL.Query(),
	}
	subs, extras := d.sourceReads(grp, r.URL.Query(), &v, key)
	// The installation profile decides the bot flag before a stream is chosen.
	// The selected stream still decides where a bot is sent.
	profile := config.Bots{}
	if d.sigs != nil && d.lists != nil {
		profile = d.botProfile()
	}
	v.Bot = detectBot(d.lists, d.sigs, d.dataDir, botInput{IP: ip, UA: ua, Ref: refOrEmpty(ref), Lang: v.Lang, Proxy: proxy, Device: device}, profile) != detect.BotNone
	// Output defaults — from the group; the stream overrides them.
	//
	// A stream (or a group's default) of type "group" hands the visitor to
	// another group: selection runs again over that group's streams with the
	// same visitor. Everything decided before this point — antiflood,
	// uniqueness, country source — belongs to the group the visitor entered
	// through; what follows, including the log line, belongs to the group
	// that finally served them.
	originID, originName := d.resolveOrigin(r, grp, apiMode, apiReq)

	var (
		streamName, redirect, outRaw, ctype string
		selStream                           *config.Stream
		via                                 string // the first link followed: "<group id>/<stream>"
	)
	for hop := 0; ; hop++ {
		deps := router.Deps{IP: d.lists}
		if d.counters != nil {
			deps.Limiter = limiter{c: d.counters, ctx: rctx, id: grp.ID}
		}
		streamName, redirect, outRaw, ctype, selStream = "-", grp.Redirect, grp.Out, grp.Header, nil
		if s, ok := d.selectStream(w, r, grp, v, deps, apiMode); ok {
			selStream = s
			streamName = s.Name
			if s.Out.Redirect != "" {
				redirect = s.Out.Redirect
				outRaw = s.Out.Out
			}
			if s.Out.Header != "" {
				ctype = s.Out.Header
			}
			// The limit counter is consumed inside the router (limiter.Allowed):
			// selecting the stream and taking its limit are one event, so there is
			// nothing to record here.
		}
		if redirect != "group" {
			break
		}
		var next *config.Group
		if d.groups != nil {
			next, _ = d.groups.Get(strings.TrimSpace(outRaw))
		}
		// A link to nowhere, to a disabled group, or a chain that does not end
		// is a configuration error; the visitor gets what an unknown group gets.
		if next == nil || !next.Status || next == grp || hop >= config.MaxGroupHops {
			d.log.Warn("engine: group link not followed", "group", grp.ID, "stream", streamName, "target", outRaw, "hop", hop)
			trashResult(d.trashMode, d.trashURL).Write(w)
			return
		}
		if via == "" {
			via = grp.ID + "/" + streamName
		}
		grp = next
		subs, extras = d.sourceReads(grp, r.URL.Query(), &v, key) // the new group's own source
	}

	// 4.5) Bot detection by the selected stream's toggles (after selection).
	botsCfg := config.Bots{CheckUA: true} // no stream — signatures only (for the log)
	if selStream != nil {
		botsCfg = selStream.Bots
	}
	bot := detectBot(d.lists, d.sigs, d.dataDir, botInput{IP: ip, UA: ua, Ref: refOrEmpty(ref), Lang: v.Lang, Proxy: proxy, Device: device}, botsCfg)
	// Separate serving for bots (bot_redirect). "skip"/"" → normal stream (the bot is still logged).
	botServed := false
	if bot != detect.BotNone && selStream != nil && botsCfg.Redirect != "" && botsCfg.Redirect != "skip" {
		redirect = botsCfg.Redirect
		outRaw = botsCfg.Out
		if botsCfg.Header != "" {
			ctype = botsCfg.Header
		}
		botServed = true
	}

	// separation: substitute the output by keyword (unless this is bot serving).
	if !botServed && selStream != nil && selStream.Separation.Enabled &&
		v.Key != "" && selStream.Separation.File != "" {
		if o := d.seps.Lookup(selStream.Separation.File, v.Key); o != "" {
			outRaw = o
		}
	}

	// remote: load [REMOTE] from an external URL (cache, regex, fallback).
	// The value itself is spliced in after render.Expand, below: it is the
	// partner's response, not our template, and Expand's "substituted values
	// are never rescanned" rule only covers what Expand itself substitutes.
	remoteVal, wantRemote := "", false
	if !botServed && selStream != nil && selStream.Remote.Enabled && strings.Contains(outRaw, "[REMOTE]") {
		remoteVal, wantRemote = remoteValue(d.fetcher, rctx, selStream.Remote, ip.String(), country, g.City, v.Lang, v.Key), true
		// "|||" is the distribution separator, applied to the template by
		// pickOut. A remote value carrying it would otherwise let the partner
		// choose which variant we serve; treat it as a bad response instead.
		if strings.Contains(remoteVal, "|||") {
			remoteVal = selStream.Remote.Reserved
		}
	}

	// chance: for javascript/js_selection — show with probability chance %.
	// Here chance 0 means "not configured", so only a real 1..99 gates the
	// output; the draw itself goes through hitPercent, which carries the
	// off-by-one this comparison would otherwise have to get right by hand.
	if !botServed && selStream != nil && (redirect == "javascript" || redirect == "js_selection") {
		if ch := selStream.Out.Chance; ch > 0 && ch < 100 && !hitPercent(ch) {
			redirect = "stop"
			outRaw = ""
		}
	}

	// distribution of out variants (|||): random/rotator/evenly.
	dist := ""
	if selStream != nil && !botServed && (len(selStream.Out.Offers) > 0 || len(selStream.Out.Landings) > 0) {
		redirect, outRaw = router.ResolveOut(selStream, d.catalogs(), offerStep, redirect, outRaw, nil)
	}
	offerID, landingID, networkID := clickIDs(selStream, d.catalogs(), outRaw, offerStep)
	if networkID != "" {
		outRaw = appendOfferQuery(outRaw, d.offerQuery(networkID))
	}
	var shares []int
	if selStream != nil {
		dist = selStream.Out.Distribution
		if len(selStream.Out.Offers) == 0 && len(selStream.Out.Landings) == 0 {
			shares = selStream.Out.OutShares
		}
	}
	outRaw = pickOut(w, r, d.counters, rctx, grp.ID, streamName, outRaw, dist, shares)
	// @page: is inlined after clickIDs has compared the raw reference and
	// before macro expansion. A missing file stays an empty body of this stream.
	outRaw = d.applyPage(outRaw)

	// 5) Render the output: macros, api_mac, CURL fetch, or a normal redirect.
	q := v.Query
	originTok := ""
	if originID != "" && d.cids != nil {
		originTok = d.cids.signOrigin(originID)
	}
	md := render.MacroDeps{
		Key: v.Key, Path: r.Host, IP: ip.String(), Country: country, City: g.City,
		Region: g.Region, Lang: v.Lang, Device: device, Operator: operator,
		Domain: v.Domain, UserAgent: ua, CID: cid, Pars: pars, SubIDs: subs,
		ASN: asnText(g.ASN), Org: dashEmpty(g.Org), Timezone: g.Timezone, TZOffset: tzOffset,
		DataDir: d.dataDir, Offer: offerStepURL(gid, cid, r.URL.RawQuery), Origin: originTok,
		Query: func(n string) (string, bool) {
			v, ok := q[n]
			if !ok || len(v) == 0 {
				return "", false
			}
			return v[0], true
		},
	}
	out := render.Expand(outRaw, md)
	if wantRemote {
		out = strings.ReplaceAll(out, "[REMOTE]", remoteVal)
	}

	// api_mac: mac code in the api response with probability Prob %.
	mac := ""
	if redirect == "api" && selStream != nil && selStream.APIMac.Enabled &&
		selStream.APIMac.Code != "" && hitPercent(selStream.APIMac.Prob) {
		mac = render.Expand(selStream.APIMac.Code, md)
	}

	// logRedirect is what the event carries. It tracks redirect except when a
	// curl fetch fails: the visitor is then served something other than the
	// partner's page, and the report must not call that an ordinary serve.
	logRedirect := redirect

	var res render.Result
	if redirect == "curl" {
		rules := ""
		if selStream != nil {
			if rules = selStream.Curl; botServed {
				rules = selStream.BotCurl
			}
		}
		ct := ctype
		if ct == "" {
			ct = "text/html; charset=utf-8"
		}
		body, ok := curlBody(d.fetcher, rctx, out, render.Expand(rules, md), d.curlCacheMin)
		if ok {
			res = render.Result{Status: http.StatusOK, ContentType: ct, Body: []byte(body)}
		} else {
			// No stream-level fallback exists for curl (unlike Remote.Reserved),
			// so reuse the configured trash behaviour — the same answer the
			// engine already gives when it cannot serve a group. Anything is
			// better than a blank 200, which burns the click and hides it.
			logRedirect = "curl_error"
			res = trashResult(d.trashMode, d.trashURL)
			d.log.Error("engine: curl fetch failed", "group", grp.ID, "stream", streamName, "url", out)
		}
	} else if redirect != "landing" {
		res = render.Do(redirect, out, render.Options{ContentType: ctype, Path: r.Host,
			Country: country, City: g.City, Region: g.Region, Device: device,
			Operator: operator, Bot: bot, Uniq: b2yn(unique), Lang: v.Lang, Mac: mac})
	}

	// 6) Event log — asynchronous, the response does not wait for the write (phase 4).
	costCents, costCur, costOK := config.IncomingCost(r.URL.Query(), grp.CostParam, grp.CurrencyParam)
	// The second step carries the landing's query, so its price is already on the
	// landing row: the step logs no cents, and keeps the currency as the mark
	// that a price came with the click. A click without a valid price has neither.
	switch {
	case !costOK:
		costCents, costCur = 0, ""
	case ownStep:
		costCents = 0
	}
	if d.logs != nil {
		d.logs.Push(logbuf.Event{
			Ts: time.Now(), GroupID: grp.ID, GroupName: grp.Name,
			OriginID: originID, OriginName: originName,
			Stream: streamName, Out: out, Redirect: logRedirect, Device: device,
			Operator: operator, Country: country, City: g.City, Region: g.Region,
			Lang: v.Lang, Uniq: b2u8(loggedUnique), Bot: bot, IP: ip.String(),
			Referer: v.Referer, UserAgent: ua, Domain: v.Domain, Keyword: v.Key,
			OS: info.OS, OSVersion: info.OSVersion, Browser: info.Browser,
			BrowserV: info.BrowserVer, Brand: info.Brand, CID: cid,
			ASN: g.ASN, Org: dashEmpty(g.Org), Timezone: dashEmpty(g.Timezone), Via: via,
			OfferID: offerID, LandingID: landingID, SourceID: grp.SourceID, NetworkID: networkID,
			Subs: storedSubs(subs), CostCents: costCents, CostCur: costCur,
			Site: extras.Site, AdCampaignID: extras.AdCampaignID, CreativeID: extras.CreativeID,
			ExternalID: extras.ExternalID, XRW: requestXRW(r, apiMode),
			DeviceModel: info.Model, Step: b2u8(ownStep),
		})
	}

	// Keyword collection.
	if bot == detect.BotNone {
		if grp.SaveKeys && v.Key != "" {
			appendKey(d.keysDir, grp.ID, v.Key, false)
		}
		if grp.SaveKeysSE {
			if kse := parseSEKeyword(ref); kse != "" {
				appendKey(d.keysDir, grp.ID, kse, true)
			}
		}
	}

	// Diagnostic headers (before writing the response) + render.
	h := w.Header()
	h.Set("X-Kuztds-Bot", emptyDash(bot))
	h.Set("X-Kuztds-Device", device)
	h.Set("X-Kuztds-Country", country)
	h.Set("X-Kuztds-Stream", streamName)
	h.Set("X-Kuztds-Uniq", b2yn(unique))
	if redirect == "landing" {
		d.serveLocalLanding(w, r, strings.TrimSpace(outRaw), md)
		return
	}
	res.Write(w)
}

// clickIDs names the offer or landing whose template equals outRaw.
// The network id is the one stored on that offer. No match leaves the ids empty.
func clickIDs(stream *config.Stream, cat router.Catalogs, outRaw string, offerStep bool) (offerID, landingID, networkID string) {
	if stream == nil || outRaw == "" {
		return "", "", ""
	}
	if !offerStep {
		for _, ref := range stream.Out.Landings {
			item, ok := cat.Landings[ref.ID]
			if !ok || !item.Status {
				continue
			}
			if ref.ID == outRaw || (!item.Local && item.URL == outRaw) {
				return "", ref.ID, ""
			}
		}
	}
	for _, ref := range stream.Out.Offers {
		o, ok := cat.Offers[ref.ID]
		if ok && o.Status && !o.Local && o.URL == outRaw {
			return ref.ID, "", o.NetworkID
		}
	}
	return "", "", ""
}

// appendOfferQuery adds the network query to the URL the visitor is sent to.
// The offer was already named from the URL before this append, so the extra
// query does not change which offer or network the click belongs to.
// An address that already has a query keeps it; the new part is joined with &.
func appendOfferQuery(raw, query string) string {
	query = strings.TrimSpace(strings.TrimPrefix(query, "?"))
	if query == "" || raw == "" || !strings.Contains(raw, "://") {
		return raw
	}
	if strings.Contains(raw, "?") {
		return raw + "&" + strings.TrimPrefix(query, "&")
	}
	return raw + "?" + query
}

// offerQuery is the partner query stored on the network. A disabled network
// adds nothing. The engine never requests the network postback URL.
func (d *engineDeps) offerQuery(networkID string) string {
	if d == nil || d.networks == nil || networkID == "" {
		return ""
	}
	n, ok := d.networks.Get(networkID)
	if !ok || !n.Status {
		return ""
	}
	return n.OfferQuery
}

// storedSubs keeps the first twelve sub ids for the click log.
func storedSubs(subs [config.MaxSubID]string) (out [12]string) {
	for i := 0; i < len(out) && i < len(subs); i++ {
		out[i] = subs[i]
	}
	return out
}

// asnText renders an AS number for macros: "" when unknown, so a template
// like "as=[ASN]" degrades to an empty value instead of "as=0".
func asnText(n uint32) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(n), 10)
}

// dashEmpty turns the "-" placeholder into "": free-text values go into URLs
// and columns where a literal dash would read as data.
func dashEmpty(s string) string {
	if s == geo.Empty {
		return ""
	}
	return s
}

// resolveOrigin picks the first-entry campaign for this visit.
//
// r carries ?origin=. grp is the group the visitor entered. apiMode marks an
// api request; apiReq holds its campaign id. A verified origin token naming an
// active group wins; otherwise the entry group is used. In api mode the
// campaign id from the request is the origin. It returns the id and display name.
func (d *engineDeps) resolveOrigin(r *http.Request, grp *config.Group, apiMode bool, apiReq *apiRequest) (id, name string) {
	if apiMode && apiReq != nil {
		id = strings.TrimSpace(apiReq.ID)
		if id != "" && d.groups != nil {
			if g, ok := d.groups.Get(id); ok {
				name = g.Name
			}
		}
		return id, name
	}
	if tok := strings.TrimSpace(r.URL.Query().Get("origin")); tok != "" && d.cids != nil {
		if gid, ok := d.cids.verifyOrigin(tok); ok && d.groups != nil {
			if g, ok := d.groups.Get(gid); ok && g.Status {
				return g.ID, g.Name
			}
		}
	}
	if grp != nil {
		return grp.ID, grp.Name
	}
	return "", ""
}
