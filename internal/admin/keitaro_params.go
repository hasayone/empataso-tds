package admin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

// forParameters checks a parameters_only request.
// parameters_only reads campaign and traffic-source parameters and fills only the empty
// parameter names and tokens of imported campaigns. It combines with dry_run and with
// either campaign_ids or all_imported, and with nothing else.
// It returns a client message, or an empty string when the request is valid.
// A request that is not parameters_only returns an empty string: forFilters already
// refused campaign_ids, all_imported and dry_run standing alone.
func (req *keitaroImportReq) forParameters() string {
	if !req.ParametersOnly {
		return ""
	}
	if req.FiltersOnly || req.catalog() || req.Clicks || req.Conversions || req.CampaignID != 0 {
		return "parameters_only does not combine with other import flags or campaign_id"
	}
	if req.AllImported == (len(req.CampaignIDs) > 0) {
		return "parameters_only needs either campaign_ids or all_imported"
	}
	if len(req.CampaignIDs) > keitaroRefilterMax {
		return "campaign_ids holds more than " + strconv.Itoa(keitaroRefilterMax) + " campaigns"
	}
	for _, id := range req.CampaignIDs {
		if id <= 0 {
			return "campaign_ids must be positive Keitaro campaign ids"
		}
	}
	return ""
}

// runCampaignParams fills empty parameter names and tokens of imported campaigns.
// ctx bounds the tracker calls. req is a valid parameters_only request. st carries the
// copied credentials, the manifest store and the job report.
// One GET reads the campaign list and one GET reads traffic sources. Clicks, conversions
// and streams are not requested and not rewritten. A stored parameter key stays, even when
// its value is empty. With dry_run nothing is written. On failure st.fail is set.
// It does not return an error.
func (s *Server) runCampaignParams(ctx context.Context, req keitaroImportReq, st *importState) {
	prev, err := loadManifestStore(st.manifestStore)
	if err != nil {
		st.fail = "save error"
		return
	}
	if prev.Host != "" && !strings.EqualFold(prev.Host, st.creds.host()) {
		st.fail = errRefilterHost.Error()
		return
	}
	if req.DryRun {
		st.counts["dry_run"] = 1
	}
	s.ktJob.setStage("groups")
	client := s.keitaroClient(st.creds)
	camps, err := client.FetchCampaigns(ctx)
	if err != nil {
		st.fail = sanitize(err, st.creds.Key)
		return
	}
	sources, err := fetchTrafficSources(ctx, client)
	if err != nil {
		st.fail = sanitize(err, st.creds.Key)
		return
	}
	aliases := prev.LogCampaignAliases()
	ids := req.CampaignIDs
	if req.AllImported {
		ids = sortedCampaignIDs(aliases)
	}
	changed, err := s.applyCampaignParams(ctx, campaignParamPass{
		ids: ids, camps: camps, sources: sources, aliases: aliases, prev: prev, dryRun: req.DryRun,
	}, st)
	if err != nil {
		st.fail = "save error"
		return
	}
	st.counts["groups"] = changed
}

