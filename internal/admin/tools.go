package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/cron"
	"github.com/hasayone/empataso-tds/internal/detect"
	"github.com/hasayone/empataso-tds/internal/geo"
	"github.com/hasayone/empataso-tds/internal/ipindex"
	"github.com/hasayone/empataso-tds/internal/router"
)

// GeoInfo is what the panel needs from the geo resolver: answers and the list
// of loaded databases (geo.DB provides both).
type GeoInfo interface {
	geo.Resolver
	Databases() []geo.Info
}

// dirLists answers "is this IP in list <name>" straight from the .dat files.
// The engine keeps the lists in memory; the panel asks a few times a day, so
// it reads the file when asked and remembers it for the rest of the request.
type dirLists struct {
	dir   string
	cache map[string]*ipindex.Index
}

// Lookup returns the list label for ip in name.dat, loading the index once per name.
func (d *dirLists) Lookup(name string, ip netip.Addr) (string, bool) {
	if d.dir == "" || !safeSeg.MatchString(name) {
		return "", false
	}
	idx, ok := d.cache[name]
	if !ok {
		idx, _ = ipindex.LoadFile(filepath.Join(d.dir, name+".dat"))
		if d.cache == nil {
			d.cache = map[string]*ipindex.Index{}
		}
		d.cache[name] = idx
	}
	if idx == nil {
		return "", false
	}
	return idx.Lookup(ip)
}

// names lists the IP lists in the directory (files the index can parse at
// least one range from are IP lists; signatures and separation files are not).
func (d *dirLists) names() []string {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".dat") {
			out = append(out, strings.TrimSuffix(n, ".dat"))
		}
	}
	sort.Strings(out)
	return out
}

type visitorInfo struct {
	IP       string `json:"ip"`
	Country  string `json:"country"`
	City     string `json:"city"`
	Region   string `json:"region"`
	Timezone string `json:"timezone"`
	UTC      string `json:"utc"`
	ASN      uint32 `json:"asn"`
	ASNAlt   uint32 `json:"asn_alt,omitempty"` // the number the secondary ASN database gives, when it differs
	Org      string `json:"org"`
	OrgAlt   string `json:"org_alt,omitempty"` // the organization as the secondary ASN database spells it
	Operator string `json:"operator"`
	Proxy    bool   `json:"proxy"`
	Device   string `json:"device,omitempty"`
	OS       string `json:"os,omitempty"`
	Browser  string `json:"browser,omitempty"`
	Brand    string `json:"brand,omitempty"`
}

// resolve fills geo and visitor panel fields for ip using Geo and dirLists.
func (s *Server) resolve(ip netip.Addr, lists *dirLists) (geo.Geo, visitorInfo) {
	g := geo.None()
	if s.cfg.Geo != nil {
		g = s.cfg.Geo.Resolve(ip)
	}
	op := router.Empty
	if label, ok := lists.Lookup("wap", ip); ok && label != "" {
		op = label
	}
	proxy := detect.IsProxy(lists, ip, detect.LoadProxyOrg(s.cfg.DataDir), g.Org, g.OrgAlt)
	return g, visitorInfo{
		IP: ip.String(), Country: g.Country, City: g.City, Region: g.Region,
		Timezone: g.Timezone, UTC: g.UTCOffset(time.Now()), ASN: g.ASN, ASNAlt: g.ASNAlt, Org: g.Org, OrgAlt: g.OrgAlt,
		Operator: op, Proxy: proxy,
	}
}

// handleLookup — what the engine would learn from an IP address: geo, network,
// and which of the .dat lists it falls into.
func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	ip, err := netip.ParseAddr(strings.TrimSpace(r.URL.Query().Get("ip")))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "not an IP address")
		return
	}
	ip = ip.Unmap()
	lists := &dirLists{dir: s.cfg.DataDir}
	_, vi := s.resolve(ip, lists)

	type hit struct {
		List  string `json:"list"`
		Label string `json:"label,omitempty"`
	}
	hits := []hit{}
	for _, name := range lists.names() {
		if label, ok := lists.Lookup(name, ip); ok {
			hits = append(hits, hit{List: name, Label: label})
		}
	}
	dbs := []geo.Info{}
	if s.cfg.Geo != nil {
		dbs = append(dbs, s.cfg.Geo.Databases()...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"visitor": vi, "lists": hits, "databases": dbs})
}

