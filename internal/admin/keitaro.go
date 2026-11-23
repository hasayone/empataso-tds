package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
)

const (
	keitaroCheckTimeout = 20 * time.Second
	// keitaroVersionTimeout ограничивает чтение журналов для определения версии.
	keitaroVersionTimeout = 10 * time.Second
	// keitaroVersionDays — глубина окна журналов для определения версии.
	keitaroVersionDays = 90
)

// keitaroImportReq is the body of POST /api/keitaro/import.
// From and To are calendar dates, YYYY-MM-DD, used only for clicks and conversions.
type keitaroImportReq struct {
	Domains     bool   `json:"domains"`
	Networks    bool   `json:"networks"`
	Offers      bool   `json:"offers"`
	Landings    bool   `json:"landings"`
	Sources     bool   `json:"sources"`
	Campaigns   bool   `json:"campaigns"`
	Users       bool   `json:"users"`
	Conversions bool   `json:"conversions"`
	Clicks      bool   `json:"clicks"`
	From        string `json:"from"`
	To          string `json:"to"`
	// Board is the target panel section for this import. Empty uses the request section or main.
	Board string `json:"board_id"`
	// CampaignID is the Keitaro campaign id for a dosync of one campaign; 0 imports every campaign.
	CampaignID int `json:"campaign_id"`
	// FiltersOnly rewrites only the filters of imported streams; see forFilters.
	FiltersOnly bool `json:"filters_only"`
	// CampaignIDs are the Keitaro campaigns of a filters_only run.
	CampaignIDs []int `json:"campaign_ids"`
	// AllImported runs filters_only over every campaign in the import manifest.
	AllImported bool `json:"all_imported"`
	// DryRun reports filters_only or parameters_only changes without saving them.
	DryRun bool `json:"dry_run"`
	// ParametersOnly fills empty campaign parameter names and tokens. It does not
	// read clicks or conversions and does not rewrite streams.
	ParametersOnly bool `json:"parameters_only"`
	// Resume continues the last failed full import from its journal; it takes no other field.
	Resume bool `json:"resume,omitempty"`
}

// forCampaign turns a request with campaign_id into the dosync of that one campaign.
// The campaign comes with the offers, landings, networks, traffic source and domain it uses,
// so every catalog flag except users is set. It returns a client message for a negative id or
// for users, clicks or conversions in the same body, or an empty string when the request is valid.
// A zero id leaves the request unchanged.
func (req *keitaroImportReq) forCampaign() string {
	if req.CampaignID == 0 {
		return ""
	}
	if req.CampaignID < 0 {
		return "campaign_id must be a positive Keitaro campaign id"
	}
	if req.Users || req.Clicks || req.Conversions {
		return "campaign_id does not combine with users, clicks or conversions"
	}
	req.Campaigns, req.Offers, req.Landings = true, true, true
	req.Networks, req.Sources, req.Domains = true, true, true
	return ""
}

// handleKeitaroStatus reports whether a tracker origin and key are set.
// It does not call the tracker. The host is the origin's hostname.
// The key is never written.
func (s *Server) handleKeitaroStatus(w http.ResponseWriter, r *http.Request) {
	creds := s.keitaroCreds()
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": creds.configured(),
		"host":       creds.host(),
	})
}

