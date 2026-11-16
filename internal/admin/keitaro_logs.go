package admin

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/logbuf"
	"github.com/egerkuzma/emtds/internal/store"
	"github.com/emtds/bridge/ktimport"
)

// logSkipTally counts log rows skipped for empty sub_id or zero time.
type logSkipTally struct {
	clicksEmpty int
	clicksZero  int
	convZero    int
}

// total returns the sum of all skip counters, or 0 when t is nil.
func (t *logSkipTally) total() int {
	if t == nil {
		return 0
	}
	return t.clicksEmpty + t.clicksZero + t.convZero
}

// noteLogSkipLosses appends ktimport.Loss rows for non-zero skip tallies on manual import.
func noteLogSkipLosses(losses *[]ktimport.Loss, t *logSkipTally) {
	if t == nil {
		return
	}
	if t.clicksEmpty > 0 {
		*losses = append(*losses, ktimport.Loss{Kind: "skipped", Ref: "clicks", Detail: fmt.Sprintf("%d rows, empty sub_id", t.clicksEmpty)})
	}
	if t.clicksZero > 0 {
		*losses = append(*losses, ktimport.Loss{Kind: "skipped", Ref: "clicks", Detail: fmt.Sprintf("%d rows, zero time", t.clicksZero)})
	}
	if t.convZero > 0 {
		*losses = append(*losses, ktimport.Loss{Kind: "skipped", Ref: "conversions", Detail: fmt.Sprintf("%d rows, zero time", t.convZero)})
	}
}

const (
	keitaroBatch           = 1000
	keitaroTimeLayout      = "2006-01-02 15:04:05"
	keitaroDayLayout       = "2006-01-02"
	maxKeitaroClickFillIdx = 2_000_000
)

// keitaroClickFillIndex maps sub_id to click metadata built during a log import.
type keitaroClickFillIndex map[string]store.ClickFill

// put stores fill under subID when the index is below the size limit.
// An existing entry is replaced only when fill is strictly newer.
func (idx keitaroClickFillIndex) put(subID string, fill store.ClickFill) {
	if subID == "" {
		return
	}
	if prev, ok := idx[subID]; ok {
		if !fill.TS.After(prev.TS) {
			return
		}
		idx[subID] = fill
		return
	}
	if len(idx) >= maxKeitaroClickFillIdx {
		return
	}
	idx[subID] = fill
}

// keitaroRange turns inclusive calendar dates into tracker range strings.
// from and to are YYYY-MM-DD. The result is UTC day start and 23:59:59.
// It returns an error when either value is not that date layout.
func keitaroRange(from, to string) (string, string, error) {
	f, err := time.Parse(keitaroDayLayout, strings.TrimSpace(from))
	if err != nil {
		return "", "", err
	}
	t, err := time.Parse(keitaroDayLayout, strings.TrimSpace(to))
	if err != nil {
		return "", "", err
	}
	return f.Format(keitaroDayLayout) + " 00:00:00", t.Format(keitaroDayLayout) + " 23:59:59", nil
}