type simRequest struct {
	Groups    []config.Group   `json:"groups"` // as edited in the browser, saved or not
	Offers    []config.Offer   `json:"offers"`
	Landings  []config.Landing `json:"landings"`
	Group     string           `json:"group"`
	OfferStep bool             `json:"offer_step"`
	Roll      *int             `json:"roll"`
	Visitor   struct {
		IP        string `json:"ip"`
		UserAgent string `json:"useragent"`
		Referer   string `json:"referer"`
		Lang      string `json:"lang"`
		Key       string `json:"key"`
		Query     string `json:"query"`
		Country   string `json:"country"` // overrides the database (what a CDN header would say)
		Repeat    bool   `json:"repeat"`  // a returning, non-unique visitor
	} `json:"visitor"`
}

type simStream struct {
	Name  string `json:"name"`
	On    bool   `json:"on"`
	Match bool   `json:"match"`
	Why   string `json:"why,omitempty"` // the rule that rejected the visitor
}

type simStep struct {
	Group    string      `json:"group"`
	Streams  []simStream `json:"streams"`
	Selected string      `json:"selected"` // stream name, or "-" for the group default
	Redirect string      `json:"redirect"`
	Out      string      `json:"out"`
	Error    string      `json:"error,omitempty"`
}

// handleSimulate walks a visitor through a group the way the engine's router
// would and reports, for every stream, whether it matches and which rule
// turned the visitor away. It works on the groups sent in the request, so an
// edit can be tried before it is saved.
//
// Not simulated: impression limits (they spend a counter), bot detection and
// the antiflood — those depend on live state.
func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var req simRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(req.Visitor.IP))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "not an IP address")
		return
	}
	ip = ip.Unmap()
	find := func(id string) *config.Group {
		for i := range req.Groups {
			g := &req.Groups[i]
			if g.ID == id {
				return g
			}
			for _, a := range g.Aliases {
				if a == id {
					return g
				}
			}
		}
		return nil
	}
	grp := find(req.Group)
	if grp == nil {
		writeErr(w, http.StatusBadRequest, "unknown group")
		return
	}

	lists := &dirLists{dir: s.cfg.DataDir}
	g, vi := s.resolve(ip, lists)
	if cc := strings.ToLower(strings.TrimSpace(req.Visitor.Country)); len(cc) == 2 {
		if g.Country == geo.Empty || grp.Geo == "cf" {
			vi.Country = cc
		}
	}
	info := detect.Parse(req.Visitor.UserAgent)
	vi.Device, vi.OS, vi.Browser, vi.Brand = info.Device, strings.TrimSpace(info.OS+" "+info.OSVersion),
		strings.TrimSpace(info.Browser+" "+info.BrowserVer), info.Brand
	q, _ := url.ParseQuery(strings.TrimPrefix(req.Visitor.Query, "?"))
	ref := strings.TrimSpace(req.Visitor.Referer)
	domain := simDomain(ref)
	if ref == "" {
		ref = router.Empty
	}
	lang := strings.ToLower(strings.TrimSpace(req.Visitor.Lang))
	if lang == "" {
		lang = router.Empty
	}
	subsOf := s.simSubs(r.Context(), q)
	v := router.Visitor{
		Lang: lang, Country: vi.Country, City: g.City, Region: g.Region,
		UA: req.Visitor.UserAgent, Referer: ref, Domain: domain, Key: req.Visitor.Key,
		Device: info.Device, Operator: vi.Operator,
		OS: info.OS, OSVersion: info.OSVersion, Browser: info.Browser, BrowserVer: info.BrowserVer, Brand: info.Brand, Model: info.Model,
		Unique: !req.Visitor.Repeat, IP: ip, Proxy: vi.Proxy,
		ASN: g.ASN, ASNAlt: g.ASNAlt, Org: g.Org, OrgAlt: g.OrgAlt, Timezone: g.Timezone, TZOffset: vi.UTC, Query: q, Subs: subsOf(grp),
	}
	deps := router.Deps{IP: lists} // no Limiter: asking must not spend a limit
	if req.Roll != nil {
		roll := *req.Roll
		deps.Roll = func(n int) int { return roll }
	}
	cats := s.simCatalogs(r.Context(), req, s.viewerFrom(r.Context()))

	var path []simStep
	for hop := 0; ; hop++ {
		step := simStep{Group: grp.ID, Selected: "-", Redirect: grp.Redirect, Out: grp.Out, Streams: []simStream{}}
		chosen, _ := router.Select(grp, v, deps)
		if chosen != nil {
			step.Selected = chosen.Name
			if chosen.Out.Redirect != "" {
				step.Redirect = chosen.Out.Redirect
			}
			step.Out = chosen.Out.Out
			step.Redirect, step.Out = router.ResolveOut(chosen, cats, req.OfferStep, step.Redirect, step.Out, deps.Roll)
			if len(chosen.Out.Offers) == 0 && len(chosen.Out.Landings) == 0 {
				if next, ok := router.ApplyOutShares(step.Out, chosen.Out.OutShares, deps.Roll); ok {
					step.Out = next
				}
			}
		}
		for i := range grp.Streams {
			st := &grp.Streams[i]
			ss := simStream{Name: st.Name, On: st.Status}
			if st.Status {
				ss.Why = router.Why(st, v, deps)
				ss.Match = ss.Why == ""
			}
			step.Streams = append(step.Streams, ss)
		}
		if step.Redirect != "group" {
			path = append(path, step)
			break
		}
		next := find(strings.TrimSpace(step.Out))
		switch {
		case next == nil:
			step.Error = "links to a group that does not exist: " + strconv.Quote(step.Out)
		case !next.Status:
			step.Error = "links to a disabled group: " + next.ID
		case next == grp:
			step.Error = "links to itself"
		case hop >= config.MaxGroupHops:
			step.Error = "too many group links in a row"
		}
		path = append(path, step)
		if step.Error != "" {
			break
		}
		grp, v.Subs = next, subsOf(next)
	}
	writeJSON(w, http.StatusOK, map[string]any{"visitor": vi, "unique": v.Unique, "lang": lang, "path": path})
}