// handleKeitaroCheck checks the tracker key and counts campaigns with one-row page requests,
// so the campaign list is not downloaded (see keitaroCountCampaigns). The count found is
// remembered per tracker origin and speeds up the next check.
// It returns 503 when the origin or key is empty.
// On success it returns ok, the campaign count, api "v1", product_version ("10", "11" or empty when
// the signals are missing or disagree), version_signals (the votes behind it) and version_conflict.
// The version comes from read-only samples of the click and conversion logs (see keitaroDetectVersion).
// A tracker error is 502. Error bodies omit api and product_version.
// The key is removed from the error text. The settings file is not written.
func (s *Server) handleKeitaroCheck(w http.ResponseWriter, r *http.Request) {
	creds := s.keitaroCreds()
	if !creds.configured() {
		writeErr(w, http.StatusServiceUnavailable, "keitaro not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), keitaroCheckTimeout)
	defer cancel()
	base := strings.TrimRight(strings.TrimSpace(creds.Base), "/")
	n, err := keitaroCountCampaigns(ctx, s.keitaroClient(creds).Get, s.ktCount.get(base))
	if err != nil {
		writeErr(w, http.StatusBadGateway, sanitize(err, creds.Key))
		return
	}
	s.ktCount.set(base, n)
	verdict := keitaroDetectVersion(ctx, s.keitaroClient(creds))
	s.ktVersion.set(base, verdict.Version)
	votes := verdict.Votes
	if votes == nil {
		votes = []ktimport.VersionVote{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "campaigns": n, "api": "v1",
		"product_version": verdict.Version, "version_signals": votes, "version_conflict": verdict.Conflict,
	})
}

// keitaroVersionHint remembers the tracker version of the last connection check for one origin.
type keitaroVersionHint struct {
	mu      sync.Mutex
	base    string
	version string
}

// get returns the version remembered for base, or an empty string when none is stored for that origin
// or the last check could not decide.
func (h *keitaroVersionHint) get(base string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.base != base {
		return ""
	}
	return h.version
}

// set stores version as the version of base; an empty version forgets the earlier one.
func (h *keitaroVersionHint) set(base, version string) {
	h.mu.Lock()
	h.base, h.version = base, version
	h.mu.Unlock()
}

// keitaroDetectVersion собирает голоса версии из свежих записей журналов и считает итог.
// Запросы только читают и идут тем же клиентом, что проверка соединения; сбой запроса не прерывает проверку.
//
// Параметры: ctx — отмена и общий срок проверки; client — клиент трекера.
// Результат: версия, голоса и признак противоречия; без голосов версия пуста.
// Ошибки: нет.
func keitaroDetectVersion(ctx context.Context, client *ktimport.Client) ktimport.VersionVerdict {
	ctx, cancel := context.WithTimeout(ctx, keitaroVersionTimeout)
	defer cancel()
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -keitaroVersionDays).Format(keitaroTimeLayout)
	return ktimport.DetectVersion(client.ProbeVersion(ctx, from, now.Format(keitaroTimeLayout)))
}

// needsTracker reports whether the request has to call the tracker.
// It returns true when any catalog, campaign, click, conversion, filters_only or parameters_only flag is set.
func (req keitaroImportReq) needsTracker() bool {
	return req.catalog() || req.Conversions || req.Clicks || req.FiltersOnly || req.ParametersOnly
}

// catalog reports whether a snapshot must be fetched.
// It returns true when any of the six catalog or campaign flags is set.
func (req keitaroImportReq) catalog() bool {
	return req.Domains || req.Networks || req.Offers || req.Landings || req.Sources || req.Campaigns || req.Users
}

// selection is the mapper switch for this request.
// It returns the six catalog flags. Clicks and conversions are not part of it.
func (req keitaroImportReq) selection() ktimport.Selection {
	return ktimport.Selection{
		Domains:   req.Domains,
		Networks:  req.Networks,
		Offers:    req.Offers,
		Landings:  req.Landings,
		Sources:   req.Sources,
		Campaigns: req.Campaigns,
		Users:     req.Users,
		Campaign:  req.CampaignID,
	}
}

// keitaroClient builds a tracker client for one origin and key.
// creds is the copy a job took at its start. The key is sent as a header and is not placed in the URL.
// Progress updates the running import. Reports while no import is running are ignored.
// The HTTP client does not follow a redirect to another host and has no own deadline; the caller context does.
// With a GET budget in creds every GET of the client counts against it.
func (s *Server) keitaroClient(creds ktCreds) *ktimport.Client {
	c := &ktimport.Client{
		Base: strings.TrimRight(strings.TrimSpace(creds.Base), "/"),
		Key:  creds.Key,
		HTTP: keitaroHTTP,
	}
	if creds.gets != nil {
		c.HTTP = creds.gets.client(keitaroHTTP)
	}
	if s.ktJob != nil {
		c.Progress = s.ktJob.progress
	}
	return c
}

// keitaroStoreMissing reports a selected catalog whose store is not wired.
// It returns "store unavailable", or an empty string when every selected store exists.
func (s *Server) keitaroStoreMissing(req keitaroImportReq) string {
	missing := (req.Networks && s.cfg.Networks == nil) ||
		(req.Sources && s.cfg.Sources == nil) ||
		(req.Domains && s.cfg.Domains == nil) ||
		(req.Landings && s.cfg.Landings == nil) ||
		(req.Offers && s.cfg.Offers == nil) ||
		((req.Campaigns || req.FiltersOnly || req.ParametersOnly) && s.cfg.Groups == nil) ||
		(req.Users && !s.usersEnabled())
	if missing {
		return "store unavailable"
	}
	return ""
}

// keitaroHost returns the lower-case hostname of a tracker origin.
// base is the configured URL. It returns an empty string when base has no host.
// It does not return an error and does not include userinfo or a query.
func keitaroHost(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u == nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// sanitize replaces every copy of the tracker key in an error.
// err is the failure. key is the configured API key; an empty key is left as-is.
// It returns the error text, or an empty string when err is nil.
func sanitize(err error, key string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "***")
	}
	return msg
}

