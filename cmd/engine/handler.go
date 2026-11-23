package main

import (
	"context"
	"log/slog"
	"math/rand"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/fetch"
	"github.com/hasayone/empataso-tds/internal/geo"
	"github.com/hasayone/empataso-tds/internal/ipindex"
	"github.com/hasayone/empataso-tds/internal/logbuf"
	"github.com/hasayone/empataso-tds/internal/render"
	"github.com/hasayone/empataso-tds/internal/router"
	"github.com/hasayone/empataso-tds/internal/seplist"
	"github.com/hasayone/empataso-tds/internal/server"
	"github.com/hasayone/empataso-tds/internal/store"
)

// engineDeps — hot-path dependencies assembled in main() and passed to the
// handler. Extracted from a closure so the pipeline can be exercised through
// httptest (see handler_test.go).
type engineDeps struct {
	log      *slog.Logger
	lists    *ipindex.Set
	sigs     *detect.Signatures
	seps     *seplist.Set
	geores   geo.Resolver
	counters *store.Counters
	groups   *config.Groups
	domains  *config.Book[config.Domain]
	offers   *config.Book[config.Offer]
	landings *config.Book[config.Landing]
	sources  *config.Book[config.TrafficSource]
	networks *config.Book[config.AffiliateNetwork]
	s2s      *config.Book[config.OutboundPostback]
	outbound chan outboundJob
	logs     *logbuf.Buffer
	// chConn is the ClickHouse connection; it is empty until the background
	// connect succeeds, so every reader goes through clickhouse().
	chConn       atomic.Pointer[store.CH]
	fetcher      *fetch.Client
	dataDir      string
	keyFiles     *keyWriter
	postbackKey  string
	keys         apiKeys
	trashMode    string
	trashURL     string
	curlCacheMin int
	botPath      string
	cids         *cidSigner
	pages        *pageCache
	landServe    *landingServer
	pbAuth       postbackAuth

	// stats counts postback outcomes and outbound jobs for the metrics file.
	stats engineStats

	// traffic writes the click paths of the campaign traffic log. It is nil
	// without Redis or with EMTDS_TRAFFIC_LOG_MAX=0, and nothing is recorded then.
	traffic *trafficLogger

	// pbq, plog and outSpool are the disk queues of postbacks, postback journal
	// lines and outbound jobs. They are nil without a spool, and a postback is
	// then answered from the store alone.
	pbq      *postbackQueue
	plog     *postbackLog
	outSpool *outboundSpool
	// outLocks is the memory fallback of the Redis lock of outbound jobs.
	outLocks outboundLocks
	// bg counts the background goroutines the stop waits for: the postback
	// workers, the outbound sender and the feeder. Start them with goBG.
	bg sync.WaitGroup
	// convOverride replaces the ClickHouse client for postbacks; tests set it.
	convOverride conversionStore

	// totalSeeds holds the retry times of the total conversion counters being seeded, and
	// totalCount replaces the store count of leads and sales of an offer; tests set it.
	totalSeeds totalSeeds
	totalCount func(ctx context.Context, offerID string) (int64, error)

	// noStepWave is true while events may lack the step column: before
	// ClickHouse connects, and after it connects without that column. The
	// /offer step then logs the first step's uniqueness, so a campaign with a
	// landing does not count one visitor twice.
	noStepWave atomic.Bool
}

