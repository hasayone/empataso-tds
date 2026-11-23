package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/router"
	"github.com/hasayone/empataso-tds/internal/store"
)

const (
	// trafficLogPage is how many entries the list answers with when limit is not given.
	trafficLogPage = 100
)

// TrafficLogReader reads the click paths the engine stores in Redis.
// Recent returns up to n entries of a campaign, newest first. Click returns the
// stored paths of one click id in the order they were taken. Limits returns what
// the log keeps. Both reads return unpacked JSON of a router.Trace.
// *store.TrafficLog implements it.
type TrafficLogReader interface {
	Recent(ctx context.Context, group string, n int) ([][]byte, error)
	Click(ctx context.Context, cid string) ([][]byte, error)
	Limits() store.TrafficLogLimits
}

// trafficLogInfo is the part of every traffic-log answer that tells how the log works.
type trafficLogInfo struct {
	Available     bool `json:"available"`       // Redis keeps the log
	Enabled       bool `json:"enabled"`         // the campaign records its click paths
	Max           int  `json:"max"`             // entries kept per campaign
	TTLHours      int  `json:"ttl_hours"`       // how long an idle campaign log lives
	ClickTTLHours int  `json:"click_ttl_hours"` // how long a click path can be opened from the visit
}

// trafficLogList is the answer of GET /api/campaigns/{id}/traffic-log.
type trafficLogList struct {
	trafficLogInfo
	Total   int            `json:"total"` // entries read that matched; a search reads the whole log, a plain list only the page
	Entries []router.Trace `json:"entries"`
}

// trafficLogClick is the answer of GET /api/campaigns/{id}/traffic-log/{cid}.
type trafficLogClick struct {
	trafficLogInfo
	Entries []router.Trace `json:"entries"`
}

// trafficInfo describes the log for campaign g.
func (s *Server) trafficInfo(g config.Group) trafficLogInfo {
	info := trafficLogInfo{Enabled: !g.TrafficLogOff}
	if s.cfg.Traffic == nil {
		return info
	}
	l := s.cfg.Traffic.Limits()
	info.Available = l.PerCampaign > 0
	info.Max = l.PerCampaign
	info.TTLHours = int(l.CampaignTTL.Hours())
	info.ClickTTLHours = int(l.ClickTTL.Hours())
	return info
}

// visibleCampaign finds the campaign named by the {id} path value that the
// caller may see. It answers 503 or 404 itself and returns false when there is
// none.
func (s *Server) visibleCampaign(w http.ResponseWriter, r *http.Request) (config.Group, bool) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return config.Group{}, false
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return config.Group{}, false
	}
	id := r.PathValue("id")
	v := s.viewerFrom(r.Context())
	for _, g := range groups {
		if g.ID == id && v.seesBoard(g.BoardID) && v.canSee(resCampaigns, campaignRef(g)) {
			return g, true
		}
	}
	writeErr(w, http.StatusNotFound, "not found")
	return config.Group{}, false
}

// decodeTraces parses stored entries into traces, skipping one that is not a trace.
func decodeTraces(raw [][]byte) []router.Trace {
	out := make([]router.Trace, 0, len(raw))
	for _, b := range raw {
		var t router.Trace
		if json.Unmarshal(b, &t) == nil {
			out = append(out, t)
		}
	}
	return out
}

// anyHidden reports whether v has at least one of the journal fields hidden.
func anyHidden(v viewer, names ...string) bool {
	for _, n := range names {
		if fieldHidden(v, n) {
			return true
		}
	}
	return false
}

// redactTrace removes from t every value of a journal field that v may not see,
// so a role that hides a field gets no other way to read it. It changes t in place.
// Headers carry the address, so they go with the ip; the address and the query of
// the requested URL go when any value they could carry is hidden.
func redactTrace(v viewer, t *router.Trace) {
	if v.admin || len(v.hidden) == 0 {
		return
	}
	hide := func(name string, vals ...*string) {
		if fieldHidden(v, name) {
			for _, p := range vals {
				*p = ""
			}
		}
	}
	vi := &t.Visitor
	hide("cid", &t.CID)
	hide("campaign", &t.Name)
	hide("redirect", &t.Action)
	hide("out", &t.Out)
	hide("country", &vi.Country)
	hide("region", &vi.Region)
	hide("city", &vi.City)
	hide("timezone", &vi.Timezone)
	hide("org", &vi.Org)
	hide("operator", &vi.Operator)
	hide("device", &vi.Device)
	hide("os", &vi.OS)
	hide("os_version", &vi.OSVersion)
	hide("browser", &vi.Browser)
	hide("browser_ver", &vi.BrowserVer)
	hide("brand", &vi.Brand)
	hide("device_model", &vi.Model)
	hide("lang", &vi.Lang)
	hide("referer", &vi.Referer)
	hide("keyword", &vi.Keyword)
	hide("useragent", &vi.UserAgent)
	hide("bot", &vi.Bot, &vi.BotWhy)
	if fieldHidden(v, "asn") {
		vi.ASN = 0
	}
	if fieldHidden(v, "ip") {
		t.IP, t.Headers = "", nil
	}
	if anyHidden(v, "keyword", "referer", "ip") || len(hiddenSubIndexes(v)) > 0 {
		if i := strings.IndexByte(t.URL, '?'); i >= 0 {
			t.URL = t.URL[:i]
		}
	}
	redactSubs(v, vi)
	if fieldHidden(v, "stream") {
		t.Stream, t.Via = "", ""
		for i := range t.Hops {
			t.Hops[i].Selected = ""
			for j := range t.Hops[i].Streams {
				t.Hops[i].Streams[j].Name = "#" + strconv.Itoa(j+1)
			}
		}
	}
}