// catalogMerge holds parameters for mergeCatalog.
//
// store is the catalog JSON file. idOf reads the record id. valid returns a client
// message or an empty string when the row is acceptable. imported is true for ids
// already recorded in the manifest. owner is the importing session UID for CreatedBy;
// empty when the environment administrator runs the import.
type catalogMerge[T any] struct {
	store    *JSONStore[T]
	idOf     func(T) string
	valid    func(T) string
	imported func(string) bool
	owner    string
}

// importCatalogs writes the selected catalogs in a fixed order.
// st holds the mapper result, prior manifest, owner UID, counts, and losses for this run.
// After each catalog the matching sequence moves past the imported numeric ids. A nil
// sequence is left alone. It returns an error when a store cannot be read or saved.
func (s *Server) importCatalogs(ctx context.Context, req keitaroImportReq, st *importState) error {
	if req.Networks {
		n, extra, err := mergeCatalog(ctx, st.res.Networks, catalogMerge[config.AffiliateNetwork]{
			store: s.cfg.Networks, idOf: func(v config.AffiliateNetwork) string { return v.ID },
			valid: validNetwork, imported: st.prev.Imported(ktimport.KindNetworks), owner: st.owner,
		})
		st.losses = append(st.losses, extra...)
		if err != nil {
			return err
		}
		s.cfg.SeqNetworks.noteAll(st.res.Manifest.Networks)
		st.counts["networks"] = n
	}
	if req.Sources {
		n, extra, err := mergeCatalog(ctx, st.res.Sources, catalogMerge[config.TrafficSource]{
			store: s.cfg.Sources, idOf: func(v config.TrafficSource) string { return v.ID },
			valid: validSource, imported: st.prev.Imported(ktimport.KindSources), owner: st.owner,
		})
		st.losses = append(st.losses, extra...)
		if err != nil {
			return err
		}
		s.cfg.SeqSources.noteAll(st.res.Manifest.Sources)
		st.counts["sources"] = n
	}
	if req.Domains {
		n, extra, err := s.mergeDomains(ctx, st)
		st.losses = append(st.losses, extra...)
		if err != nil {
			return err
		}
		s.cfg.SeqDomains.noteAll(st.res.Manifest.Domains)
		st.counts["domains"] = n
	}
	if req.Landings {
		n, extra, err := mergeCatalog(ctx, st.res.Landings, catalogMerge[config.Landing]{
			store: s.cfg.Landings, idOf: func(v config.Landing) string { return v.ID },
			valid: validLanding, imported: st.prev.Imported(ktimport.KindLandings), owner: st.owner,
		})
		st.losses = append(st.losses, extra...)
		if err != nil {
			return err
		}
		s.cfg.SeqLandings.noteAll(st.res.Manifest.Landings)
		st.counts["landings"] = n
	}
	if req.Offers {
		n, extra, err := mergeCatalog(ctx, st.res.Offers, catalogMerge[config.Offer]{
			store: s.cfg.Offers, idOf: func(v config.Offer) string { return v.ID },
			valid: validOffer, imported: st.prev.Imported(ktimport.KindOffers), owner: st.owner,
		})
		st.losses = append(st.losses, extra...)
		if err != nil {
			return err
		}
		s.cfg.SeqOffers.noteAll(st.res.Manifest.Offers)
		st.counts["offers"] = n
	}
	return nil
}

