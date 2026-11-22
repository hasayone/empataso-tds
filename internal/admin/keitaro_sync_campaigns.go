package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/emtds/bridge/ktimport"
)

// Limits of one sync pass toward the tracker: GET requests per pass, GET requests of one
// campaign snapshot apart from the streams, new campaigns per pass, and the wait after a failure.
const (
	keitaroSyncGETCap        = 300
	keitaroSnapshotGETs      = 14
	keitaroSyncCampaignBatch = 50
	keitaroCampaignsRetry    = time.Hour
)

// errKeitaroGETCap is the transport error once the GET budget of a pass is spent.
var errKeitaroGETCap = errors.New("keitaro: лимит GET за прогон")

// keitaroGETBudget counts the GET requests of one sync pass and refuses them over the limit.
// It also remembers a 402 or 429 answer, since optional catalog reads hide the status.
// The zero value is not usable; use newKeitaroGETBudget.
type keitaroGETBudget struct {
	mu      sync.Mutex
	limit   int
	used    int
	refused int
}

// newKeitaroGETBudget returns a budget of limit GET requests.
func newKeitaroGETBudget(limit int) *keitaroGETBudget {
	return &keitaroGETBudget{limit: limit}
}

// left returns how many GET requests the pass may still send. A nil budget has no limit.
func (b *keitaroGETBudget) left() int {
	if b == nil {
		return keitaroSyncGETCap
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit - b.used
}

// refusal returns the last 402 or 429 status the tracker answered, or 0.
func (b *keitaroGETBudget) refusal() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.refused
}

// take counts one GET. It returns errKeitaroGETCap when the limit is reached.
func (b *keitaroGETBudget) take() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.limit {
		return errKeitaroGETCap
	}
	b.used++
	return nil
}

// note records a refusing status of the tracker.
func (b *keitaroGETBudget) note(code int) {
	if code != http.StatusPaymentRequired && code != http.StatusTooManyRequests {
		return
	}
	b.mu.Lock()
	b.refused = code
	b.mu.Unlock()
}

// client wraps base so that its GET requests use this budget.
// base keeps its redirect rule and timeout. It returns a new client.
func (b *keitaroGETBudget) client(base *http.Client) *http.Client {
	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	return &http.Client{
		Transport:     budgetTransport{budget: b, next: next},
		CheckRedirect: base.CheckRedirect,
		Timeout:       base.Timeout,
	}
}

// budgetTransport sends requests through next and counts GET requests in budget.
type budgetTransport struct {
	budget *keitaroGETBudget
	next   http.RoundTripper
}

// RoundTrip refuses a GET over the budget before the network and notes 402 and 429 answers.
// The error is errKeitaroGETCap or the error of next.
func (t budgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet {
		if err := t.budget.take(); err != nil {
			return nil, err
		}
	}
	res, err := t.next.RoundTrip(req)
	if err == nil {
		t.budget.note(res.StatusCode)
	}
	return res, err
}

// newCampaignsBatch is the merged dosync result of the new campaigns one pass imports.
// res holds their groups, lists and pages and the catalog rows that are not stored yet.
// taken are the handled campaign ids in ascending order, skipped those whose group
// id the manifest already lists, so a group deleted here is not brought back.
type newCampaignsBatch struct {
	res     ktimport.Result
	taken   []int
	skipped []int
}

