package admin

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/emtds/bridge/ktimport"
	"github.com/hasayone/empataso-tds/internal/config"
	"github.com/hasayone/empataso-tds/internal/store"
)

// keitaroTopUpCap is the most catalog records one top-up asks the tracker for.
const keitaroTopUpCap = 300

// catalogTopUpRef is the Ref of every top-up report line, so the sync can pick them out.
const catalogTopUpRef = "catalog"

// topUpKind names one catalog kind in report lines.
// kind is the manifest kind. one and many are the nouns, missOne and missMany the verbs.
type topUpKind struct {
	kind, one, many, missOne, missMany string
}

// topUpKinds lists the kinds a top-up fills, in report order.
var topUpKinds = []topUpKind{
	{ktimport.KindOffers, "оффер", "офферы", "не найден", "не найдены"},
	{ktimport.KindLandings, "лендинг", "лендинги", "не найден", "не найдены"},
	{ktimport.KindNetworks, "сеть", "сети", "не найдена", "не найдены"},
	{ktimport.KindSources, "источник", "источники", "не найден", "не найдены"},
	{ktimport.KindDomains, "домен", "домены", "не найден", "не найдены"},
}

// catalogRefs collects catalog ids that imported log rows refer to, keyed by manifest kind.
// A nil value ignores note and add.
type catalogRefs map[string]map[string]struct{}

// newCatalogRefs returns an empty collector.
func newCatalogRefs() catalogRefs {
	return catalogRefs{}
}

// note records the offer, landing, network and source ids of one log row. Empty ids are skipped.
func (r catalogRefs) note(offer, landing, network, source string) {
	r.add(ktimport.KindOffers, offer)
	r.add(ktimport.KindLandings, landing)
	r.add(ktimport.KindNetworks, network)
	r.add(ktimport.KindSources, source)
}

// add records ids of one kind. Blank ids and a nil collector are ignored.
func (r catalogRefs) add(kind string, ids ...string) {
	if r == nil {
		return
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if r[kind] == nil {
			r[kind] = map[string]struct{}{}
		}
		r[kind][id] = struct{}{}
	}
}