// mergeDomains merges the imported domains into domains.json under domainMu, so a check, a
// Cloudflare connection or an edit running at the same time cannot be overwritten.
// The lock covers only the read, the merge and the save. st holds the mapper result.
// It returns the count of accepted rows, the losses and an error from the store.
func (s *Server) mergeDomains(ctx context.Context, st *importState) (int, []ktimport.Loss, error) {
	s.domainMu.Lock()
	defer s.domainMu.Unlock()
	return mergeCatalog(ctx, st.res.Domains, catalogMerge[config.Domain]{
		store: s.cfg.Domains, idOf: func(v config.Domain) string { return v.ID },
		valid: validDomain, imported: st.prev.Imported(ktimport.KindDomains), owner: st.owner,
	})
}

// preserveCatalogGroups copies Group from existing rows when the incoming row left it empty.
// existing is the file before merge. kept is the validated incoming slice. idOf reads the id.
// It returns kept with empty Group fields filled from the prior record of the same id.
func preserveCatalogGroups[T any](existing, kept []T, idOf func(T) string) []T {
	if len(existing) == 0 || len(kept) == 0 {
		return kept
	}
	byID := make(map[string]T, len(existing))
	for _, row := range existing {
		byID[idOf(row)] = row
	}
	out := make([]T, len(kept))
	for i, row := range kept {
		id := idOf(row)
		if old, ok := byID[id]; ok {
			row = keepLocalFields(old, row)
		}
		out[i] = row
	}
	return out
}

// createdByOf returns CreatedBy from a catalog row, or an empty string for unknown types.
func createdByOf[T any](item T) string {
	switch n := any(item).(type) {
	case config.Offer:
		return n.CreatedBy
	case config.Landing:
		return n.CreatedBy
	case config.AffiliateNetwork:
		return n.CreatedBy
	case config.TrafficSource:
		return n.CreatedBy
	case config.Domain:
		return n.CreatedBy
	default:
		return ""
	}
}

// keepLocalFields returns next with Group taken from old when empty on next, then restores
// CreatedBy from old when next left it empty. For domains the operator mark, the last
// probe result, the registrar fields and the Cloudflare, connection and watch fields stay. It supports catalog types with group and owner.
func keepLocalFields[T any](old, next T) T {
	switch o := any(old).(type) {
	case config.Offer:
		n := any(next).(config.Offer)
		if n.Group == "" {
			n.Group = o.Group
		}
		return withOwner(any(n).(T), createdByOf(old))
	case config.Landing:
		n := any(next).(config.Landing)
		if n.Group == "" {
			n.Group = o.Group
		}
		return withOwner(any(n).(T), createdByOf(old))
	case config.AffiliateNetwork:
		n := any(next).(config.AffiliateNetwork)
		if n.Group == "" {
			n.Group = o.Group
		}
		return withOwner(any(n).(T), createdByOf(old))
	case config.TrafficSource:
		n := any(next).(config.TrafficSource)
		if n.Group == "" {
			n.Group = o.Group
		}
		return withOwner(any(n).(T), createdByOf(old))
	case config.Domain:
		n := any(next).(config.Domain)
		if n.Group == "" {
			n.Group = o.Group
		}
		n.Disabled = o.Disabled
		n.OK = o.OK
		n.Reason, n.ReasonDetail = o.Reason, o.ReasonDetail
		n.Checked = o.Checked
		n.Registrar, n.RegistrarAccount = o.Registrar, o.RegistrarAccount
		n.RegistrarStatus, n.RegistrarCharged, n.RegistrarExpected = o.RegistrarStatus, o.RegistrarCharged, o.RegistrarExpected
		n.RegistrarPendingSince = o.RegistrarPendingSince
		copyCloudflare(&n, o)
		return withOwner(any(n).(T), createdByOf(old))
	default:
		return withOwner(next, createdByOf(old))
	}
}

// withOwner sets CreatedBy when uid is non-empty and the row field is still empty.
func withOwner[T any](item T, uid string) T {
	if uid == "" {
		return item
	}
	switch n := any(item).(type) {
	case config.Offer:
		if n.CreatedBy == "" {
			n.CreatedBy = uid
		}
		return any(n).(T)
	case config.Landing:
		if n.CreatedBy == "" {
			n.CreatedBy = uid
		}
		return any(n).(T)
	case config.AffiliateNetwork:
		if n.CreatedBy == "" {
			n.CreatedBy = uid
		}
		return any(n).(T)
	case config.TrafficSource:
		if n.CreatedBy == "" {
			n.CreatedBy = uid
		}
		return any(n).(T)
	case config.Domain:
		if n.CreatedBy == "" {
			n.CreatedBy = uid
		}
		return any(n).(T)
	default:
		return item
	}
}