// keitaroSyncNewCampaigns imports the Keitaro campaigns that the manifest of target does not list,
// with their streams, filters and settings, through the mapper of the dosync of one campaign.
// Groups and catalog records already stored are never rewritten: catalog rows go in insert-only
// as in the catalog top-up, and a campaign whose group id the manifest lists is skipped.
// Campaigns go in ascending id; st.CampaignsCursor keeps the last handled one, so a campaign
// that failed is not retried on every pass. The GET budget of target.creds bounds the batch;
// the rest waits for the next pass. A tracker failure, a 402 or 429 answer, or a store failure
// pauses the step for keitaroCampaignsRetry and never fails the pass.
// It returns how many campaigns were imported.
func (s *Server) keitaroSyncNewCampaigns(ctx context.Context, target keitaroSyncTarget, st *keitaroSyncFile) int {
	now := s.keitaroSyncClock()
	if s.cfg.Groups == nil || now.Before(st.CampaignsPausedUntil) {
		return 0
	}
	st.CampaignsPausedUntil, st.CampaignsError, st.CampaignsFailed = time.Time{}, "", nil
	pause := func(reason string) int {
		st.CampaignsPausedUntil = now.Add(keitaroCampaignsRetry)
		st.CampaignsError = reason
		s.logWarn("keitaro sync campaigns paused", "reason", reason)
		return 0
	}
	creds := target.creds
	fresh, deleted, err := s.findNewCampaigns(ctx, target, st.CampaignsCursor)
	if code := creds.gets.refusal(); code != 0 {
		return pause(fmt.Sprintf("Keitaro ответил %d", code))
	}
	if err != nil {
		return pause(sanitize(err, creds.Key))
	}
	st.CampaignsDeleted = deleted
	st.CampaignsPending = len(fresh)
	n := min(len(fresh), keitaroSyncCampaignBatch, (creds.gets.left()-keitaroSnapshotGETs)/2)
	if n <= 0 {
		return 0
	}
	snap, err := s.keitaroClient(creds).FetchCampaignsSnapshot(ctx, fresh[:n])
	if code := creds.gets.refusal(); code != 0 {
		return pause(fmt.Sprintf("Keitaro ответил %d", code))
	}
	if err != nil {
		return pause(sanitize(err, creds.Key))
	}
	batch, err := s.mapNewCampaigns(ctx, target.manifestStore, snap, fresh[:n], creds.gets.left())
	if err != nil {
		return pause("кампании не сохранены")
	}
	if len(batch.taken) == 0 {
		return 0
	}
	if err := s.saveNewCampaigns(ctx, target, batch); err != nil {
		s.logWarn("keitaro sync campaigns save failed", "err", err)
		return pause("кампании не сохранены")
	}
	imported, failed, err := s.newCampaignsOutcome(target.manifestStore, batch)
	if err != nil {
		return pause("манифест не прочитан")
	}
	st.CampaignsCursor = batch.taken[len(batch.taken)-1]
	st.CampaignsPending = len(fresh) - len(batch.taken)
	st.CampaignsDeleted += len(batch.skipped)
	st.CampaignsFailed = failed
	s.logInfo("keitaro sync campaigns", "imported", imported, "failed", len(failed), "pending", st.CampaignsPending)
	return imported
}

// findNewCampaigns lists the Keitaro campaigns above cursor that the manifest of target does not list.
// deleted counts the listed campaigns whose group no longer exists here.
// It reads the campaign list with one GET. An error comes from the manifest, the groups or the tracker.
func (s *Server) findNewCampaigns(ctx context.Context, target keitaroSyncTarget, cursor int) (fresh []int, deleted int, err error) {
	manifest, err := loadManifestStore(target.manifestStore)
	if err != nil {
		return nil, 0, err
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return nil, 0, err
	}
	have := groupIDSet(groups)
	camps, err := s.keitaroClient(target.creds).FetchCampaigns(ctx)
	if err != nil {
		return nil, 0, err
	}
	for _, c := range camps {
		id := int(c.ID)
		if id <= 0 {
			continue
		}
		if gid, ok := manifest.Campaigns[strconv.Itoa(id)]; ok {
			if !have[gid] {
				deleted++
			}
			continue
		}
		if id > cursor {
			fresh = append(fresh, id)
		}
	}
	sort.Ints(fresh)
	return fresh, deleted, nil
}

// mapNewCampaigns maps every campaign of ids with the selection of a dosync and merges the results.
// Catalog rows that are stored or listed in the manifest are dropped. A campaign whose local
// landings need more archive GETs than budget is not taken, nor any after it.
// It returns the batch. An error comes from reading the manifest or the catalogs.
func (s *Server) mapNewCampaigns(ctx context.Context, store ManifestStore, snap ktimport.Snapshot, ids []int, budget int) (newCampaignsBatch, error) {
	return s.mapCampaignSet(ctx, store, snap, ids, budget, true)
}