// parseKeitaroTime reads a tracker timestamp.
// raw is "2006-01-02 15:04:05" in UTC. It returns the zero time when raw is
// empty or not that layout. It does not return an error.
func parseKeitaroTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	t, err := time.ParseInLocation(keitaroTimeLayout, raw, time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

// logImport is the window and campaign map for one log import.
// from and to are tracker range strings. host is the tracker hostname.
// creds is the origin and key of the run that started this import.
// aliases maps a tracker campaign id to the emtds group id for this run.
// groups is the groups-store snapshot
// used to build the name index once for this import.
type logImport struct {
	from    string
	to      string
	host    string
	creds   ktCreds
	aliases map[int]string
	groups  []config.Group
}

// logPlace is the group identity available while mapping one log import.
// aliases maps a tracker campaign id to the emtds group id. names maps a
// group id to the group name from the groups store. unknown collects tracker
// campaign ids that were missing from aliases when rows were mapped. refs
// collects the catalog ids of mapped rows for the catalog top-up; nil skips it.
type logPlace struct {
	aliases map[int]string
	names   map[string]string
	unknown *unknownCampaignTally
	refs    catalogRefs
}

// unknownCampaignTally counts log rows tied to campaigns absent from the tracker list.
type unknownCampaignTally struct {
	ids  map[int]struct{}
	rows int
}

// note registers one row mapped with a numeric fallback for campaignID.
func (u *unknownCampaignTally) note(campaignID int) {
	if u.ids == nil {
		u.ids = map[int]struct{}{}
	}
	u.ids[campaignID] = struct{}{}
	u.rows++
}

// logSpan is the tracker window and mapping context for one log fetch.
// from and to are the inclusive tracker range strings passed to FetchClicks
// or FetchConversions. host is stored as the row domain. place supplies the
// group id and name for every mapped row.
type logSpan struct {
	from  string
	to    string
	host  string
	place logPlace
}

// importLogs writes conversions and clicks, or records that the warehouse is absent.
// li carries the tracker range, the host, the campaign aliases and the groups snapshot.
// The name index is built once from that snapshot and shared by both logs.
// counts and losses are updated. After both logs are written, catalog records the rows
// refer to but the catalog lacks are added by topUpKeitaroCatalog; its lines join losses.
// It returns a tracker or warehouse error.
func (s *Server) importLogs(ctx context.Context, req keitaroImportReq, li logImport, counts map[string]int, losses *[]ktimport.Loss, opts logImportOpts) (logFetchStats, error) {
	stats, refs, err := s.importLogRows(ctx, req, li, counts, losses, opts)
	if err != nil {
		return stats, err
	}
	top := s.topUpKeitaroCatalog(ctx, li.creds, opts.manifestStore, refs)
	*losses = append(*losses, top.losses...)
	if n := top.total(); n > 0 {
		counts["catalog_added"] = n
	}
	return stats, nil
}

// importLogRows writes conversions and clicks for importLogs.
// It returns the fetch stats, the catalog ids of mapped rows, and a tracker or warehouse error.
func (s *Server) importLogRows(ctx context.Context, req keitaroImportReq, li logImport, counts map[string]int, losses *[]ktimport.Loss, opts logImportOpts) (logFetchStats, catalogRefs, error) {
	var stats logFetchStats
	tally := &unknownCampaignTally{}
	skip := opts.skip
	if skip == nil {
		skip = &logSkipTally{}
	}
	refs := newCatalogRefs()
	place := logPlace{aliases: li.aliases, names: groupNames(li.groups), unknown: tally, refs: refs}
	defer noteUnknownCampaignLoss(losses, tally)
	defer noteLogSkipLosses(losses, skip)
	client := s.keitaroClient(li.creds)
	var clickIdx keitaroClickFillIndex
	if req.Clicks {
		if s.cfg.Warehouse == nil || li.host == "" {
			counts["clicks"] = 0
			*losses = append(*losses, ktimport.Loss{Kind: "skipped", Ref: "clicks", Detail: "clickhouse unavailable"})
		} else {
			span := logSpan{from: li.from, to: li.to, host: li.host, place: place}
			read, inserted, dups, maxTS, idx, err := s.importClicks(ctx, client, span, opts.syncMode, skip)
			if err != nil {
				return stats, refs, err
			}
			if opts.syncMode {
				counts["clicks"] = inserted
			} else {
				counts["clicks"] = read
				if dups > 0 {
					counts["clicks_duplicates"] = dups
				}
			}
			stats.maxClick = maxTS
			if !opts.syncMode {
				clickIdx = idx
			}
			noteExtendedColumns(losses, client, "clicks")
		}
	}
	if req.Conversions {
		if s.cfg.Warehouse == nil || li.host == "" {
			counts["conversions"] = 0
			*losses = append(*losses, ktimport.Loss{Kind: "skipped", Ref: "conversions", Detail: "clickhouse unavailable"})
		} else {
			span := logSpan{from: li.from, to: li.to, host: li.host, place: place}
			n, maxTS, odd, err := s.importConversions(ctx, client, span, clickIdx, opts.syncMode, skip)
			if err != nil {
				return stats, refs, err
			}
			counts["conversions"] = n
			stats.maxConv = maxTS
			noteExtendedColumns(losses, client, "conversions")
			noteOddConversionStatuses(losses, odd)
		}
	}
	return stats, refs, nil
}

const maxLogCampaignIDsInLoss = 20

// noteUnknownCampaignLoss records rows that kept a numeric tracker campaign id.
// losses receives one aggregated line when tally is non-empty.
// At most maxLogCampaignIDsInLoss ids appear in the detail text.
func noteUnknownCampaignLoss(losses *[]ktimport.Loss, tally *unknownCampaignTally) {
	if tally == nil || tally.rows == 0 || len(tally.ids) == 0 {
		return
	}
	ids := make([]int, 0, len(tally.ids))
	for id := range tally.ids {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	*losses = append(*losses, ktimport.Loss{
		Kind:   "approximated",
		Ref:    "log_campaign",
		Detail: fmt.Sprintf("%d rows, unknown campaign_id: %s", tally.rows, formatLogCampaignIDs(ids)),
	})
}

// formatLogCampaignIDs formats tracker campaign ids for a loss line.
// ids are sorted. When there are more than maxLogCampaignIDsInLoss entries,
// the tail is replaced with «и ещё N».
func formatLogCampaignIDs(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	limit := maxLogCampaignIDsInLoss
	if len(ids) <= limit {
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		return strings.Join(parts, ",")
	}
	parts := make([]string, limit)
	for i := 0; i < limit; i++ {
		parts[i] = strconv.Itoa(ids[i])
	}
	return strings.Join(parts, ",") + fmt.Sprintf(" и ещё %d", len(ids)-limit)
}

// noteExtendedColumns records that the tracker rejected the wide log columns.
// losses receives the line. client is the reader just used. ref is conversions or clicks.
// A client that accepted the wide set adds nothing. It does not return an error.
func noteExtendedColumns(losses *[]ktimport.Loss, client *ktimport.Client, ref string) {
	if client == nil || !client.ExtendedColumnsRejected() {
		return
	}
	*losses = append(*losses, ktimport.Loss{Kind: "approximated", Ref: ref, Detail: "расширенные колонки недоступны"})
}

// noteOddConversionStatuses records conversions whose status is outside the known set.
// losses receives one line when n is positive. n is the count from importConversions.
// The detail names the allowed statuses and the count. A zero count adds nothing.
// It does not return an error.
func noteOddConversionStatuses(losses *[]ktimport.Loss, n int) {
	if losses == nil || n <= 0 {
		return
	}
	*losses = append(*losses, ktimport.Loss{
		Kind:   "approximated",
		Ref:    "conversions",
		Detail: fmt.Sprintf("статус конверсии не из списка lead, sale, rejected, registration, deposit, rebill: %d", n),
	})
}

// oddConversionStatus reports whether a conversion status is outside the imported set.
// status is the raw tracker status. It is trimmed and lowercased. The argument is not changed.
// It returns false for an empty status and for lead, sale, rejected, registration, deposit and rebill.
// It returns true for any other non-empty status. It does not return an error.
func oddConversionStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "lead", "sale", "rejected", "registration", "deposit", "rebill":
		return false
	default:
		return true
	}
}