// numeric returns the positive numeric ids of one kind in ascending order.
func (r catalogRefs) numeric(kind string) []int {
	var out []int
	for id := range r[kind] {
		if n, err := strconv.Atoi(id); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// catalogRefsFrom converts warehouse ids into a collector.
func catalogRefsFrom(in store.CatalogRefs) catalogRefs {
	r := newCatalogRefs()
	r.add(ktimport.KindOffers, in.Offers...)
	r.add(ktimport.KindLandings, in.Landings...)
	r.add(ktimport.KindNetworks, in.Networks...)
	r.add(ktimport.KindSources, in.Sources...)
	return r
}

// catalogRefReader is the optional warehouse side that lists ids stored rows refer to.
type catalogRefReader interface {
	CatalogRefs(ctx context.Context, domain string) (store.CatalogRefs, error)
}

// catalogTopUp is the outcome of one insert-only top-up.
// added counts new records per kind. losses are report lines with Ref "catalog" plus mapper lines.
// complete is false when the cap cut the list or the tracker or a store failed.
// failed is true only when the tracker or a store failed, not when the cap cut the list.
type catalogTopUp struct {
	added    map[string]int
	losses   []ktimport.Loss
	complete bool
	failed   bool
}

// total returns the number of records added in all kinds.
func (t catalogTopUp) total() int {
	n := 0
	for _, v := range t.added {
		n += v
	}
	return n
}

// warehouseCatalogRefs reads every catalog id stored events and conversions of host refer to.
// ok is false when the warehouse does not offer that read. An error comes from the warehouse.
func (s *Server) warehouseCatalogRefs(ctx context.Context, host string) (catalogRefs, bool, error) {
	reader, ok := s.cfg.Warehouse.(catalogRefReader)
	if !ok {
		return nil, false, nil
	}
	refs, err := reader.CatalogRefs(ctx, host)
	if err != nil {
		return nil, true, err
	}
	return catalogRefsFrom(refs), true, nil
}

// groupDomainRefs records into refs the domain ids that campaigns of scope refer to.
// A server without a group store adds nothing. An error comes from reading the groups.
func (s *Server) groupDomainRefs(ctx context.Context, scope campaignMapScope, refs catalogRefs) error {
	if s.cfg.Groups == nil {
		return nil
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if groupInCampaignScope(g, scope) {
			refs.add(ktimport.KindDomains, g.DomainID)
		}
	}
	return nil
}

// raiseCatalogSeqs moves the offer, landing, network, source and domain counters past the largest
// referenced id, so a record created by hand never takes an id that tracker history already uses.
// refs are ids from log rows, the warehouse or groups. It does not return an error.
func (s *Server) raiseCatalogSeqs(refs catalogRefs) {
	seqs := map[string]*idSeq{
		ktimport.KindOffers: s.cfg.SeqOffers, ktimport.KindLandings: s.cfg.SeqLandings,
		ktimport.KindNetworks: s.cfg.SeqNetworks, ktimport.KindSources: s.cfg.SeqSources,
		ktimport.KindDomains: s.cfg.SeqDomains,
	}
	for kind, seq := range seqs {
		ids := refs.numeric(kind)
		if len(ids) == 0 {
			continue
		}
		seq.noteAll([]string{strconv.Itoa(ids[len(ids)-1])})
	}
}

// catalogIDSets lists the ids stored in each catalog the top-up fills.
// A kind whose store is not configured is absent from the result.
// An error comes from reading a store.
func (s *Server) catalogIDSets(ctx context.Context) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	var err error
	if out[ktimport.KindOffers], err = storeIDSet(ctx, s.cfg.Offers, func(v config.Offer) string { return v.ID }); err != nil {
		return nil, err
	}
	if out[ktimport.KindLandings], err = storeIDSet(ctx, s.cfg.Landings, func(v config.Landing) string { return v.ID }); err != nil {
		return nil, err
	}
	if out[ktimport.KindNetworks], err = storeIDSet(ctx, s.cfg.Networks, func(v config.AffiliateNetwork) string { return v.ID }); err != nil {
		return nil, err
	}
	if out[ktimport.KindSources], err = storeIDSet(ctx, s.cfg.Sources, func(v config.TrafficSource) string { return v.ID }); err != nil {
		return nil, err
	}
	if out[ktimport.KindDomains], err = storeIDSet(ctx, s.cfg.Domains, func(v config.Domain) string { return v.ID }); err != nil {
		return nil, err
	}
	for kind, set := range out {
		if set == nil {
			delete(out, kind)
		}
	}
	return out, nil
}

// storeIDSet returns the ids of one catalog file, or nil when the store is not configured.
// An error comes from reading the file.
func storeIDSet[T any](ctx context.Context, st *JSONStore[T], idOf func(T) string) (map[string]bool, error) {
	if st == nil || st.path == "" {
		return nil, nil
	}
	list, err := st.List(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(list))
	for _, item := range list {
		set[idOf(item)] = true
	}
	return set, nil
}

// topUpPlan picks the ids one top-up asks the tracker for.
// local are the catalog ids, manifest the earlier transfers, budget what is left of the cap.
type topUpPlan struct {
	local    map[string]map[string]bool
	manifest ktimport.Manifest
	budget   int
	want     map[string][]int
	capped   map[string][]int
}

// pick adds the ids of kind that are neither in the catalog nor in the manifest.
// An id from the manifest is a transferred record removed by hand and stays removed.
// A kind without a store is skipped. Ids over the budget go to capped.
func (p *topUpPlan) pick(kind string, ids []int) {
	set, ok := p.local[kind]
	if !ok {
		return
	}
	seen := map[int]bool{}
	for _, n := range p.want[kind] {
		seen[n] = true
	}
	for _, n := range ids {
		id := strconv.Itoa(n)
		if n <= 0 || seen[n] || set[id] || p.manifest.Has(kind, id) {
			continue
		}
		seen[n] = true
		if p.budget <= 0 {
			p.capped[kind] = append(p.capped[kind], n)
			continue
		}
		p.budget--
		p.want[kind] = append(p.want[kind], n)
	}
}

// report compares the requested ids with the catalog after the merge.
// after are the stored ids, gone the ids the tracker answered 404 for, failed is true after a tracker error.
// It sets out.added, appends one miss line per kind and reason to out.losses, and returns the added ids per kind.
func (p *topUpPlan) report(after map[string]map[string]bool, gone ktimport.CatalogIDs, failed bool, out *catalogTopUp) map[string][]string {
	reason := "не перенесён"
	if failed {
		reason = "ошибка Keitaro"
	}
	added := map[string][]string{}
	for _, k := range topUpKinds {
		goneSet := map[int]bool{}
		for _, n := range goneOf(gone, k.kind) {
			goneSet[n] = true
		}
		var absent, rejected []int
		for _, n := range p.want[k.kind] {
			id := strconv.Itoa(n)
			switch {
			case after[k.kind][id]:
				added[k.kind] = append(added[k.kind], id)
			case goneSet[n]:
				absent = append(absent, n)
			default:
				rejected = append(rejected, n)
			}
		}
		out.added[k.kind] = len(added[k.kind])
		out.losses = appendMissLine(out.losses, k, absent, "нет в Keitaro")
		out.losses = appendMissLine(out.losses, k, rejected, reason)
		out.losses = appendMissLine(out.losses, k, p.capped[k.kind], fmt.Sprintf("лимит %d за прогон", keitaroTopUpCap))
	}
	return added
}

// topUpKeitaroCatalog adds catalog records that log rows or campaigns refer to but the catalog lacks.
// It only inserts: a stored id, from an earlier transfer or created here, is never rewritten,
// and an id listed in the manifest is skipped. Records are read one by one with GET, mapped by
// ktimport.Map and merged by mergeCatalog; new ids join the manifest as transferred records.
// creds is the tracker of this run. manifestStore is the connection manifest; nil uses the global one.
// refs are the referenced ids. The result carries counts and report lines; failures are reported
// there and never stop the caller.
func (s *Server) topUpKeitaroCatalog(ctx context.Context, creds ktCreds, manifestStore ManifestStore, refs catalogRefs) catalogTopUp {
	out := catalogTopUp{added: map[string]int{}, complete: true}
	if len(refs) == 0 {
		return out
	}
	s.raiseCatalogSeqs(refs)
	fail := func(detail string) catalogTopUp {
		out.complete, out.failed = false, true
		out.losses = append(out.losses, ktimport.Loss{Kind: "skipped", Ref: catalogTopUpRef, Detail: detail})
		return out
	}
	if manifestStore == nil {
		manifestStore = s.cfg.Manifest
	}
	manifest, err := loadManifestStore(manifestStore)
	if err != nil {
		return fail("дозагрузка каталога: манифест не прочитан")
	}
	local, err := s.catalogIDSets(ctx)
	if err != nil {
		return fail("дозагрузка каталога: каталог не прочитан")
	}
	plan := &topUpPlan{local: local, manifest: manifest, budget: topUpBudget(creds), want: map[string][]int{}, capped: map[string][]int{}}
	plan.pick(ktimport.KindOffers, refs.numeric(ktimport.KindOffers))
	plan.pick(ktimport.KindLandings, refs.numeric(ktimport.KindLandings))
	plan.pick(ktimport.KindSources, refs.numeric(ktimport.KindSources))
	plan.pick(ktimport.KindDomains, refs.numeric(ktimport.KindDomains))
	client := s.keitaroClient(creds)
	snap, gone, fetchErr := client.FetchCatalogByID(ctx, ktimport.CatalogIDs{
		Offers: plan.want[ktimport.KindOffers], Landings: plan.want[ktimport.KindLandings],
		Sources: plan.want[ktimport.KindSources], Domains: plan.want[ktimport.KindDomains],
	})
	if fetchErr == nil {
		nets := refs.numeric(ktimport.KindNetworks)
		for _, o := range snap.Offers {
			nets = append(nets, int(o.AffiliateNetworkID))
		}
		sort.Ints(nets)
		plan.pick(ktimport.KindNetworks, nets)
		netSnap, netGone, err := client.FetchCatalogByID(ctx, ktimport.CatalogIDs{Networks: plan.want[ktimport.KindNetworks]})
		snap.AffiliateNetworks, gone.Networks, fetchErr = netSnap.AffiliateNetworks, netGone.Networks, err
	}
	if fetchErr != nil {
		out.complete, out.failed = false, true
		out.losses = append(out.losses, ktimport.Loss{Kind: "skipped", Ref: catalogTopUpRef, Detail: "дозагрузка каталога: " + sanitize(fetchErr, creds.Key)})
	}
	if len(plan.capped) > 0 {
		out.complete = false
	}
	if err := s.mergeTopUp(ctx, ktimport.Map(snap, ktimport.Selection{Offers: true, Landings: true, Networks: true, Sources: true, Domains: true}), &out); err != nil {
		return fail("дозагрузка каталога: каталог не сохранён")
	}
	after, err := s.catalogIDSets(ctx)
	if err != nil {
		return fail("дозагрузка каталога: каталог не прочитан")
	}
	added := plan.report(after, gone, fetchErr != nil, &out)
	if out.total() == 0 {
		return out
	}
	if err := saveTopUpManifest(manifestStore, added); err != nil {
		return fail("дозагрузка каталога: манифест не сохранён")
	}
	s.cfg.SeqNetworks.noteAll(added[ktimport.KindNetworks])
	s.logInfo("keitaro catalog top-up", "offers", out.added[ktimport.KindOffers], "landings", out.added[ktimport.KindLandings],
		"networks", out.added[ktimport.KindNetworks], "sources", out.added[ktimport.KindSources], "domains", out.added[ktimport.KindDomains])
	return out
}

// topUpFolderGETs is how many folder lists one top-up reads besides the records.
const topUpFolderGETs = 3

// topUpBudget returns how many records one top-up may ask for: keitaroTopUpCap, or what the
// GET budget of creds still allows after the folder lists. It is never negative.
func topUpBudget(creds ktCreds) int {
	if creds.gets == nil {
		return keitaroTopUpCap
	}
	return max(0, min(keitaroTopUpCap, creds.gets.left()-topUpFolderGETs))
}

// mergeTopUp merges mapped records into the catalogs without replacing any stored id.
// res is the mapper output. Mapper and validation lines are appended to out.losses.
// An error means a catalog could not be read or saved.
func (s *Server) mergeTopUp(ctx context.Context, res ktimport.Result, out *catalogTopUp) error {
	none := func(string) bool { return false }
	out.losses = append(out.losses, res.Losses...)
	steps := []func() (int, []ktimport.Loss, error){
		func() (int, []ktimport.Loss, error) {
			return mergeTopUpKind(ctx, res.Offers, catalogMerge[config.Offer]{store: s.cfg.Offers, idOf: func(v config.Offer) string { return v.ID }, valid: validOffer, imported: none})
		},
		func() (int, []ktimport.Loss, error) {
			return mergeTopUpKind(ctx, res.Landings, catalogMerge[config.Landing]{store: s.cfg.Landings, idOf: func(v config.Landing) string { return v.ID }, valid: validLanding, imported: none})
		},
		func() (int, []ktimport.Loss, error) {
			return mergeTopUpKind(ctx, res.Networks, catalogMerge[config.AffiliateNetwork]{store: s.cfg.Networks, idOf: func(v config.AffiliateNetwork) string { return v.ID }, valid: validNetwork, imported: none})
		},
		func() (int, []ktimport.Loss, error) {
			return mergeTopUpKind(ctx, res.Sources, catalogMerge[config.TrafficSource]{store: s.cfg.Sources, idOf: func(v config.TrafficSource) string { return v.ID }, valid: validSource, imported: none})
		},
		func() (int, []ktimport.Loss, error) {
			s.domainMu.Lock()
			defer s.domainMu.Unlock()
			return mergeTopUpKind(ctx, res.Domains, catalogMerge[config.Domain]{store: s.cfg.Domains, idOf: func(v config.Domain) string { return v.ID }, valid: validDomain, imported: none})
		},
	}
	for _, step := range steps {
		_, extra, err := step()
		out.losses = append(out.losses, extra...)
		if err != nil {
			return err
		}
	}
	return nil
}

// mergeTopUpKind runs mergeCatalog for one kind when there is something to add.
// An empty incoming list leaves the file untouched.
func mergeTopUpKind[B any, T any](ctx context.Context, incoming []B, cm catalogMerge[T]) (int, []ktimport.Loss, error) {
	if len(incoming) == 0 {
		return 0, nil, nil
	}
	return mergeCatalog(ctx, incoming, cm)
}

// saveTopUpManifest adds the new ids to the manifest stored now; host and other kinds stay.
// An error comes from reading or writing the manifest.
func saveTopUpManifest(manifestStore ManifestStore, added map[string][]string) error {
	if manifestStore == nil {
		return nil
	}
	m, err := manifestStore.Load()
	if err != nil {
		return err
	}
	for kind, ids := range added {
		m.Add(kind, ids...)
	}
	return manifestStore.Save(m)
}

// goneOf returns the ids of kind the tracker answered 404 for.
func goneOf(gone ktimport.CatalogIDs, kind string) []int {
	switch kind {
	case ktimport.KindOffers:
		return gone.Offers
	case ktimport.KindLandings:
		return gone.Landings
	case ktimport.KindNetworks:
		return gone.Networks
	case ktimport.KindSources:
		return gone.Sources
	case ktimport.KindDomains:
		return gone.Domains
	default:
		return nil
	}
}

// appendMissLine appends one «оффер X не найден в каталоге» line for ids, or nothing for no ids.
// k names the kind. reason goes in brackets. At most maxLogCampaignIDsInLoss ids are listed.
func appendMissLine(losses []ktimport.Loss, k topUpKind, ids []int, reason string) []ktimport.Loss {
	if len(ids) == 0 {
		return losses
	}
	detail := fmt.Sprintf("%s %d %s в каталоге (%s)", k.one, ids[0], k.missOne, reason)
	if len(ids) > 1 {
		detail = fmt.Sprintf("%s %s %s в каталоге (%s)", k.many, formatLogCampaignIDs(ids), k.missMany, reason)
	}
	return append(losses, ktimport.Loss{Kind: "skipped", Ref: catalogTopUpRef, Detail: detail})
}