// mapCampaignSet maps every campaign of ids and merges the results; see mapNewCampaigns.
// skipKnown drops a campaign whose group id the manifest already lists: true for the new-campaign
// step, false for the step that refreshes campaigns already imported.
func (s *Server) mapCampaignSet(ctx context.Context, store ManifestStore, snap ktimport.Snapshot, ids []int, budget int, skipKnown bool) (newCampaignsBatch, error) {
	var out newCampaignsBatch
	prev, err := loadManifestStore(store)
	if err != nil {
		return out, err
	}
	known, err := s.catalogIDSets(ctx)
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		r := ktimport.Map(snap.ForCampaign(id), ktimport.Selection{
			Offers: true, Landings: true, Networks: true, Sources: true, Domains: true, Campaigns: true,
			Campaign: id, Known: prev.Has,
		})
		if skipKnown && groupsKnown(r.Groups, prev) {
			out.taken = append(out.taken, id)
			out.skipped = append(out.skipped, id)
			continue
		}
		fresh := newCatalogRows(r, known, prev)
		need := len(fresh.LocalLandingIDs())
		if need > budget {
			break
		}
		budget -= need
		markCatalogRows(fresh, known)
		addCampaignResult(&out.res, r, fresh)
		out.taken = append(out.taken, id)
	}
	return out, nil
}

// groupsKnown reports whether the manifest already lists the id of one of groups.
func groupsKnown(groups []ktimport.Group, m ktimport.Manifest) bool {
	for _, g := range groups {
		if m.Has(ktimport.KindGroups, g.ID) {
			return true
		}
	}
	return false
}

// newCatalogRows returns the catalog rows of res whose ids are neither in known nor in the manifest.
// A kind absent from known has no store and keeps no rows. Groups, lists and pages are not copied.
func newCatalogRows(res ktimport.Result, known map[string]map[string]bool, m ktimport.Manifest) ktimport.Result {
	keep := func(kind, id string) bool {
		set, ok := known[kind]
		return ok && !set[id] && !m.Has(kind, id)
	}
	var out ktimport.Result
	out.Offers = keepRows(res.Offers, func(v ktimport.CatOffer) bool { return keep(ktimport.KindOffers, v.ID) })
	out.Landings = keepRows(res.Landings, func(v ktimport.CatLanding) bool { return keep(ktimport.KindLandings, v.ID) })
	out.Networks = keepRows(res.Networks, func(v ktimport.CatNetwork) bool { return keep(ktimport.KindNetworks, v.ID) })
	out.Sources = keepRows(res.Sources, func(v ktimport.CatSource) bool { return keep(ktimport.KindSources, v.ID) })
	out.Domains = keepRows(res.Domains, func(v ktimport.CatDomain) bool { return keep(ktimport.KindDomains, v.ID) })
	return out
}

// keepRows returns the rows for which ok is true, in order.
func keepRows[T any](rows []T, ok func(T) bool) []T {
	var out []T
	for _, r := range rows {
		if ok(r) {
			out = append(out, r)
		}
	}
	return out
}

// markCatalogRows adds the ids of the catalog rows of res to known, so a later campaign of the
// same batch does not add them twice.
func markCatalogRows(res ktimport.Result, known map[string]map[string]bool) {
	for kind, ids := range catalogRowIDs(res) {
		for _, id := range ids {
			known[kind][id] = true
		}
	}
}

// catalogRowIDs returns the ids of the catalog rows of res per manifest kind.
func catalogRowIDs(res ktimport.Result) map[string][]string {
	out := map[string][]string{}
	for _, v := range res.Offers {
		out[ktimport.KindOffers] = append(out[ktimport.KindOffers], v.ID)
	}
	for _, v := range res.Landings {
		out[ktimport.KindLandings] = append(out[ktimport.KindLandings], v.ID)
	}
	for _, v := range res.Networks {
		out[ktimport.KindNetworks] = append(out[ktimport.KindNetworks], v.ID)
	}
	for _, v := range res.Sources {
		out[ktimport.KindSources] = append(out[ktimport.KindSources], v.ID)
	}
	for _, v := range res.Domains {
		out[ktimport.KindDomains] = append(out[ktimport.KindDomains], v.ID)
	}
	return out
}

// addCampaignResult appends the groups, lists, pages and losses of r and the catalog rows of fresh to dst.
func addCampaignResult(dst *ktimport.Result, r, fresh ktimport.Result) {
	dst.Groups = append(dst.Groups, r.Groups...)
	dst.Losses = append(dst.Losses, r.Losses...)
	dst.Offers = append(dst.Offers, fresh.Offers...)
	dst.Landings = append(dst.Landings, fresh.Landings...)
	dst.Networks = append(dst.Networks, fresh.Networks...)
	dst.Sources = append(dst.Sources, fresh.Sources...)
	dst.Domains = append(dst.Domains, fresh.Domains...)
	for name, body := range r.Lists {
		if dst.Lists == nil {
			dst.Lists = map[string]string{}
		}
		dst.Lists[name] = body
	}
	for name, body := range r.Pages {
		if dst.Pages == nil {
			dst.Pages = map[string]string{}
		}
		dst.Pages[name] = body
	}
}