// listKeitaroGroups reads the groups used to name one log import.
// It returns the current list. A nil store returns a nil slice and no error.
// An error comes from the groups store.
func (s *Server) listKeitaroGroups(ctx context.Context) ([]config.Group, error) {
	if s.cfg.Groups == nil {
		return nil, nil
	}
	return s.cfg.Groups.List(ctx)
}

// importConversions reads the conversion log and writes batches of 1000.
// client is the tracker reader, so a column fallback stays visible to the caller.
// span carries the tracker range, domain host and group mapping for each row.
// clickIdx maps sub_id to click metadata from an earlier importClicks in the same run;
// it may be nil when conversions import alone.
// It returns how many rows were read, the latest timestamp, and how many rows had a
// non-empty status outside lead, sale, rejected, registration, deposit and rebill.
// An error comes from the tracker or the warehouse. The row status is not rewritten.
func (s *Server) importConversions(ctx context.Context, client *ktimport.Client, span logSpan, clickIdx keitaroClickFillIndex, syncMode bool, skip *logSkipTally) (int, time.Time, int, error) {
	var batch []store.ConversionInsert
	var maxTS time.Time
	var odd int
	n, err := client.FetchConversions(ctx, span.from, span.to, func(row ktimport.ConversionRow) error {
		if oddConversionStatus(row.Status) {
			odd++
		}
		var snap *store.ClickFill
		if clickIdx != nil {
			if fill, ok := clickIdx[row.SubID]; ok {
				cp := fill
				snap = &cp
			}
		}
		ins := conversionInsert(row, span.host, span.place, snap)
		span.place.refs.note(ins.OfferID, ins.LandingID, ins.NetworkID, ins.SourceID)
		if !ins.TS.IsZero() && ins.TS.After(maxTS) {
			maxTS = ins.TS
		}
		if syncMode && ins.TS.IsZero() {
			if skip != nil {
				skip.convZero++
			}
			return nil
		}
		batch = append(batch, ins)
		if len(batch) < keitaroBatch {
			return nil
		}
		cp := append([]store.ConversionInsert(nil), batch...)
		batch = batch[:0]
		return s.cfg.Warehouse.InsertConversions(ctx, cp)
	})
	if err != nil {
		return n, maxTS, odd, err
	}
	if len(batch) == 0 {
		return n, maxTS, odd, nil
	}
	cp := append([]store.ConversionInsert(nil), batch...)
	if err := s.cfg.Warehouse.InsertConversions(ctx, cp); err != nil {
		return n, maxTS, odd, err
	}
	return n, maxTS, odd, nil
}