// redactSubs drops the Sub IDs v may not see. The slots past the rule slots are
// read from the query under any name, so they go with any hidden Sub ID.
func redactSubs(v viewer, vi *router.VisitorInfo) {
	anySub := len(hiddenSubIndexes(v)) > 0
	for key := range vi.Subs {
		n, _ := strconv.Atoi(strings.TrimPrefix(key, "sub_id_"))
		if fieldHidden(v, key) || (anySub && n > config.MaxRuleSubID) {
			delete(vi.Subs, key)
		}
	}
	if len(vi.Subs) == 0 {
		vi.Subs = nil
	}
}

// matchesSearch reports whether every word of q occurs in the trace text. An
// empty q matches everything.
func matchesSearch(t *router.Trace, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	text := t.SearchText()
	for _, w := range terms {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// handleTrafficLogGet answers the latest click paths of a campaign, newest
// first, filtered by the words of ?q= and cut to ?limit= (100 by default, at most
// what the log keeps). Without Redis it answers an empty list with available false.
func (s *Server) handleTrafficLogGet(w http.ResponseWriter, r *http.Request) {
	g, ok := s.visibleCampaign(w, r)
	if !ok {
		return
	}
	out := trafficLogList{trafficLogInfo: s.trafficInfo(g), Entries: []router.Trace{}}
	if !out.Available {
		writeJSON(w, http.StatusOK, out)
		return
	}
	limit := trafficLogPage
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	limit = min(limit, out.Max)
	terms := strings.Fields(strings.ToLower(r.URL.Query().Get("q")))
	// A search reads the whole log; without one only the page is read.
	want := limit
	if len(terms) > 0 {
		want = out.Max
	}
	raw, err := s.cfg.Traffic.Recent(r.Context(), g.ID, want)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "traffic log unavailable")
		return
	}
	v := s.viewerFrom(r.Context())
	for _, t := range decodeTraces(raw) {
		redactTrace(v, &t)
		if !matchesSearch(&t, terms) {
			continue
		}
		out.Total++
		if len(out.Entries) < limit {
			out.Entries = append(out.Entries, t)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTrafficLogClick answers the stored paths of one click of the campaign.
// A click that expired, or one that belongs to another campaign, answers an
// empty list.
func (s *Server) handleTrafficLogClick(w http.ResponseWriter, r *http.Request) {
	g, ok := s.visibleCampaign(w, r)
	if !ok {
		return
	}
	out := trafficLogClick{trafficLogInfo: s.trafficInfo(g), Entries: []router.Trace{}}
	if !out.Available {
		writeJSON(w, http.StatusOK, out)
		return
	}
	raw, err := s.cfg.Traffic.Click(r.Context(), r.PathValue("cid"))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "traffic log unavailable")
		return
	}
	v := s.viewerFrom(r.Context())
	for _, t := range decodeTraces(raw) {
		if t.Campaign == g.ID || tracePassed(t, g.ID) {
			redactTrace(v, &t)
			out.Entries = append(out.Entries, t)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// tracePassed reports whether the click went through campaign id on any hop.
func tracePassed(t router.Trace, id string) bool {
	for _, h := range t.Hops {
		if h.Group == id {
			return true
		}
	}
	return false
}

// handleTrafficLogPut switches the recording of click paths for one campaign.
// The body is {"enabled": bool}; the answer is the log state. It stores the
// switch on the campaign, so the engine reads it with the rest of the config.
func (s *Server) handleTrafficLogPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "enabled is required")
		return
	}
	v := s.viewerFrom(r.Context())
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	id := r.PathValue("id")
	at := -1
	for i := range groups {
		if groups[i].ID != id {
			continue
		}
		ref := campaignRef(groups[i])
		if !v.seesBoard(groups[i].BoardID) || !v.canSee(resCampaigns, ref) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if !v.canWrite(resCampaigns, ref) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		at = i
		break
	}
	if at < 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	groups[at].TrafficLogOff = !*body.Enabled
	if err := s.cfg.Groups.Save(r.Context(), groups); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, s.trafficInfo(groups[at]))
}