// clickhouse returns the ClickHouse connection, or nil while there is none. A
// nil d has none.
func (d *engineDeps) clickhouse() *store.CH {
	if d == nil {
		return nil
	}
	return d.chConn.Load()
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
// ip is the visitor address. org and orgAlt are the organization names from the
// two geo databases; orgAlt may be "" or "-".
// It returns detect.IsProxy for the lists, the organization patterns of the
// signature holder (the built-in ones when it is nil) and the two names.
// It does not report an error.
func isProxy(d *engineDeps, ip netip.Addr, org, orgAlt string) bool {
	return detect.IsProxy(d.lists, ip, d.sigs.ProxyOrg(), org, orgAlt)
}

// hitPercent reports whether a p-percent chance fires: p <= 0 never, p >= 100
// always, and exactly p times in 100 in between. It does not report an error.
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
	began := time.Now()
	// The query is parsed once; every read below shares it.
	query := r.URL.Query()

	// Keitaro postback path: /{key}/postback — two segments, second exactly
	// "postback". Only checked when a secret is configured. A wrong key is not
	// a 403: it falls through to normal campaign routing so a campaign alias
	// that happens to look like this pattern is not broken.
	if d.pbAuth.secret != "" {
		if seg, rest, found := strings.Cut(strings.Trim(r.URL.Path, "/"), "/"); found && rest == "postback" && d.pbAuth.matchesSecret(seg) {
			d.handlePostback(w, r, seg)
			return
		}
	}

	// Postback pixel: ?pb=KEY&cid=<id;cid>&profit=X.
	if pb := query.Get("pb"); pb != "" {
		if ch := d.clickhouse(); d.postbackKey != "" && pb == d.postbackKey && ch != nil {
			cid := query.Get("cid")
			profit, _ := strconv.ParseFloat(query.Get("profit"), 64)
			status := strings.ToLower(strings.TrimSpace(query.Get("status")))
			if cid != "" {
				sctx, cancel := context.WithTimeout(r.Context(), postbackStoreTimeout)
				res, err := ch.RecordPostback(sctx, cid, profit, status)
				cancel()
				if err != nil {
					d.log.Warn("postback failed", "cid", cid, "err", err)
				} else {
					_, subs := config.ApplySource(query, nil)
					d.queueOutbound(res, cid, profit, status, subs)
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// API-client mode: ?api=base64(JSON). Input is taken from the client request.
	var apiReq *apiRequest
	apiMode := false
	if a := query.Get("api"); a != "" {
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
			if d.traffic.on(grp) {
				d.recordClick(clickPath{r: r, began: began, apiMode: apiMode, entered: grp, served: grp, ip: ip,
					stream: "-", action: "forbidden", notes: []string{"antiflood"}})
			}
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
	proxy := isProxy(d, ip, g.Org, g.OrgAlt)
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
		if c := query.Get("cid"); d.cids.reusedCID(c) {
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
	// The keyword itself is chosen by sourceReads, per serving group.
	apiKey := ""
	lang := langOf(r)
	domain := domainOf(ref)
	pars := extraParams(r)
	if apiMode {
		apiKey, lang, domain, pars = config.LimitBytes(apiReq.Key, config.MaxValueBytes), apiReq.Lang, apiReq.Domain, apiReq.Pars
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
		ASNAlt:     g.ASNAlt,
		Org:        g.Org,
		OrgAlt:     g.OrgAlt,
		Timezone:   g.Timezone,
		TZOffset:   tzOffset,
		Query:      query,
	}
	subs, extras := d.sourceReads(grp, query, &v, apiMode, apiKey)
	// The installation profile decides the bot flag before a stream is chosen.
	// The selected stream still decides where a bot is sent.
	profile := config.Bots{}
	if d.sigs != nil && d.lists != nil {
		profile = d.botProfile()
	}
	botIn := botInput{IP: ip, UA: ua, Ref: refOrEmpty(ref), Lang: v.Lang, Proxy: proxy, Device: device}
	profileBot, profileWhy := detectBotWhy(d.lists, d.sigs, d.dataDir, botIn, profile)
	v.Bot = profileBot != detect.BotNone
	// Output defaults — from the group; the stream overrides them.
	//
	// A stream (or a group's default) of type "group" hands the visitor to
	// another group: selection runs again over that group's streams with the
	// same visitor. Everything decided before this point — antiflood,
	// uniqueness, country source — belongs to the group the visitor entered
	// through; what follows, including the log line, belongs to the group
	// that finally served them.
	now := time.Now()
	originID, originName, chainRoot, issued := d.resolveOrigin(query, grp, apiMode, apiReq, now)
	// A redirect from another campaign of this tracker names that campaign in
	// origin. Its mapping already collected the click; this campaign fills only
	// the fields that mapping left empty.
	if !apiMode && strings.TrimSpace(query.Get("origin")) != "" && originID != "" && grp != nil && originID != grp.ID && d.groups != nil {
		if og, ok := d.groups.Get(originID); ok && og != nil && og.Status {
			baseSubs, baseExtras := d.sourceReads(og, query, &v, apiMode, apiKey)
			kept := d.fillCollected(grp, query, &v, collectedClick{subs: baseSubs, extras: baseExtras}, apiMode, apiKey)
			subs, extras = kept.subs, kept.extras
		}
	}

	var (
		streamName, redirect, outRaw, ctype string
		selStream                           *config.Stream
		via                                 string // the first link followed: "<group id>/<stream>"
		entered                             = grp  // the campaign the visitor entered
		hops                                []router.HopDraft
	)
	for hop := 0; ; hop++ {
		deps := router.Deps{IP: d.lists}
		if d.counters != nil {
			deps.Limiter = limiter{c: d.counters, ctx: rctx, id: grp.ID}
		}
		var rec *router.HopRecorder
		if d.traffic.on(entered) || d.traffic.on(grp) {
			rec = &router.HopRecorder{}
			deps.Seen = rec.Note
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
		if rec != nil {
			hops = append(hops, router.HopDraft{Group: grp.ID, Rec: rec, V: v, Selected: streamName})
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
			if rec != nil {
				hops[len(hops)-1].Error = "group_link"
				d.recordClick(clickPath{r: r, began: began, apiMode: apiMode, entered: entered, served: grp, cid: cid,
					ip: ip, geo: g, info: info, v: v, country: country, operator: operator, unique: loggedUnique,
					subs: subs, stream: streamName, action: "trash", hops: hops, notes: []string{"group_link"}})
			}
			trashResult(d.trashMode, d.trashURL).Write(w)
			return
		}
		if rec != nil {
			hops[len(hops)-1].Link = next.ID
		}
		if via == "" {
			via = grp.ID + "/" + streamName
		}
		grp = next
		// Keep what the entry campaign collected. This campaign's mapping fills
		// only a keyword, sub id or extra that is still empty.
		kept := d.fillCollected(grp, query, &v, collectedClick{subs: subs, extras: extras}, apiMode, apiKey)
		subs, extras = kept.subs, kept.extras
	}

	// 4.5) Bot detection by the selected stream's toggles (after selection).
	botsCfg := config.Bots{CheckUA: true} // no stream — signatures only (for the log)
	if selStream != nil {
		botsCfg = selStream.Bots
	}
	bot, botWhy := profileBot, profileWhy
	if !sameBotChecks(botsCfg, profile) {
		bot, botWhy = detectBotWhy(d.lists, d.sigs, d.dataDir, botIn, botsCfg)
	}
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
	// The log, the header and keyword collection see the bot that routed the visitor
	// even when the selected stream has no bot toggles of its own.
	if bot == detect.BotNone {
		bot, botWhy = profileBot, profileWhy
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
	cat := d.catalogs()
	cat.OfferFull = d.offerFull(rctx, cat.Offers)
	cat.Country = country
	var offerID, landingID, networkID string
	if selStream != nil && !botServed && (len(selStream.Out.Offers) > 0 || len(selStream.Out.Landings) > 0) {
		served := router.ResolveServed(selStream, cat, offerStep, redirect, outRaw, nil)
		redirect, outRaw = served.Redirect, served.Out
		if served.OfferID != "" || served.LandingID != "" {
			offerID, landingID = served.OfferID, served.LandingID
			if o, ok := cat.Offers[served.OfferID]; ok {
				networkID = o.NetworkID
			}
		}
	}
	if offerID == "" && landingID == "" {
		offerID, landingID, networkID = clickIDs(selStream, cat, outRaw, offerStep)
	}
	// A network query is part of the partner URL. An action page is not that URL.
	if networkID != "" {
		if o, ok := cat.Offers[offerID]; !ok || o.SendsURL() {
			outRaw = appendOfferQuery(outRaw, d.offerQuery(networkID))
		}
	}
	clickRev := 0.0
	if offerID != "" {
		offer := cat.Offers[offerID]
		outRaw = offer.ApplyValues(outRaw)
		if rev, ok := offer.ClickRevenue(); ok {
			clickRev = rev
		}
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
	pathRoot, pathIssued := chainRoot, issued
	if pathRoot == "" {
		pathRoot, pathIssued = cid, now
	}
	originTok := ""
	if originID != "" && d.cids != nil {
		originTok = d.cids.signPath(originID, pathRoot, pathIssued)
	}
	md := render.MacroDeps{
		Key: v.Key, Path: r.Host, IP: ip.String(), Country: country, City: g.City,
		Region: g.Region, Lang: v.Lang, Device: device, Operator: operator,
		Domain: v.Domain, UserAgent: ua, CID: cid, Pars: pars, SubIDs: subs,
		ASN: asnText(g.ASN), Org: dashEmpty(g.Org), Timezone: g.Timezone, TZOffset: tzOffset,
		DataDir: d.dataDir, Offer: offerStepURL(gid, cid, r.URL.RawQuery, originTok), Origin: originTok,
		RootCID: chainRoot,
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
		if redirect == "http_redirect" {
			res.Location = d.withOrigin(res.Location, originTok)
			// Link names belong to the campaign that first collected the click.
			// A path token already names that campaign; otherwise it is the one
			// the visitor entered on this request.
			named := entered
			if originID != "" && entered != nil && originID != entered.ID && d.groups != nil {
				if og, ok := d.groups.Get(originID); ok && og != nil && og.Status {
					named = og
				}
			}
			res.Location = d.carryClickParams(res.Location, named, v.Key, subs, extras)
		}
	}

	// 6) Event log — asynchronous, the response does not wait for the write (phase 4).
	costCents, costCur, costOK := config.IncomingCost(query, grp.CostParam, grp.CurrencyParam)
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
			Ts: now, GroupID: grp.ID, GroupName: grp.Name,
			OriginID: originID, OriginName: originName, RootCID: chainRoot,
			Stream: streamName, Out: logbuf.CapOut(out), Redirect: logRedirect, Device: device,
			Operator: operator, Country: country, City: g.City, Region: g.Region,
			Lang: v.Lang, Uniq: b2u8(loggedUnique), Bot: bot, IP: ip.String(),
			Referer: v.Referer, UserAgent: ua, Domain: v.Domain, Keyword: v.Key,
			OS: info.OS, OSVersion: info.OSVersion, Browser: info.Browser,
			BrowserV: info.BrowserVer, Brand: info.Brand, CID: cid,
			ASN: g.ASN, Org: dashEmpty(g.Org), Timezone: dashEmpty(g.Timezone), Via: via,
			OfferID: offerID, LandingID: landingID, SourceID: grp.SourceID, NetworkID: networkID,
			Subs: storedSubs(subs), CostCents: costCents, CostCur: costCur, Revenue: clickRev,
			Site: extras.Site, AdCampaignID: extras.AdCampaignID, CreativeID: extras.CreativeID,
			ExternalID: extras.ExternalID, XRW: requestXRW(r, apiMode),
			DeviceModel: info.Model, Step: b2u8(ownStep),
		})
	}

	// Keyword collection.
	if bot == detect.BotNone {
		if grp.SaveKeys && v.Key != "" {
			d.keyFiles.add(grp.ID, v.Key, false)
		}
		if grp.SaveKeysSE {
			if kse := parseSEKeyword(ref); kse != "" {
				d.keyFiles.add(grp.ID, kse, true)
			}
		}
	}

	// The click path for the campaign traffic log: queued without waiting.
	if d.traffic != nil {
		var notes []string
		if logRedirect == "curl_error" {
			notes = append(notes, "curl_error")
		}
		d.recordClick(clickPath{r: r, began: began, apiMode: apiMode, entered: entered, served: grp, cid: cid, step: offerStep,
			ip: ip, geo: g, info: info, v: v, country: country, operator: operator, bot: bot, botWhy: botWhy, botServe: botServed,
			unique: loggedUnique, subs: subs, via: via, stream: streamName, action: logRedirect, out: out,
			offerID: offerID, landing: landingID, network: networkID, saved: d.logs != nil, hops: hops, notes: notes})
	}

	// Diagnostic headers (before writing the response) + render.
	h := w.Header()
	h.Set("X-Emtds-Bot", emptyDash(bot))
	if botWhy != "" {
		h.Set("X-Emtds-Bot-Reason", botWhy)
	}
	h.Set("X-Emtds-Device", device)
	h.Set("X-Emtds-Country", country)
	h.Set("X-Emtds-Stream", streamName)
	h.Set("X-Emtds-Uniq", b2yn(unique))
	if redirect == "landing" {
		d.serveLocalLanding(w, r, strings.TrimSpace(outRaw), md)
		return
	}
	res.Write(w)
}

// clickIDs names the offer or landing whose template equals outRaw.
// A stream offer counts as the offer that serves it (Catalogs.Serve), so a click sent to a spare
// offer belongs to the spare one. The network id is the one stored on that offer. No match leaves
// the ids empty.
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
		served, ok := cat.Serve(ref.ID)
		if ok && cat.Offers[served].URL == outRaw {
			return served, "", cat.Offers[served].NetworkID
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

// storedSubs copies every sub id into the click log.
// subs is the visitor's sub_id_1 through sub_id_30. The result has the same
// length. It does not fail.
func storedSubs(subs [config.MaxSubID]string) [config.MaxSubID]string {
	return subs
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

// resolveOrigin picks the first-entry campaign and the chain root for this visit.
//
// query is the parsed request query and carries ?origin=. grp is the group the
// visitor entered. apiMode marks an api request; apiReq holds its campaign id.
// now is the verification time. A path token names the root click and its issue
// time. An old origin token names only the campaign. A token that does not
// verify is ignored. A path token whose campaign is off or missing keeps the
// root click and uses the entry campaign. In api mode the campaign id from the
// request is the origin and this click is the root.
// It returns the campaign id, its display name, the root click id and the root
// issue time. An empty root click id means this click is the root. It does not
// fail.
func (d *engineDeps) resolveOrigin(query url.Values, grp *config.Group, apiMode bool, apiReq *apiRequest, now time.Time) (id, name, rootCID string, issued time.Time) {
	entryID, entryName := "", ""
	if grp != nil {
		entryID, entryName = grp.ID, grp.Name
	}
	if apiMode && apiReq != nil {
		id = strings.TrimSpace(apiReq.ID)
		if id != "" && d.groups != nil {
			if g, ok := d.groups.Get(id); ok {
				name = g.Name
			}
		}
		return id, name, "", time.Time{}
	}
	if tok := strings.TrimSpace(query.Get("origin")); tok != "" && d.cids != nil {
		if gid, rc, iss, ok := d.cids.verifyPath(tok, now); ok {
			if d.groups != nil {
				if g, found := d.groups.Get(gid); found && g.Status {
					return g.ID, g.Name, rc, iss
				}
			}
			return entryID, entryName, rc, iss
		}
		if gid, ok := d.cids.verifyOrigin(tok); ok && d.groups != nil {
			if g, found := d.groups.Get(gid); found && g.Status {
				return g.ID, g.Name, "", time.Time{}
			}
		}
	}
	return entryID, entryName, "", time.Time{}
}