// mergeCatalog validates incoming rows, merges them by id and saves the list.
// incoming is the mapper output. cm carries the store, validators, manifest filter and owner.
// The count is how many incoming rows passed validation. Losses are rejections and id conflicts.
// The read, the merge and the save run under the store lock that catalog writes also take,
// so a concurrent create, replace or delete is not lost; callers must not hold that lock.
// An error means the file could not be read or saved.
func mergeCatalog[B any, T any](ctx context.Context, incoming []B, cm catalogMerge[T]) (int, []ktimport.Loss, error) {
	defer cm.store.lock()()
	existing, err := cm.store.List(ctx)
	if err != nil {
		return 0, nil, err
	}
	kept := make([]T, 0, len(incoming))
	var losses []ktimport.Loss
	for _, src := range incoming {
		item, err := convert[T](src)
		if err != nil {
			return 0, losses, err
		}
		if msg := cm.valid(item); msg != "" {
			losses = append(losses, ktimport.Loss{Kind: "skipped", Ref: cm.idOf(item), Detail: msg})
			continue
		}
		kept = append(kept, item)
	}
	kept = preserveCatalogGroups(existing, kept, cm.idOf)
	for i := range kept {
		kept[i] = withOwner(kept[i], cm.owner)
	}
	merged, extra := ktimport.MergeByIDReport(existing, kept, cm.idOf, cm.imported)
	losses = append(losses, extra...)
	if err := cm.store.Save(ctx, merged); err != nil {
		return 0, losses, err
	}
	return len(kept), losses, nil
}

// mergeImportedGroups applies the bridge merge and keeps local group fields.
// existing is the file. incoming is the mapper output. prev is the manifest from before this run.
// owner is the importing session UID for CreatedBy on new groups, or empty for root.
// A group is replaced only when prev already lists its id. Otherwise the stored group is kept.
// A replaced import keeps its stored number when the incoming row has none,
// and always keeps the stored sticky_stream and traffic_log_off switches, which Keitaro does not carry.
// A parameter name, token or price field that is already stored stays, including an empty name
// that clears a source mapping. An absent key is filled from the incoming row.
// A new import keeps the number from the merge. Missing numbers are filled for ids in this import.
// Losses are the bridge merge report. An error means a JSON conversion failed.
func mergeImportedGroups(existing []config.Group, incoming []ktimport.Group, prev ktimport.Manifest, owner string, board string) ([]config.Group, []ktimport.Loss, error) {
	board = config.NormalizeBoardID(board)
	bridgeExisting, err := convert[[]ktimport.Group](existing)
	if err != nil {
		return nil, nil, err
	}
	merged, losses := ktimport.Merge(bridgeExisting, incoming, prev.Imported(ktimport.KindGroups))
	incomingByID := map[string]ktimport.Group{}
	ids := make([]string, 0, len(incoming))
	for _, g := range incoming {
		incomingByID[g.ID] = g
		ids = append(ids, g.ID)
	}
	stored := map[string]config.Group{}
	for _, g := range existing {
		stored[g.ID] = g
	}
	out := make([]config.Group, 0, len(merged))
	for _, g := range merged {
		old, ok := stored[g.ID]
		if !ok {
			cg, err := convert[config.Group](g)
			if err != nil {
				return nil, nil, err
			}
			if owner != "" && cg.CreatedBy == "" {
				cg.CreatedBy = owner
			}
			cg.BoardID = board
			out = append(out, cg)
			continue
		}
		if _, inc := incomingByID[g.ID]; inc && prev.Has(ktimport.KindGroups, g.ID) {
			cg, err := convert[config.Group](incomingByID[g.ID])
			if err != nil {
				return nil, nil, err
			}
			if cg.Num <= 0 {
				cg.Num = old.Num
			}
			if cg.CreatedBy == "" {
				cg.CreatedBy = old.CreatedBy
			}
			cg.StickyStream = old.StickyStream
			cg.TrafficLogOff = old.TrafficLogOff
			if cg.Notes == "" {
				cg.Notes = old.Notes
			}
			cg.BoardID = old.BoardID
			keepCampaignParams(old, &cg)
			out = append(out, cg)
			continue
		}
		out = append(out, old)
	}
	ensureGroupNums(out, ids)
	return out, losses, nil
}