// importClicks reads the click log and writes batches of 1000.
// It returns rows read, rows inserted, duplicate skips, the latest timestamp,
// the sub_id index for conversion backfill, and an error from the tracker or warehouse.
func (s *Server) importClicks(ctx context.Context, client *ktimport.Client, span logSpan, syncMode bool, skip *logSkipTally) (read, inserted, duplicates int, maxTS time.Time, idx keitaroClickFillIndex, err error) {
	idx = make(keitaroClickFillIndex)
	overflowSeen := map[string]struct{}{}
	var batch []logbuf.Event
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		from, to := eventBatchWindow(batch)
		cids := eventBatchCIDs(batch)
		existing, err := s.cfg.Warehouse.ExistingCIDs(ctx, span.host, from, to, cids)
		if err != nil {
			return err
		}
		out := make([]logbuf.Event, 0, len(batch))
		for _, ev := range batch {
			if ev.CID == "" {
				continue
			}
			if _, ok := existing[ev.CID]; ok {
				duplicates++
				continue
			}
			out = append(out, ev)
		}
		batch = batch[:0]
		if len(out) == 0 {
			return nil
		}
		inserted += len(out)
		return s.cfg.Warehouse.InsertEvents(ctx, out)
	}
	n, err := client.FetchClicks(ctx, span.from, span.to, func(row ktimport.ClickRow) error {
		if strings.TrimSpace(row.SubID) == "" {
			if skip != nil {
				skip.clicksEmpty++
			}
			return nil
		}
		ev := clickEvent(row, span.host, span.place)
		span.place.refs.note(ev.OfferID, ev.LandingID, ev.NetworkID, ev.SourceID)
		if ev.Ts.IsZero() {
			if skip != nil {
				skip.clicksZero++
			}
			return nil
		}
		if ev.Ts.After(maxTS) {
			maxTS = ev.Ts
		}
		if !syncMode {
			if _, ok := idx[row.SubID]; ok {
				duplicates++
				return nil
			}
			if _, ok := overflowSeen[row.SubID]; ok {
				duplicates++
				return nil
			}
			before := len(idx)
			idx.put(row.SubID, clickFillFromEvent(ev))
			if len(idx) == before && before >= maxKeitaroClickFillIdx {
				overflowSeen[row.SubID] = struct{}{}
			}
		} else if _, ok := overflowSeen[row.SubID]; ok {
			duplicates++
			return nil
		} else {
			overflowSeen[row.SubID] = struct{}{}
		}
		batch = append(batch, ev)
		if len(batch) < keitaroBatch {
			return nil
		}
		return flush()
	})
	if err != nil {
		return n, inserted, duplicates, maxTS, idx, err
	}
	if err := flush(); err != nil {
		return n, inserted, duplicates, maxTS, idx, err
	}
	return n, inserted, duplicates, maxTS, idx, nil
}

// eventBatchWindow returns the earliest and latest click times in batch.
func eventBatchWindow(batch []logbuf.Event) (from, to time.Time) {
	for i, ev := range batch {
		if i == 0 || ev.Ts.Before(from) {
			from = ev.Ts
		}
		if i == 0 || ev.Ts.After(to) {
			to = ev.Ts
		}
	}
	return from, to
}

// eventBatchCIDs collects non-empty click ids from batch.
func eventBatchCIDs(batch []logbuf.Event) []string {
	out := make([]string, 0, len(batch))
	for _, ev := range batch {
		if ev.CID != "" {
			out = append(out, ev.CID)
		}
	}
	return out
}