// simDomain returns the referer host without "www." for the simulator, or
// router.Empty when ref is not an absolute URL with a host.
func simDomain(ref string) string {
	if u, err := url.Parse(ref); err == nil && u.Host != "" {
		return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	}
	return router.Empty
}

// simSubs returns a function that reads the sub ids of a group the way the
// engine does: through the traffic source the group names.
//
// ctx bounds the read of the source store; q is the simulated query. The sources
// are read once per simulation. Without a store, or on a read error, the plain
// parameter names are used.
func (s *Server) simSubs(ctx context.Context, q url.Values) func(*config.Group) [config.MaxSubID]string {
	sources := s.simSources(ctx)
	return func(g *config.Group) [config.MaxSubID]string {
		_, subs := config.ApplySource(q, config.EffectiveSource(g, sources[g.SourceID]))
		return subs
	}
}

// simSources indexes the stored traffic sources by id for one simulation.
// ctx bounds the read. A missing store or a read error returns an empty map,
// so a group's sub ids are then read under their own names. The pointers in the
// map are never nil; a missing id yields a nil pointer, which ApplySource accepts.
func (s *Server) simSources(ctx context.Context) map[string]*config.TrafficSource {
	out := map[string]*config.TrafficSource{}
	if s.cfg.Sources == nil {
		return out
	}
	list, err := s.cfg.Sources.List(ctx)
	if err != nil {
		return out
	}
	for i := range list {
		out[list[i].ID] = &list[i]
	}
	return out
}