// saveNewCampaigns writes a batch: catalog rows through the insert-only top-up merge, landing
// archives of the added local landings, then lists, pages and groups through the import stages.
// New catalog ids, lists, pages and accepted groups join the manifest of target.
// An error means a store or the manifest could not be read or saved; archive failures are only logged.
func (s *Server) saveNewCampaigns(ctx context.Context, target keitaroSyncTarget, batch newCampaignsBatch) error {
	res := batch.res
	if err := s.saveCampaignAssets(ctx, target, &res); err != nil {
		return err
	}
	prev, err := loadManifestStore(target.manifestStore)
	if err != nil {
		return err
	}
	var run ktimport.Manifest
	run.Add(ktimport.KindLists, sortedNames(res.Lists)...)
	run.Add(ktimport.KindPages, sortedNames(res.Pages)...)
	_, losses, err := s.importGroups(ctx, res.Groups, prev, "", &run, target.creds.host(), target.board, target.manifestStore)
	if len(losses) > 0 {
		s.logInfo("keitaro sync campaigns report", "lines", len(losses))
	}
	return err
}

// saveCampaignAssets writes what the groups of res refer to: catalog rows through the insert-only
// merge, landing archives of the added local landings, IP lists and HTML pages. res.Groups may lose
// IP list names when the lists store is missing. The stored catalog ids join the manifest of target;
// lists, pages and groups do not.
// An error means a store or the manifest could not be read or saved; archive failures are only logged.
func (s *Server) saveCampaignAssets(ctx context.Context, target keitaroSyncTarget, resp *ktimport.Result) error {
	res := *resp
	added, err := s.insertCampaignCatalogs(ctx, target.manifestStore, res)
	if err != nil {
		return err
	}
	addedLandings := map[string]bool{}
	for _, id := range added[ktimport.KindLandings] {
		addedLandings[id] = true
	}
	files := ktimport.Result{Landings: keepRows(res.Landings, func(v ktimport.CatLanding) bool { return addedLandings[v.ID] })}
	if _, losses, err := s.importLandingFiles(ctx, target.creds, files); err != nil {
		return err
	} else if len(losses) > 0 {
		s.logWarn("keitaro sync campaigns landing files", "missed", len(losses))
	}
	if _, _, err := s.importLists(&res); err != nil {
		return err
	}
	if _, _, err := s.importPages(&res); err != nil {
		return err
	}
	*resp = res
	return nil
}

// insertCampaignCatalogs merges catalog rows that are not stored yet with mergeTopUp, raises
// the id counters and records the stored ids in the manifest, as the catalog top-up does.
// It returns the ids now stored per kind. An error comes from a catalog or the manifest.
func (s *Server) insertCampaignCatalogs(ctx context.Context, store ManifestStore, res ktimport.Result) (map[string][]string, error) {
	out := catalogTopUp{added: map[string]int{}, complete: true}
	if err := s.mergeTopUp(ctx, res, &out); err != nil {
		return nil, err
	}
	after, err := s.catalogIDSets(ctx)
	if err != nil {
		return nil, err
	}
	added := map[string][]string{}
	refs := newCatalogRefs()
	for kind, ids := range catalogRowIDs(res) {
		for _, id := range ids {
			if after[kind][id] {
				added[kind] = append(added[kind], id)
				refs.add(kind, id)
			}
		}
	}
	s.raiseCatalogSeqs(refs)
	if err := saveTopUpManifest(store, added); err != nil {
		return nil, err
	}
	return added, nil
}

// newCampaignsOutcome splits the taken campaigns of batch into imported and failed by the manifest.
// Skipped campaigns count as neither. An error comes from reading the manifest.
func (s *Server) newCampaignsOutcome(store ManifestStore, batch newCampaignsBatch) (int, []int, error) {
	m, err := loadManifestStore(store)
	if err != nil {
		return 0, nil, err
	}
	skipped := map[int]bool{}
	for _, id := range batch.skipped {
		skipped[id] = true
	}
	imported := 0
	var failed []int
	for _, id := range batch.taken {
		switch {
		case skipped[id]:
		case m.Campaigns[strconv.Itoa(id)] != "":
			imported++
		default:
			failed = append(failed, id)
		}
	}
	return imported, failed, nil
}

// sortedNames returns the keys of m in ascending order, or nil for an empty map.
func sortedNames(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