// clickFillFromEvent maps one imported click event onto a ClickFill snapshot.
// ev is the row about to be written to events. It does not return an error.
func clickFillFromEvent(ev logbuf.Event) store.ClickFill {
	return store.ClickFill{
		TS:         ev.Ts,
		GroupID:    ev.GroupID,
		GroupName:  ev.GroupName,
		IP:         ev.IP,
		UserAgent:  ev.UserAgent,
		OS:         ev.OS,
		OSVersion:  ev.OSVersion,
		Browser:    ev.Browser,
		BrowserVer: ev.BrowserV,
		Brand:      ev.Brand,
		Referer:    ev.Referer,
		Lang:       ev.Lang,
		City:       ev.City,
		Region:     ev.Region,
		LandingID:  ev.LandingID,
		OfferID:    ev.OfferID,
		NetworkID:  ev.NetworkID,
		SourceID:   ev.SourceID,
	}
}

// conversionInsert maps one tracker conversion onto a warehouse row.
// row is the log line. host is the tracker hostname stored as the domain.
// place supplies the group id and the group name.
// GroupID is the id keitaroGroup returns first. ClickTS is the click time.
// OriginalStatus is the raw original status, or the raw status when that is blank.
// PrevStatus is the previous status, lowercased and trimmed.
// Site and XRW stay empty: the tracker log is not their source.
// Country is the ISO code. Device is the engine device word.
// Profit is the revenue. Offer, landing, network and source ids are the decimal
// tracker ids, or empty when the tracker id is not positive.
// A zero time means both datetimes were empty or unreadable.
// It does not return an error.
func conversionInsert(row ktimport.ConversionRow, host string, place logPlace, clickSnap *store.ClickFill) store.ConversionInsert {
	ts := parseKeitaroTime(row.PostbackDatetime)
	if ts.IsZero() {
		ts = parseKeitaroTime(row.ClickDatetime)
	}
	groupID, groupName := keitaroGroup(row.Campaign, int(row.CampaignID), place)
	original := row.OriginalStatus
	if strings.TrimSpace(original) == "" {
		original = row.Status
	}
	out := store.ConversionInsert{
		TS:              ts,
		Domain:          host,
		Device:          keitaroDevice(row.DeviceType),
		Operator:        row.Operator,
		Country:         row.CountryCode,
		City:            row.City,
		Profit:          float64(row.Revenue),
		GroupName:       groupName,
		StreamName:      row.Stream,
		CID:             row.SubID,
		Status:          strings.ToLower(strings.TrimSpace(row.Status)),
		OfferID:         catalogNum(int(row.OfferID)),
		LandingID:       catalogNum(int(row.LandingID)),
		SourceID:        catalogNum(int(row.SourceID)),
		NetworkID:       catalogNum(int(row.NetworkID)),
		Subs:            row.Subs,
		GroupID:         groupID,
		OriginGroupID:   groupID,
		OriginGroupName: groupName,
		ClickTS:         parseKeitaroTime(row.ClickDatetime),
		OriginalStatus:  original,
		PrevStatus:      strings.ToLower(strings.TrimSpace(row.PreviousStatus)),
		TID:             string(row.TID),
		IP:              row.IP,
		Region:          row.Region,
		Lang:            row.Language,
		OS:              row.OS,
		OSVersion:       row.OSVersion,
		Browser:         row.Browser,
		BrowserV:        row.BrowserVersion,
		DeviceModel:     row.DeviceModel,
		UserAgent:       row.UserAgent,
		Referer:         row.Referrer,
		SE:              row.SearchEngine,
		Keyword:         row.Keyword,
		Org:             row.ISP,
		AdCampaignID:    string(row.AdCampaignID),
		CreativeID:      string(row.CreativeID),
		ExternalID:      string(row.ExternalID),
	}
	store.FillConversionInsert(&out, clickSnap)
	return out
}