// simCatalogs builds the offer and landing maps for one simulation.
// An omitted list is read from the store and filtered by v. A list in the
// request replaces it unchanged, so an unsaved edit can be tried on its own.
func (s *Server) simCatalogs(ctx context.Context, req simRequest, v viewer) router.Catalogs {
	offers := req.Offers
	if offers == nil && s.cfg.Offers != nil {
		if list, err := s.cfg.Offers.List(ctx); err == nil {
			offers = visibleCatalog(v, &catalogHooks[config.Offer]{
				res:   resOffers,
				label: func(o config.Offer) string { return o.Group },
				owner: func(o config.Offer) string { return o.CreatedBy },
			}, func(o config.Offer) string { return o.ID }, list)
		}
	}
	landings := req.Landings
	if landings == nil && s.cfg.Landings != nil {
		if list, err := s.cfg.Landings.List(ctx); err == nil {
			landings = visibleCatalog(v, &catalogHooks[config.Landing]{
				res:   resLandings,
				label: func(l config.Landing) string { return l.Group },
				owner: func(l config.Landing) string { return l.CreatedBy },
			}, func(l config.Landing) string { return l.ID }, list)
		}
	}
	cat := router.Catalogs{
		Offers:   map[string]config.Offer{},
		Landings: map[string]config.Landing{},
	}
	for _, o := range offers {
		cat.Offers[o.ID] = o
	}
	for _, l := range landings {
		cat.Landings[l.ID] = l
	}
	return cat
}

// cronState is what the panel shows on the Automation page.
type cronState struct {
	Available bool           `json:"available"` // the admin knows where the cron config is
	Alive     bool           `json:"alive"`     // the service wrote a heartbeat recently
	Config    cron.Config    `json:"config"`    // secrets masked
	Status    cron.Status    `json:"status"`
	Jobs      []string       `json:"jobs"`
	Floors    map[string]int `json:"floors"`
	GeoPaths  struct {
		City     bool `json:"city"`
		ASN      bool `json:"asn"`
		CityFile bool `json:"city_file"` // city database file exists and is not empty
		ASNFile  bool `json:"asn_file"`  // asn database file exists and is not empty
		DBIP     bool `json:"dbip"`      // a path for the DB-IP ASN database is known (Config.GeoDBIPPath)
		DBIPFile bool `json:"dbip_file"` // DB-IP ASN database file exists and is not empty
	} `json:"geo_paths"`
	DBIPURL            string           `json:"dbip_url"`                     // URL template of the DB-IP ASN source, {YYYY-MM} is the month
	TelegramConfigured bool             `json:"telegram_configured"`          // stored token and chat id are both set
	KeitaroConfigured  bool             `json:"keitaro_configured"`           // tracker origin and key are both set
	ConfigError        string           `json:"config_error,omitempty"`       // Validate failure; never includes a secret
	KeitaroSyncState   *keitaroSyncFile `json:"keitaro_sync_state,omitempty"` // catch-up cursors; no secrets
}

// cronAliveWindow — the service ticks every few seconds; a heartbeat older
// than this means it is not running.
const cronAliveWindow = 30 * time.Second

// handleCronGet returns cron config, status, job list and panel flags for the Automation page.
func (s *Server) handleCronGet(w http.ResponseWriter, r *http.Request) {
	cfg := cron.Default()
	st := cronState{
		Jobs:   append(append([]string{}, cron.Jobs...), cron.AdminJobs...),
		Floors: cron.Floors(),
		Config: cfg.Redacted(), Status: cron.Status{Jobs: map[string]cron.JobStatus{}},
	}
	if s.cfg.CronFile != "" {
		st.Available = true
		loaded, err := cron.Load(s.cfg.CronFile)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "cron config unreadable")
			return
		}
		cfg = loaded
		st.Config = cfg.Redacted()
		if status, err := cron.ReadStatus(cron.StatusPath(s.cfg.CronFile)); err == nil {
			st.Status = status
			st.Alive = time.Since(status.Heartbeat) < cronAliveWindow
		}
	}
	if st.Status.Jobs == nil {
		st.Status.Jobs = map[string]cron.JobStatus{}
	}
	if syncSt, ok, err := s.loadKeitaroSync(); err == nil && ok {
		st.Status.Jobs[cron.JobKeitaroSync] = s.keitaroSyncJobStatus(syncSt)
		st.KeitaroSyncState = &syncSt
	}
	s.fillCronPanel(&st, cfg)
	writeJSON(w, http.StatusOK, st)
}

