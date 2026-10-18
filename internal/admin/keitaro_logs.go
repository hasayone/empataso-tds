package admin

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/logbuf"
	"github.com/egerkuzma/kuztds/internal/store"
	"github.com/kuztds/bridge/ktimport"
)

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
// aliases maps a campaign id to its group id and is nil when campaigns
// were not fetched in this request. groups is the groups-store snapshot
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
// aliases maps a campaign id to a snapshot alias. names maps a group id to
// the group name from the groups store. Either map may be nil.
type logPlace struct {
	aliases map[int]string
	names   map[string]string
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
// counts and losses are updated. It returns a tracker or warehouse error.
func (s *Server) importLogs(ctx context.Context, req keitaroImportReq, li logImport, counts map[string]int, losses *[]ktimport.Loss) error {
	place := logPlace{aliases: li.aliases, names: groupNames(li.groups)}
	client := s.keitaroClient(li.creds)
	var clickIdx keitaroClickFillIndex
	if req.Clicks {
		if s.cfg.Warehouse == nil || li.host == "" {
			counts["clicks"] = 0
			*losses = append(*losses, ktimport.Loss{Kind: "skipped", Ref: "clicks", Detail: "clickhouse unavailable"})
		} else {
			span := logSpan{from: li.from, to: li.to, host: li.host, place: place}
			n, idx, err := s.importClicks(ctx, client, span)
			if err != nil {
				return err
			}
			counts["clicks"] = n
			clickIdx = idx
			noteExtendedColumns(losses, client, "clicks")
		}
	}
	if req.Conversions {
		if s.cfg.Warehouse == nil || li.host == "" {
			counts["conversions"] = 0
			*losses = append(*losses, ktimport.Loss{Kind: "skipped", Ref: "conversions", Detail: "clickhouse unavailable"})
		} else {
			span := logSpan{from: li.from, to: li.to, host: li.host, place: place}
			n, err := s.importConversions(ctx, client, span, clickIdx)
			if err != nil {
				return err
			}
			counts["conversions"] = n
			noteExtendedColumns(losses, client, "conversions")
		}
	}
	return nil
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
// It returns how many rows were read. An error comes from the tracker or the warehouse.
func (s *Server) importConversions(ctx context.Context, client *ktimport.Client, span logSpan, clickIdx keitaroClickFillIndex) (int, error) {
	var batch []store.ConversionInsert
	n, err := client.FetchConversions(ctx, span.from, span.to, func(row ktimport.ConversionRow) error {
		var snap *store.ClickFill
		if clickIdx != nil {
			if fill, ok := clickIdx[row.SubID]; ok {
				cp := fill
				snap = &cp
			}
		}
		batch = append(batch, conversionInsert(row, span.host, span.place, snap))
		if len(batch) < keitaroBatch {
			return nil
		}
		cp := append([]store.ConversionInsert(nil), batch...)
		batch = batch[:0]
		return s.cfg.Warehouse.InsertConversions(ctx, cp)
	})
	if err != nil {
		return n, err
	}
	if len(batch) == 0 {
		return n, nil
	}
	cp := append([]store.ConversionInsert(nil), batch...)
	if err := s.cfg.Warehouse.InsertConversions(ctx, cp); err != nil {
		return n, err
	}
	return n, nil
}

// importClicks reads the click log and writes batches of 1000.
// client is the tracker reader, so a column fallback stays visible to the caller.
// span carries the tracker range, domain host and group mapping for each row.
// It returns how many rows were read, the sub_id index for conversion backfill,
// and an error from the tracker or the warehouse.
func (s *Server) importClicks(ctx context.Context, client *ktimport.Client, span logSpan) (int, keitaroClickFillIndex, error) {
	idx := make(keitaroClickFillIndex)
	var batch []logbuf.Event
	n, err := client.FetchClicks(ctx, span.from, span.to, func(row ktimport.ClickRow) error {
		ev := clickEvent(row, span.host, span.place)
		idx.put(row.SubID, clickFillFromEvent(ev))
		batch = append(batch, ev)
		if len(batch) < keitaroBatch {
			return nil
		}
		cp := append([]logbuf.Event(nil), batch...)
		batch = batch[:0]
		return s.cfg.Warehouse.InsertEvents(ctx, cp)
	})
	if err != nil {
		return n, idx, err
	}
	if len(batch) == 0 {
		return n, idx, nil
	}
	cp := append([]logbuf.Event(nil), batch...)
	if err := s.cfg.Warehouse.InsertEvents(ctx, cp); err != nil {
		return n, idx, err
	}
	return n, idx, nil
}

// clickFillFromEvent maps one imported click event onto a ClickFill snapshot.
// ev is the row about to be written to events. It does not return an error.
func clickFillFromEvent(ev logbuf.Event) store.ClickFill {
	return store.ClickFill{
		TS:         ev.Ts,
		GroupID:    ev.GroupID,
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
		TS:             ts,
		Domain:         host,
		Device:         keitaroDevice(row.DeviceType),
		Operator:       row.Operator,
		Country:        row.CountryCode,
		City:           row.City,
		Profit:         float64(row.Revenue),
		GroupName:      groupName,
		StreamName:     row.Stream,
		CID:            row.SubID,
		Status:         strings.ToLower(strings.TrimSpace(row.Status)),
		OfferID:        catalogNum(int(row.OfferID)),
		LandingID:      catalogNum(int(row.LandingID)),
		SourceID:       catalogNum(int(row.SourceID)),
		NetworkID:      catalogNum(int(row.NetworkID)),
		Subs:           row.Subs,
		GroupID:        groupID,
		ClickTS:        parseKeitaroTime(row.ClickDatetime),
		OriginalStatus: original,
		PrevStatus:     strings.ToLower(strings.TrimSpace(row.PreviousStatus)),
		TID:            string(row.TID),
		IP:             row.IP,
		Region:         row.Region,
		Lang:           row.Language,
		OS:             row.OS,
		OSVersion:      row.OSVersion,
		Browser:        row.Browser,
		BrowserV:       row.BrowserVersion,
		DeviceModel:    row.DeviceModel,
		UserAgent:      row.UserAgent,
		Referer:        row.Referrer,
		SE:             row.SearchEngine,
		Keyword:        row.Keyword,
		Org:            row.ISP,
		AdCampaignID:   string(row.AdCampaignID),
		CreativeID:     string(row.CreativeID),
		ExternalID:     string(row.ExternalID),
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
	return logbuf.Event{
		Ts:           parseKeitaroTime(row.Datetime),
		GroupID:      groupID,
		GroupName:    groupName,
		Stream:       row.Stream,
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
// place carries the snapshot aliases and the id-to-name index.
// The id is the snapshot alias when it is present, otherwise the decimal campaign id
// when campaignID is positive, or empty when there is no alias and campaignID is not positive.
// The name is the stored group name for that id, otherwise the campaign name.
// It does not return an error.
func keitaroGroup(campaign string, campaignID int, place logPlace) (string, string) {
	id := ""
	if place.aliases != nil {
		id = strings.TrimSpace(place.aliases[campaignID])
	}
	if id == "" && campaignID > 0 {
		id = strconv.Itoa(campaignID)
	}
	name := campaign
	if place.names != nil {
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

// campaignAliases maps a campaign id to its alias.
// snap is a snapshot fetched in this request. Empty aliases are omitted.
// It does not return an error.
func campaignAliases(snap ktimport.Snapshot) map[int]string {
	out := map[int]string{}
	for _, camp := range snap.Campaigns {
		alias := strings.TrimSpace(camp.Alias)
		if alias == "" {
			continue
		}
		out[int(camp.ID)] = alias
	}
	return out
}