// ensureGroupNums gives each group from this import a campaign number when it has none.
// out is the merged list and is updated in place. ids are the group ids of this import.
// The next number is one past the highest Num already set. Any other id is left unchanged.
// It does not return a value or an error.
func ensureGroupNums(out []config.Group, ids []string) {
	want := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			want[id] = true
		}
	}
	max := 0
	for _, g := range out {
		if g.Num > max {
			max = g.Num
		}
	}
	for i := range out {
		if out[i].Num > 0 || !want[out[i].ID] {
			continue
		}
		max++
		out[i].Num = max
	}
}

// loadManifest reads the import manifest.
// It returns an empty manifest when the store is not set.
// It returns an error when the file cannot be read or parsed.
func (s *Server) loadManifest() (ktimport.Manifest, error) {
	return loadManifestStore(s.cfg.Manifest)
}

// loadManifestStore reads a manifest from store. A nil store yields an empty manifest.
func loadManifestStore(store ManifestStore) (ktimport.Manifest, error) {
	if store == nil {
		return ktimport.Manifest{}, nil
	}
	return store.Load()
}

// saveRunManifest stores this run's manifest on top of the one loaded at the start.
// base is that earlier manifest and is not modified. res is the mapper output.
// host is the tracker hostname of this run's snapshot. A nil store does nothing.
// It returns an error when the file cannot be written.
func (s *Server) saveRunManifest(base ktimport.Manifest, res ktimport.Result, host string) error {
	return s.saveRunManifestStore(s.cfg.Manifest, base, res, host)
}

// saveRunManifestStore merges res.Manifest into base and writes store.
func (s *Server) saveRunManifestStore(store ManifestStore, base ktimport.Manifest, res ktimport.Result, host string) error {
	next := base.Merge(res.Manifest)
	next.Host = host
	if store == nil {
		return nil
	}
	return store.Save(next)
}

// convert copies v into T through JSON.
// v is the source value. It returns the decoded value.
// An error means the value cannot be encoded or does not fit T.
func convert[T any](v any) (T, error) {
	var zero T
	raw, err := json.Marshal(v)
	if err != nil {
		return zero, err
	}
	if string(raw) == "null" {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// maskLosses hides query values and the tracker key in loss text.
// losses is the report. key is the configured API key.
// It returns a new slice; a nil input becomes an empty slice.
func maskLosses(losses []ktimport.Loss, key string) []ktimport.Loss {
	if losses == nil {
		return []ktimport.Loss{}
	}
	out := make([]ktimport.Loss, len(losses))
	for i, l := range losses {
		l.Ref = maskSecret(l.Ref, key)
		l.Detail = maskSecret(l.Detail, key)
		out[i] = l
	}
	return out
}

// maskSecret masks a URL and removes the tracker key.
// s is one loss field. key is the configured API key.
// A value that contains :// is passed through MaskURL. It does not return an error.
func maskSecret(s, key string) string {
	if strings.Contains(s, "://") {
		s = ktimport.MaskURL(s)
	}
	if key != "" {
		s = strings.ReplaceAll(s, key, "***")
	}
	return s
}

// logKeitaro writes one import line with the selected counts and the duration.
// counts holds only kinds the request asked for. elapsed is how long the run took.
// URLs and the key are not logged. It does not return an error.
func (s *Server) logKeitaro(msg string, counts map[string]int, elapsed time.Duration) {
	args := make([]any, 0, len(counts)*2+2)
	for _, name := range []string{"lists", "pages", "groups", "users", "offers", "landings", "landing_files", "networks", "sources", "domains", "conversions", "clicks"} {
		n, ok := counts[name]
		if !ok {
			continue
		}
		args = append(args, name, n)
	}
	args = append(args, "duration", elapsed.Round(time.Millisecond).String())
	s.logInfo(msg, args...)
}
