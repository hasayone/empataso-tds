package admin

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

// keitaroRefilterMax is the most campaigns one filters_only request may list in campaign_ids.
const keitaroRefilterMax = 20000

// errRefilterHost stops a filters_only run when the manifest belongs to another tracker.
var errRefilterHost = errors.New("imported campaigns come from another tracker")

// forFilters checks a filters_only request.
// filters_only reads only the streams of the listed campaigns, or of every imported campaign
// with all_imported, and rewrites only stream filters. It combines with dry_run and nothing else.
// It returns a client message, or an empty string when the request is valid.
// campaign_ids, all_imported and dry_run without filters_only are refused.
func (req *keitaroImportReq) forFilters() string {
	if !req.FiltersOnly {
		if len(req.CampaignIDs) > 0 || req.AllImported || req.DryRun {
			return "campaign_ids, all_imported and dry_run need filters_only"
		}
		return ""
	}
	if req.catalog() || req.Clicks || req.Conversions || req.CampaignID != 0 {
		return "filters_only does not combine with other import flags or campaign_id"
	}
	if req.AllImported == (len(req.CampaignIDs) > 0) {
		return "filters_only needs either campaign_ids or all_imported"
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

// refilterRun is one filters_only pass over the imported groups.
// aliases maps a Keitaro campaign id to its imported group id. prev is the manifest of the import.
// lists collects the IP list files the new rules name. counts and losses are the job report.
type refilterRun struct {
	aliases map[int]string
	prev    ktimport.Manifest
	lists   map[string]string
	counts  map[string]int
	losses  []ktimport.Loss
}

// runRefilter rewrites the stream filters of imported campaigns from the tracker.
// ctx bounds the tracker calls. req is a valid filters_only request. st carries the copied
// credentials, the manifest store and the job report.
// With dry_run nothing is written: the report lists every stream that would change.
// Otherwise IP list files, the manifest and groups.json are saved. On failure st.fail is set.
// It does not return an error.
func (s *Server) runRefilter(ctx context.Context, req keitaroImportReq, st *importState) {
	prev, err := loadManifestStore(st.manifestStore)
	if err != nil {
		st.fail = "save error"
		return
	}
	if prev.Host != "" && !strings.EqualFold(prev.Host, st.creds.host()) {
		st.fail = errRefilterHost.Error()
		return
	}
	run := &refilterRun{aliases: prev.LogCampaignAliases(), prev: prev, counts: st.counts}
	ids := req.CampaignIDs
	if req.AllImported {
		ids = sortedCampaignIDs(run.aliases)
	}
	if req.DryRun {
		run.counts["dry_run"] = 1
	}
	streams, err := s.keitaroClient(st.creds).FetchStreams(ctx, ids)
	if err != nil {
		st.fail = sanitize(err, st.creds.Key)
		return
	}
	s.ktJob.setStage("groups")
	err = s.refilterGroups(ctx, ids, streams, run, req.DryRun, st)
	st.losses = append(st.losses, run.losses...)
	if err != nil {
		st.fail = "save error"
	}
}

// sortedCampaignIDs returns the campaign ids of the manifest map in ascending order.
func sortedCampaignIDs(aliases map[int]string) []int {
	ids := make([]int, 0, len(aliases))
	for id := range aliases {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// refilterGroups applies the tracker filters to the imported groups under the groups lock.
// ids are the campaigns in request order, streams their tracker streams by campaign id.
// run collects the report. dryRun leaves every file untouched. st gives the manifest store and host.
// A group that fails the save checks keeps its stored streams and gets a skipped line.
// It returns an error when groups, offers, lists or the manifest cannot be read or saved.
func (s *Server) refilterGroups(ctx context.Context, ids []int, streams map[string][]ktimport.KStream, run *refilterRun, dryRun bool, st *importState) error {
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return err
	}
	offers, err := s.offerList(ctx)
	if err != nil {
		return err
	}
	index := make(map[string]int, len(groups))
	for i, g := range groups {
		index[g.ID] = i
	}
	changed := 0
	for _, cid := range ids {
		run.counts["campaigns"]++
		gi, ok := run.groupOf(cid, index)
		if !ok {
			continue
		}
		next, n := run.refilterGroup(groups[gi], streams[strconv.Itoa(cid)])
		if n == 0 {
			continue
		}
		if msg := subIDFault(next); msg != "" {
			run.skip(next.ID, msg)
			continue
		}
		if msg := validateStreams([]config.Group{next}, offers); msg != "" {
			run.skip(next.ID, msg)
			continue
		}
		groups[gi] = next
		changed++
		run.counts["changed"] += n
	}
	run.counts["groups"] = changed
	if dryRun || changed == 0 {
		return nil
	}
	if err := s.saveRefilterLists(run, st); err != nil {
		return err
	}
	return s.cfg.Groups.Save(ctx, groups)
}

// saveRefilterLists writes the IP list files of the new rules and records them in the manifest.
// run holds the files. st gives the manifest store and the tracker host.
// It returns an error when the lists store is missing while files are needed, or a write fails.
func (s *Server) saveRefilterLists(run *refilterRun, st *importState) error {
	if len(run.lists) == 0 {
		return nil
	}
	if s.cfg.Lists == nil {
		return errors.New("lists store unavailable")
	}
	var res ktimport.Result
	for _, name := range sortedListNames(run.lists) {
		if err := s.cfg.Lists.Write(name, run.lists[name]); err != nil {
			return err
		}
		res.Manifest.Add(ktimport.KindLists, name)
	}
	run.counts["lists"] = len(run.lists)
	return s.saveRunManifestStore(st.manifestStore, run.prev, res, st.creds.host())
}

// sortedListNames returns the file names of lists in ascending order.
func sortedListNames(lists map[string]string) []string {
	names := make([]string, 0, len(lists))
	for name := range lists {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// groupOf finds the imported group of a Keitaro campaign.
// cid is the campaign id; index maps group ids to positions in groups.json.
// It returns the position and true, or records a missing line and returns false when the
// campaign was not imported or its group is gone.
func (run *refilterRun) groupOf(cid int, index map[string]int) (int, bool) {
	ref := "campaign " + strconv.Itoa(cid)
	gid, ok := run.aliases[cid]
	if !ok || !run.prev.Has(ktimport.KindGroups, gid) {
		run.missing(ref, "кампания не импортирована, фильтры не тронуты")
		return 0, false
	}
	gi, ok := index[gid]
	if !ok {
		run.missing(ref, "группа "+gid+" удалена, фильтры не тронуты")
		return 0, false
	}
	return gi, true
}

// refilterGroup puts the tracker filters on the imported streams of one group.
// g is the stored group and is not modified. kstreams are the campaign streams from the tracker.
// Streams are matched by keitaro_stream_id in the note. Only rule keys the import fills, the
// AND/OR switch, the note of skipped filters and, when nothing maps, the status change.
// It returns the updated copy and the number of streams that changed.
func (run *refilterRun) refilterGroup(g config.Group, kstreams []ktimport.KStream) (config.Group, int) {
	mapped, lists, losses := ktimport.MapStreamFilters(kstreams)
	run.counts["streams"] += len(mapped)
	run.losses = append(run.losses, losses...)
	for name, text := range lists {
		if run.lists == nil {
			run.lists = map[string]string{}
		}
		run.lists[name] = text
	}
	next := g
	next.Streams = append([]config.Stream(nil), g.Streams...)
	seen := map[int]bool{}
	n := 0
	for i, stream := range next.Streams {
		id, ok := ktimport.NoteStreamID(stream.Comment)
		if !ok {
			continue
		}
		f, ok := mapped[id]
		if !ok {
			run.missing("stream "+strconv.Itoa(id), "поток есть в группе "+g.ID+", но не в Keitaro, фильтры не тронуты")
			continue
		}
		seen[id] = true
		out, diff := refilterStream(stream, f)
		if diff == "" {
			continue
		}
		if stream.Status && !out.Status {
			run.counts["disabled"]++
		}
		next.Streams[i] = out
		n++
		run.losses = append(run.losses, ktimport.Loss{Kind: "changed", Ref: "stream " + strconv.Itoa(id), Detail: "группа " + g.ID + ": " + diff})
	}
	for _, ks := range kstreams {
		if id := int(ks.ID); id > 0 && !seen[id] {
			run.missing("stream "+strconv.Itoa(id), "потока Keitaro нет в группе "+g.ID+", нужен досинк кампании")
		}
	}
	return next, n
}

// missing records a stream or campaign the pass could not match.
func (run *refilterRun) missing(ref, detail string) {
	run.counts["missing"]++
	run.losses = append(run.losses, ktimport.Loss{Kind: "skipped", Ref: ref, Detail: detail})
}

// skip records a group whose new filters fail the save checks; its streams stay as stored.
func (run *refilterRun) skip(gid, msg string) {
	run.counts["failed"]++
	run.losses = append(run.losses, ktimport.Loss{Kind: "skipped", Ref: gid, Detail: "фильтры не применены: " + msg})
}

// refilterStream puts one tracker stream's filters on a stored stream.
// old is the stored stream. f is the result of the filter mapping for the same Keitaro stream.
// Rule keys the import does not fill (limit, domain, operators, Yandex Browser) keep their stored
// values. The stream is only ever switched off, when no filter maps; it is never switched on.
// It returns the updated stream and a readable list of changes, or the old stream and an empty
// string when nothing changes.
func refilterStream(old config.Stream, f ktimport.StreamFilters) (config.Stream, string) {
	rules, err := mergeImportedRules(old.Rules, f.Rules)
	if err != nil {
		return old, ""
	}
	out := old
	out.Rules = rules
	out.FilterOr = f.FilterOr
	out.Comment = ktimport.RenoteStream(old.Comment, f.Skipped)
	var parts []string
	added, removed, edited := diffRuleKeys(old.Rules, rules)
	if len(added) > 0 {
		parts = append(parts, "добавлено: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "убрано: "+strings.Join(removed, ", "))
	}
	if len(edited) > 0 {
		parts = append(parts, "изменено: "+strings.Join(edited, ", "))
	}
	if old.FilterOr != out.FilterOr {
		parts = append(parts, "отношение "+map[bool]string{true: "ИЛИ", false: "И"}[out.FilterOr])
	}
	if old.Comment != out.Comment {
		parts = append(parts, "заметка")
	}
	if !f.OK && old.Status {
		out.Status = false
		parts = append(parts, "поток выключен: "+f.Why)
	}
	if len(parts) == 0 {
		return old, ""
	}
	return out, strings.Join(parts, "; ")
}

// mergeImportedRules replaces the import-owned keys of stored rules with the mapped ones.
// stored are the rules in groups.json. mapped are the rules from the Keitaro filters.
// It returns the merged rules, or an error when either set cannot pass through JSON.
func mergeImportedRules(stored config.Rules, mapped ktimport.Rules) (config.Rules, error) {
	base, err := ruleFields(stored)
	if err != nil {
		return config.Rules{}, err
	}
	add, err := ruleFields(mapped)
	if err != nil {
		return config.Rules{}, err
	}
	for _, key := range ktimport.RuleKeys() {
		delete(base, key)
	}
	for key, raw := range add {
		base[key] = raw
	}
	return convert[config.Rules](base)
}

// ruleFields encodes rules as a JSON object keyed by rule name.
func ruleFields(rules any) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// diffRuleKeys compares the import-owned rule keys that filter anything.
// before and after are the stored and the new rules.
// It returns keys that start filtering, stop filtering and filter differently, each in key order.
func diffRuleKeys(before, after config.Rules) (added, removed, edited []string) {
	a, errA := ruleFields(before)
	b, errB := ruleFields(after)
	if errA != nil || errB != nil {
		return nil, nil, nil
	}
	for _, key := range ktimport.RuleKeys() {
		was, now := activeRule(a[key]), activeRule(b[key])
		switch {
		case was == now:
		case was == "":
			added = append(added, key)
		case now == "":
			removed = append(removed, key)
		default:
			edited = append(edited, key)
		}
	}
	return added, removed, edited
}

// activeRule returns the canonical JSON of a rule value that filters, or "" when it does not.
// A zero number, a flag of 0, a disabled schedule and an empty object or map do not filter.
// For sub_ids only the active entries count.
func activeRule(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return ""
	}
	switch t := v.(type) {
	case float64:
		if t == 0 {
			return ""
		}
	case map[string]any:
		if flag, ok := t["flag"]; ok {
			if n, _ := flag.(float64); n == 0 {
				return ""
			}
			break
		}
		if enabled, ok := t["enabled"]; ok {
			if on, _ := enabled.(bool); !on {
				return ""
			}
			break
		}
		for key, entry := range t {
			if activeRule(rawJSON(entry)) == "" {
				delete(t, key)
			}
		}
		if len(t) == 0 {
			return ""
		}
	case nil:
		return ""
	}
	out, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(out)
}

// rawJSON encodes a decoded JSON value back to bytes; it cannot fail for values from json.Unmarshal.
func rawJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}