// fetchTrafficSources reads the tracker traffic sources.
// ctx bounds the call. client is the tracker client of this run.
// It returns the list. A 404 and an empty body are an empty list: the tracker has no sources.
// An error means the request failed or the body is not an array. The key is not part of the error.
func fetchTrafficSources(ctx context.Context, client *ktimport.Client) ([]ktimport.KSource, error) {
	raw, err := client.Get(ctx, "/admin_api/v1/traffic_sources")
	if err != nil {
		if strings.Contains(err.Error(), " 404") {
			return nil, nil
		}
		return nil, err
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var out []ktimport.KSource
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// campaignParamPass is one parameters_only pass over imported campaigns.
// ids are the Keitaro campaign ids. camps and sources are the tracker rows.
// aliases maps a campaign id to the imported group id. prev is the manifest.
// dryRun leaves groups.json untouched.
type campaignParamPass struct {
	ids      []int
	camps    []ktimport.Campaign
	sources  []ktimport.KSource
	aliases  map[int]string
	prev     ktimport.Manifest
	dryRun   bool
}

// applyCampaignParams writes parameter fields onto the imported groups.
// ctx bounds the store. pass is the tracker rows and the campaign ids of this pass. st collects losses.
// It returns how many groups would change, or did change. With pass.dryRun nothing is saved.
// An error means groups could not be read or saved.
func (s *Server) applyCampaignParams(ctx context.Context, pass campaignParamPass, st *importState) (int, error) {
	byCamp := make(map[int]ktimport.Campaign, len(pass.camps))
	for _, camp := range pass.camps {
		byCamp[int(camp.ID)] = camp
	}
	bySource := make(map[int]ktimport.KSource, len(pass.sources))
	for _, src := range pass.sources {
		if int(src.ID) > 0 {
			bySource[int(src.ID)] = src
		}
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return 0, err
	}
	offers, err := s.offerList(ctx)
	if err != nil {
		return 0, err
	}
	index := make(map[string]int, len(groups))
	for i, g := range groups {
		index[g.ID] = i
	}
	changed := 0
	for _, cid := range pass.ids {
		st.counts["campaigns"]++
		gid, ok := pass.aliases[cid]
		ref := "campaign " + strconv.Itoa(cid)
		if !ok || !pass.prev.Has(ktimport.KindGroups, gid) {
			st.losses = append(st.losses, ktimport.Loss{Kind: "missing", Ref: ref, Detail: "кампания не импортирована, параметры не тронуты"})
			st.counts["missing"]++
			continue
		}
		gi, ok := index[gid]
		if !ok {
			st.losses = append(st.losses, ktimport.Loss{Kind: "missing", Ref: ref, Detail: "группа " + gid + " удалена, параметры не тронуты"})
			st.counts["missing"]++
			continue
		}
		camp, found := byCamp[cid]
		if !found {
			st.losses = append(st.losses, ktimport.Loss{Kind: "missing", Ref: ref, Detail: "кампании нет в Keitaro, параметры не тронуты"})
			st.counts["missing"]++
			continue
		}
		fields, bad := ktimport.CampaignFieldsOf(camp, bySource[int(camp.TrafficSourceID)])
		if bad != "" {
			st.losses = append(st.losses, ktimport.Loss{Kind: "skipped", Ref: gid, Detail: bad})
		}
		for _, key := range fields.Skipped {
			st.losses = append(st.losses, ktimport.Loss{Kind: "skipped", Ref: gid, Detail: "параметр " + key + " длиннее 255 байт"})
		}
		next := withCampaignFields(groups[gi], fields)
		if sameCampaignParams(groups[gi], next) {
			continue
		}
		if msg := importedGroupIssue(&next, offers); msg != "" {
			st.losses = append(st.losses, ktimport.Loss{Kind: "skipped", Ref: gid, Detail: msg})
			continue
		}
		groups[gi] = next
		changed++
	}
	st.counts["changed"] = changed
	if pass.dryRun || changed == 0 {
		return 0, nil
	}
	if err := s.cfg.Groups.Save(ctx, groups); err != nil {
		return 0, err
	}
	return changed, nil
}

// withCampaignFields lays Keitaro parameter fields on a stored group.
// old is not modified. fields are the names and tokens read from the tracker.
// A key already stored on old stays, including an empty name. Streams and every other field stay.
// It returns the updated copy. It does not return an error.
func withCampaignFields(old config.Group, fields ktimport.CampaignFields) config.Group {
	next := old
	next.Parameters = fields.Parameters
	next.ParamTokens = fields.Tokens
	next.CostParam = fields.CostParam
	next.CostToken = fields.CostToken
	next.CurrencyParam = fields.CurrencyParam
	next.CurrencyToken = fields.CurrencyToken
	keepCampaignParams(old, &next)
	return next
}

// keepCampaignParams restores parameter fields the stored group already has.
// old is the stored group. next is updated in place and otherwise keeps the incoming fields.
// A present key in old wins, even when the value is empty. An empty price string on old
// takes the incoming value. It does not return a value or an error.
func keepCampaignParams(old config.Group, next *config.Group) {
	next.Parameters = mergeParamKeep(old.Parameters, next.Parameters)
	next.ParamTokens = mergeParamKeep(old.ParamTokens, next.ParamTokens)
	if strings.TrimSpace(old.CostParam) != "" {
		next.CostParam = old.CostParam
	}
	if strings.TrimSpace(old.CostToken) != "" {
		next.CostToken = old.CostToken
	}
	if strings.TrimSpace(old.CurrencyParam) != "" {
		next.CurrencyParam = old.CurrencyParam
	}
	if strings.TrimSpace(old.CurrencyToken) != "" {
		next.CurrencyToken = old.CurrencyToken
	}
}

// mergeParamKeep fills keys that old does not have yet.
// old is the stored map. fresh is the incoming map. A key present in old stays, empty value included.
// It returns the merged map, or nil when both sides are empty. Neither argument is modified.
// It does not return an error.
func mergeParamKeep(old, fresh map[string]string) map[string]string {
	if len(old) == 0 {
		if len(fresh) == 0 {
			return nil
		}
		return fresh
	}
	out := make(map[string]string, len(old)+len(fresh))
	for key, val := range fresh {
		out[key] = val
	}
	for key, val := range old {
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sameCampaignParams reports whether the parameter fields of a and b are equal.
// a and b are two copies of one group. Nil and empty maps are equal.
// It does not look at streams. It does not return an error.
func sameCampaignParams(a, b config.Group) bool {
	return sameStringMap(a.Parameters, b.Parameters) &&
		sameStringMap(a.ParamTokens, b.ParamTokens) &&
		a.CostParam == b.CostParam && a.CostToken == b.CostToken &&
		a.CurrencyParam == b.CurrencyParam && a.CurrencyToken == b.CurrencyToken
}

// sameStringMap reports whether a and b hold the same pairs.
// Nil and empty are equal. It does not return an error.
func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, val := range a {
		if b[key] != val {
			return false
		}
	}
	return true
}