// fillCronPanel sets Telegram, config validity and geo file flags on st.
// The DB-IP flags (geo_paths.dbip, geo_paths.dbip_file) come from Config.GeoDBIPPath;
// dbip_url is cron.DBIPASNURL.
// cfg is the stored cron config before secrets are masked; st.Config is left
// unchanged. ConfigError is empty when cfg.Validate returns nil. A validator
// message that contains a bot token, a VirusTotal key or a geo password is
// replaced with "config is invalid".
func (s *Server) fillCronPanel(st *cronState, cfg cron.Config) {
	st.TelegramConfigured = cfg.Telegram.Configured()
	st.KeitaroConfigured = s.keitaroCreds().configured() && s.cfg.Warehouse != nil
	st.ConfigError = configErrorText(cfg)
	st.GeoPaths.City = s.cfg.GeoCityPath != ""
	st.GeoPaths.ASN = s.cfg.GeoASNPath != ""
	st.GeoPaths.CityFile = nonEmptyFile(s.cfg.GeoCityPath)
	st.GeoPaths.ASNFile = nonEmptyFile(s.cfg.GeoASNPath)
	st.DBIPURL = cron.DBIPASNURL
	st.GeoPaths.DBIP = s.cfg.GeoDBIPPath != ""
	st.GeoPaths.DBIPFile = nonEmptyFile(s.cfg.GeoDBIPPath)
}

// configErrorText returns the text of cfg.Validate, or "" when cfg is usable.
// It returns "config is invalid" when that text contains a secret field value,
// so the panel does not echo a token or a key.
func configErrorText(cfg cron.Config) string {
	err := cfg.Validate()
	if err == nil {
		return ""
	}
	msg := err.Error()
	secrets := []string{cfg.Telegram.Token, cfg.VirusTotal.APIKey}
	for _, src := range cfg.GeoDB.Sources {
		secrets = append(secrets, src.Password)
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(msg, secret) {
			return "config is invalid"
		}
	}
	return msg
}

// keitaroSyncSectionAbsent is true when PUT body omits keitaro_sync or sends JSON null.
func keitaroSyncSectionAbsent(raw map[string]json.RawMessage) bool {
	v, ok := raw["keitaro_sync"]
	if !ok {
		return true
	}
	return string(v) == "null"
}

// nonEmptyFile reports whether path is an existing file whose size is greater
// than zero. A missing path, a directory, an empty file and any Stat error
// return false.
func nonEmptyFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Size() > 0
}

// handleCronSave validates and writes cron.json; absent keitaro_sync keeps the stored section.
// The seeded keys of the stored config are kept whatever the body says, so a source
// the user removed is not offered again.
func (s *Server) handleCronSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.CronFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "cron is not configured (EMTDS_CRON_FILE)")
		return
	}
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	var cfg cron.Config
	if err := json.Unmarshal(rawBody, &cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	stored, err := cron.Load(s.cfg.CronFile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cron config unreadable")
		return
	}
	cfg = cfg.KeepSecrets(stored).KeepSeeded(stored)
	if keitaroSyncSectionAbsent(raw) {
		cfg.KeitaroSync = stored.KeitaroSync
	}
	if err := cfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cron.CheckIntervals(cfg); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cron.Save(s.cfg.CronFile, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// handleCronRun triggers a cron job or admin keitaro_sync catch-up (202) via run file or kick.
func (s *Server) handleCronRun(w http.ResponseWriter, r *http.Request) {
	if s.cfg.CronFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "cron is not configured (EMTDS_CRON_FILE)")
		return
	}
	var body struct {
		Job string `json:"job"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if body.Job == cron.JobTelegramTest && sessionFrom(r.Context()).API {
		writeErr(w, http.StatusForbidden, "cookie session required")
		return
	}
	if body.Job == cron.JobKeitaroSync {
		if err := s.runKeitaroSyncNow(); err != nil {
			msg := err.Error()
			if msg == "keitaro not configured" || msg == keitaroSyncBusyMsg {
				writeErr(w, http.StatusConflict, msg)
				return
			}
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
		return
	}
	if err := cron.RequestRun(s.cfg.CronFile, body.Job); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}