// clickEvent maps one tracker click onto a log event.
// row is the log line. host is the tracker hostname. place supplies the
// group id and the group name for this campaign.
// Country is the ISO code. SE is the search engine. Device is the engine word.
// Offer, landing, network and source ids are the decimal tracker ids when positive.
// AdCampaignID, CreativeID and ExternalID are copied as text. Site and XRW stay empty.
// It does not return an error.
func clickEvent(row ktimport.ClickRow, host string, place logPlace) logbuf.Event {
	var uniq uint8
	if row.IsUnique {
		uniq = 1
	}
	bot := ""
	if row.IsBot {
		bot = "bot"
	}
	groupID, groupName := keitaroGroup(row.Campaign, int(row.CampaignID), place)
	out, redirect := keitaroDest(row)
	return logbuf.Event{
		Ts:      parseKeitaroTime(row.Datetime),
		GroupID: groupID, GroupName: groupName,
		OriginID: groupID, OriginName: groupName,
		Stream:       row.Stream,
		Out:          out,
		Redirect:     redirect,
		Keyword:      row.Keyword,
		Device:       keitaroDevice(row.DeviceType),
		Operator:     row.Operator,
		Country:      row.CountryCode,
		City:         row.City,
		Region:       row.Region,
		Lang:         row.Language,
		OS:           row.OS,
		OSVersion:    row.OSVersion,
		Browser:      row.Browser,
		BrowserV:     row.BrowserVersion,
		DeviceModel:  row.DeviceModel,
		Uniq:         uniq,
		Bot:          bot,
		IP:           row.IP,
		Referer:      row.Referrer,
		UserAgent:    row.UserAgent,
		Domain:       host,
		SE:           row.SearchEngine,
		CID:          row.SubID,
		OfferID:      catalogNum(int(row.OfferID)),
		LandingID:    catalogNum(int(row.LandingID)),
		SourceID:     catalogNum(int(row.SourceID)),
		NetworkID:    catalogNum(int(row.NetworkID)),
		CostCents:    int64(math.Round(float64(row.Cost) * 100)),
		Subs:         row.Subs,
		AdCampaignID: string(row.AdCampaignID),
		CreativeID:   string(row.CreativeID),
		ExternalID:   string(row.ExternalID),
	}
}

// keitaroDest maps the tracker destination of a click onto the events out and redirect.
// row is the log line. A click with a landing and no address in destination
// (the tracker writes a marker such as LP there) becomes redirect landing with
// the landing id in out, as the engine logs its own landings. Anything else is
// the destination as given, capped by logbuf.CapOut, with an empty redirect.
// It returns out and redirect. It does not return an error.
func keitaroDest(row ktimport.ClickRow) (out, redirect string) {
	dest := strings.TrimSpace(row.Destination)
	if landing := catalogNum(int(row.LandingID)); landing != "" && !strings.Contains(dest, "://") {
		return landing, "landing"
	}
	return logbuf.CapOut(dest), ""
}

// groupNames indexes group names by id.
// groups is one groups-store snapshot. The first id wins.
// It returns the index. It does not return an error.
func groupNames(groups []config.Group) map[string]string {
	out := make(map[string]string, len(groups))
	for _, g := range groups {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			continue
		}
		if _, ok := out[id]; ok {
			continue
		}
		out[id] = g.Name
	}
	return out
}

// keitaroGroup resolves the group id and name for one log row.
// campaign is the tracker campaign name. campaignID is the tracker id.
// place carries the campaign map, the id-to-name index and optional unknown tally.
// The id comes from place.aliases. A positive campaign id missing from that map
// keeps the decimal tracker id and is noted in place.unknown when set.
// campaignID zero yields an empty id. The name is the stored group name for the
// resolved id, otherwise the campaign name from the log row.
// It does not return an error.
func keitaroGroup(campaign string, campaignID int, place logPlace) (string, string) {
	id := ""
	if campaignID > 0 && place.aliases != nil {
		var ok bool
		id, ok = place.aliases[campaignID]
		id = strings.TrimSpace(id)
		if !ok {
			id = strconv.Itoa(campaignID)
			if place.unknown != nil {
				place.unknown.note(campaignID)
			}
		}
	}
	name := campaign
	if place.names != nil && id != "" {
		if stored := strings.TrimSpace(place.names[id]); stored != "" {
			name = stored
		}
	}
	return id, name
}

// catalogNum formats a tracker catalog id.
// n is the tracker id. It returns the decimal text when n is positive.
// It returns an empty string otherwise. It does not return an error.
func catalogNum(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// keitaroDevice maps a tracker device type onto the engine vocabulary.
// raw is the device_type from a click or a conversion. It is trimmed and lowercased.
// It returns phone, computer or tablet when the text contains those markers,
// an empty string when raw is blank, and the lowercased text otherwise.
// It does not return an error.
func keitaroDevice(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "phone") || strings.Contains(s, "mobile") {
		return "phone"
	}
	if strings.Contains(s, "desktop") || strings.Contains(s, "computer") || strings.Contains(s, "pc") {
		return "computer"
	}
	if strings.Contains(s, "tablet") || strings.Contains(s, "pad") {
		return "tablet"
	}
	return s
}
