package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egerkuzma/emtds/internal/config"
	"github.com/egerkuzma/emtds/internal/store"
	"github.com/emtds/bridge/ktimport"
)

// topUpFake answers GET /{resource}/{id} with a record of that id, 404 for gone and 500 for broken.
type topUpFake struct {
	mu     sync.Mutex
	gets   []string
	gone   map[string]bool
	broken map[string]bool
	rows   string
}

func (f *topUpFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(f.rows))
			return
		}
		f.mu.Lock()
		f.gets = append(f.gets, r.URL.Path)
		f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/admin_api/v1/")
		parts := strings.Split(path, "/")
		if len(parts) != 2 {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		switch {
		case f.gone[path]:
			http.NotFound(w, r)
			return
		case f.broken[path]:
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		id, _ := strconv.Atoi(parts[1])
		switch parts[0] {
		case "offers":
			fmt.Fprintf(w, `{"id":%d,"name":"Offer %d","state":"active","action_type":"http","action_payload":"https://o.example/%d","affiliate_network_id":60}`, id, id, id)
		case "affiliate_networks":
			fmt.Fprintf(w, `{"id":%d,"name":"Net %d","state":"active"}`, id, id)
		case "domains":
			fmt.Fprintf(w, `{"id":%d,"name":"d%d.example","state":"active"}`, id, id)
		default:
			fmt.Fprintf(w, `{"id":%d,"name":"Item %d","state":"active","action_type":"http","action_payload":"https://l.example/%d"}`, id, id, id)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *topUpFake) got(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.gets {
		if p == path {
			return true
		}
	}
	return false
}

func (f *topUpFake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.gets {
		if strings.HasPrefix(p, prefix) {
			n++
		}
	}
	return n
}

func topUpServer(t *testing.T, base string, sink ImportSink) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, _ := newKeitaroServers(t, base, "fixture-key", nil, sink, &keitaroFiles{dir: dir})
	s.cfg.SeqOffers = NewIDSeq(filepath.Join(dir, "offer_seq.json"))
	s.cfg.SeqNetworks = NewIDSeq(filepath.Join(dir, "network_seq.json"))
	s.cfg.SeqLandings = NewIDSeq(filepath.Join(dir, "landing_seq.json"))
	s.cfg.SeqSources = NewIDSeq(filepath.Join(dir, "source_seq.json"))
	s.cfg.SeqDomains = NewIDSeq(filepath.Join(dir, "domain_seq.json"))
	return s, dir
}

// domainGroups returns one campaign per domain id.
func domainGroups(ids ...string) *memGroups {
	g := &memGroups{}
	for i, id := range ids {
		g.list = append(g.list, config.Group{ID: "g" + strconv.Itoa(i), Name: "G", DomainID: id})
	}
	return g
}

func lossDetails(losses []ktimport.Loss) string {
	var b strings.Builder
	for _, l := range losses {
		b.WriteString(l.Ref + ": " + l.Detail + "\n")
	}
	return b.String()
}

func TestTopUpCatalogInsertOnly(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{gone: map[string]bool{"offers/850": true}}
	srv := fake.server(t)
	s, _ := topUpServer(t, srv.URL, &fakeSink{})
	if err := s.cfg.Offers.Save(ctx, []config.Offer{{ID: "1", Name: "Mine", URL: "https://mine.example/", Status: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.Networks.Save(ctx, []config.AffiliateNetwork{{ID: "61", Name: "Local61", Status: true}}); err != nil {
		t.Fatal(err)
	}
	prev := ktimport.Manifest{Host: "tracker.example"}
	prev.Add(ktimport.KindOffers, "1", "5")
	prev.Add(ktimport.KindNetworks, "61")
	if err := s.cfg.Manifest.Save(prev); err != nil {
		t.Fatal(err)
	}
	refs := newCatalogRefs()
	refs.add(ktimport.KindOffers, "1", "5", "849", "850")
	refs.add(ktimport.KindNetworks, "61")

	top := s.topUpKeitaroCatalog(ctx, s.keitaroCreds(), nil, refs)

	if !top.complete || top.added[ktimport.KindOffers] != 1 || top.added[ktimport.KindNetworks] != 1 {
		t.Fatalf("top %+v\n%s", top, lossDetails(top.losses))
	}
	for _, p := range []string{"/admin_api/v1/offers/1", "/admin_api/v1/offers/5", "/admin_api/v1/affiliate_networks/61"} {
		if fake.got(p) {
			t.Fatalf("asked for stored or removed id %s", p)
		}
	}
	offers, _ := s.cfg.Offers.List(ctx)
	byID := map[string]config.Offer{}
	for _, o := range offers {
		byID[o.ID] = o
	}
	if len(offers) != 2 || byID["1"].Name != "Mine" || byID["849"].Name != "Offer 849" || byID["849"].NetworkID != "60" {
		t.Fatalf("offers %+v", offers)
	}
	nets, _ := s.cfg.Networks.List(ctx)
	if len(nets) != 2 || nets[0].ID != "61" || nets[0].Name != "Local61" || nets[1].ID != "60" {
		t.Fatalf("networks %+v", nets)
	}
	m, _ := s.cfg.Manifest.Load()
	if !m.Has(ktimport.KindOffers, "849") || !m.Has(ktimport.KindNetworks, "60") || m.Has(ktimport.KindOffers, "850") || m.Host != "tracker.example" {
		t.Fatalf("manifest %+v", m)
	}
	if !strings.Contains(lossDetails(top.losses), "catalog: оффер 850 не найден в каталоге (нет в Keitaro)") {
		t.Fatalf("losses\n%s", lossDetails(top.losses))
	}
	id, err := s.takeCatalogID(ctx, s.cfg.SeqOffers, s.offerFloor)
	if err != nil || id != "851" {
		t.Fatalf("next offer id %q %v", id, err)
	}
	id, err = s.takeCatalogID(ctx, s.cfg.SeqNetworks, s.networkFloor)
	if err != nil || id != "62" {
		t.Fatalf("next network id %q %v", id, err)
	}
}

func TestTopUpCatalogCap(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{}
	srv := fake.server(t)
	s, _ := topUpServer(t, srv.URL, &fakeSink{})
	refs := newCatalogRefs()
	for n := 1000; n < 1000+keitaroTopUpCap+5; n++ {
		refs.add(ktimport.KindOffers, strconv.Itoa(n))
	}
	top := s.topUpKeitaroCatalog(ctx, s.keitaroCreds(), nil, refs)
	if top.complete || top.added[ktimport.KindOffers] != keitaroTopUpCap {
		t.Fatalf("top added=%v complete=%v", top.added, top.complete)
	}
	if n := fake.count("/admin_api/v1/offers/"); n != keitaroTopUpCap {
		t.Fatalf("offer GETs %d", n)
	}
	want := fmt.Sprintf("офферы 1300,1301,1302,1303,1304 не найдены в каталоге (лимит %d за прогон)", keitaroTopUpCap)
	if !strings.Contains(lossDetails(top.losses), want) {
		t.Fatalf("losses\n%s", lossDetails(top.losses))
	}
	if fake.count("/admin_api/v1/affiliate_networks/") != 0 {
		t.Fatal("network fetched past the cap")
	}
}

func TestTopUpCatalogTrackerFailureNonFatal(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{
		broken: map[string]bool{"offers/849": true},
		rows:   `{"rows":[{"sub_id":"c1","datetime":"2026-10-01 00:00:00","campaign_id":7,"offer_id":849,"affiliate_network_id":60}],"total":1}`,
	}
	srv := fake.server(t)
	s, _ := topUpServer(t, srv.URL, &fakeSink{})
	counts, losses, _, err := s.importLogWindow(ctx, s.keitaroCreds(), "2026-10-01 00:00:00", "2026-10-01 01:00:00", logImportOpts{clicks: true, syncMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if counts["clicks"] != 1 {
		t.Fatalf("counts %+v", counts)
	}
	text := lossDetails(losses)
	if !strings.Contains(text, "catalog: дозагрузка каталога: keitaro: GET /admin_api/v1/offers/849 вернул 500") ||
		!strings.Contains(text, "оффер 849 не найден в каталоге (ошибка Keitaro)") || strings.Contains(text, "fixture-key") {
		t.Fatalf("losses\n%s", text)
	}
	offers, _ := s.cfg.Offers.List(ctx)
	if len(offers) != 0 {
		t.Fatalf("offers %+v", offers)
	}
}

func TestTopUpCatalogFromLogRows(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{
		rows: `{"rows":[{"sub_id":"c1","datetime":"2026-10-01 00:00:00","campaign_id":7,"offer_id":1127,"affiliate_network_id":74}],"total":1}`,
	}
	srv := fake.server(t)
	s, _ := topUpServer(t, srv.URL, &fakeSink{})
	counts, losses, _, err := s.importLogWindow(ctx, s.keitaroCreds(), "2026-10-01 00:00:00", "2026-10-01 01:00:00", logImportOpts{clicks: true, syncMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if counts["catalog_added"] != 3 {
		t.Fatalf("counts %+v\n%s", counts, lossDetails(losses))
	}
	nets, _ := s.cfg.Networks.List(ctx)
	if len(nets) != 2 {
		t.Fatalf("networks %+v", nets)
	}
	notes := &syncCatalogNotes{}
	notes.collect(counts["catalog_added"], losses)
	if got := notes.suffix(); got != ", каталог +3" {
		t.Fatalf("suffix %q", got)
	}
}

// refsSink is a warehouse that also lists the catalog ids its rows refer to.
type refsSink struct {
	fakeSink
	refs  store.CatalogRefs
	calls int
}

func (r *refsSink) CatalogRefs(context.Context, string) (store.CatalogRefs, error) {
	r.calls++
	return r.refs, nil
}

func TestKeitaroSyncCatalogCheckOnce(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{gone: map[string]bool{"offers/900": true}}
	srv := fake.server(t)
	sink := &refsSink{refs: store.CatalogRefs{Offers: []string{"849", "900"}, Networks: []string{"75"}}}
	s, _ := topUpServer(t, srv.URL, sink)
	target := syncTarget(s, s.keitaroCreds())
	st := &keitaroSyncFile{}
	notes := &syncCatalogNotes{}
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	if !st.CatalogChecked || notes.added != 3 {
		t.Fatalf("checked=%v notes=%+v", st.CatalogChecked, notes)
	}
	if !strings.Contains(notes.suffix(), "оффер 900 не найден в каталоге (нет в Keitaro)") {
		t.Fatalf("suffix %q", notes.suffix())
	}
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	if sink.calls != 1 {
		t.Fatalf("refs read %d times", sink.calls)
	}
}

func TestKeitaroSyncDomainCheckInsertOnly(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{gone: map[string]bool{"domains/52": true}}
	srv := fake.server(t)
	s, _ := topUpServer(t, srv.URL, &fakeSink{})
	s.cfg.Groups = domainGroups("51", "52", "3", "7", "", "51")
	mine := config.Domain{ID: "3", Host: "mine.example", Group: "Park", Role: "links", Disabled: true, OK: true, Reason: "dns", Checked: "2026-11-01T00:00:00Z", Registrar: "namecheap"}
	if err := s.cfg.Domains.Save(ctx, []config.Domain{mine}); err != nil {
		t.Fatal(err)
	}
	prev := ktimport.Manifest{Host: "tracker.example"}
	prev.Add(ktimport.KindDomains, "3", "7")
	if err := s.cfg.Manifest.Save(prev); err != nil {
		t.Fatal(err)
	}
	target := syncTarget(s, s.keitaroCreds())
	st := &keitaroSyncFile{CatalogChecked: true}
	notes := &syncCatalogNotes{}

	s.keitaroSyncCatalogCheck(ctx, target, st, notes)

	if !st.DomainsChecked || !st.CatalogChecked || notes.added != 1 {
		t.Fatalf("st %+v notes %+v", st, notes)
	}
	for _, p := range []string{"/admin_api/v1/domains/3", "/admin_api/v1/domains/7", "/admin_api/v1/domains"} {
		if fake.got(p) {
			t.Fatalf("asked for %s", p)
		}
	}
	if fake.count("/admin_api/v1/domains/51") != 1 {
		t.Fatalf("gets %v", fake.gets)
	}
	doms, _ := s.cfg.Domains.List(ctx)
	if len(doms) != 2 || !reflect.DeepEqual(doms[0], mine) {
		t.Fatalf("domains %+v", doms)
	}
	if d := doms[1]; d.ID != "51" || d.Host != "d51.example" || d.Role != "links" || d.Disabled || d.OK || d.Checked != "" {
		t.Fatalf("new domain %+v", d)
	}
	m, _ := s.cfg.Manifest.Load()
	if !m.Has(ktimport.KindDomains, "51") || m.Has(ktimport.KindDomains, "52") {
		t.Fatalf("manifest %+v", m)
	}
	if !strings.Contains(notes.suffix(), "домен 52 не найден в каталоге (нет в Keitaro)") {
		t.Fatalf("suffix %q", notes.suffix())
	}
	before := fake.count("/admin_api/v1/")
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	if fake.count("/admin_api/v1/") != before {
		t.Fatal("checked flag ignored")
	}
}

func TestTopUpDomainSeqAboveRefs(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{gone: map[string]bool{"domains/90": true}}
	srv := fake.server(t)
	s, _ := topUpServer(t, srv.URL, &fakeSink{})
	refs := newCatalogRefs()
	refs.add(ktimport.KindDomains, "12", "90")
	top := s.topUpKeitaroCatalog(ctx, s.keitaroCreds(), nil, refs)
	if !top.complete || top.added[ktimport.KindDomains] != 1 {
		t.Fatalf("top %+v\n%s", top, lossDetails(top.losses))
	}
	id, err := s.takeCatalogID(ctx, s.cfg.SeqDomains, s.domainFloor)
	if err != nil || id != "91" {
		t.Fatalf("next domain id %q %v", id, err)
	}
}

func TestKeitaroSyncDomainCheckCap(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{}
	srv := fake.server(t)
	var offers []string
	for n := 1000; n < 1000+keitaroTopUpCap; n++ {
		offers = append(offers, strconv.Itoa(n))
	}
	s, _ := topUpServer(t, srv.URL, &refsSink{refs: store.CatalogRefs{Offers: offers}})
	s.cfg.Groups = domainGroups("51")
	st := &keitaroSyncFile{}
	notes := &syncCatalogNotes{}
	s.keitaroSyncCatalogCheck(ctx, syncTarget(s, s.keitaroCreds()), st, notes)
	if st.CatalogChecked || st.DomainsChecked || fake.count("/admin_api/v1/domains/") != 0 {
		t.Fatalf("st %+v domain gets %d", st, fake.count("/admin_api/v1/domains/"))
	}
	want := fmt.Sprintf("домен 51 не найден в каталоге (лимит %d за прогон)", keitaroTopUpCap)
	if !strings.Contains(strings.Join(notes.lines, "\n"), want) {
		t.Fatalf("lines %v", notes.lines)
	}
}

func TestKeitaroSyncDomainCheckTrackerFailure(t *testing.T) {
	ctx := context.Background()
	fake := &topUpFake{broken: map[string]bool{"domains/51": true}}
	srv := fake.server(t)
	s, _ := topUpServer(t, srv.URL, &fakeSink{})
	s.cfg.Groups = domainGroups("51")
	st := &keitaroSyncFile{CatalogChecked: true}
	notes := &syncCatalogNotes{}
	s.keitaroSyncCatalogCheck(ctx, syncTarget(s, s.keitaroCreds()), st, notes)
	text := strings.Join(notes.lines, "\n")
	if st.DomainsChecked || !st.CatalogChecked || strings.Contains(text, "fixture-key") ||
		!strings.Contains(text, "GET /admin_api/v1/domains/51 вернул 500") ||
		!strings.Contains(text, "домен 51 не найден в каталоге (ошибка Keitaro)") {
		t.Fatalf("st %+v lines\n%s", st, text)
	}
	if doms, _ := s.cfg.Domains.List(ctx); len(doms) != 0 {
		t.Fatalf("domains %+v", doms)
	}
}

// failingRefsSink is a warehouse whose catalog id read always fails.
type failingRefsSink struct {
	fakeSink
	calls int
}

func (r *failingRefsSink) CatalogRefs(context.Context, string) (store.CatalogRefs, error) {
	r.calls++
	return store.CatalogRefs{}, fmt.Errorf("clickhouse down")
}

func TestKeitaroSyncCatalogCheckRetryPause(t *testing.T) {
	ctx := context.Background()
	srv := (&topUpFake{}).server(t)
	sink := &failingRefsSink{}
	s, _ := topUpServer(t, srv.URL, sink)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s.syncNow = func() time.Time { return now }
	target := syncTarget(s, s.keitaroCreds())
	st := &keitaroSyncFile{}
	notes := &syncCatalogNotes{}

	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	if sink.calls != 1 || st.CatalogChecked {
		t.Fatalf("calls=%d checked=%v", sink.calls, st.CatalogChecked)
	}
	now = now.Add(keitaroCatalogRetry - time.Minute)
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	if sink.calls != 1 {
		t.Fatalf("retried inside the pause: %d", sink.calls)
	}
	now = now.Add(2 * time.Minute)
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	if sink.calls != 2 {
		t.Fatalf("not retried after the pause: %d", sink.calls)
	}
}

func TestKeitaroSyncCatalogCheckCapCutNotPaused(t *testing.T) {
	ctx := context.Background()
	srv := (&topUpFake{}).server(t)
	var offers []string
	for n := 1000; n < 1000+keitaroTopUpCap+1; n++ {
		offers = append(offers, strconv.Itoa(n))
	}
	sink := &refsSink{refs: store.CatalogRefs{Offers: offers}}
	s, _ := topUpServer(t, srv.URL, sink)
	s.syncNow = func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }
	target := syncTarget(s, s.keitaroCreds())
	st := &keitaroSyncFile{}
	notes := &syncCatalogNotes{}
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	if st.CatalogChecked {
		t.Fatal("a cap cut must leave the catalog unchecked")
	}
	s.keitaroSyncCatalogCheck(ctx, target, st, notes)
	if sink.calls != 2 || !st.CatalogChecked {
		t.Fatalf("calls=%d checked=%v: the pass after a cap cut must run at once and finish the rest", sink.calls, st.CatalogChecked)
	}
}

func TestMergeCatalogWaitsForStoreLock(t *testing.T) {
	ctx := context.Background()
	s, _ := topUpServer(t, "http://tracker.invalid", &fakeSink{})
	unlock := s.cfg.Offers.lock()
	done := make(chan error, 1)
	go func() {
		_, _, err := mergeCatalog(ctx, []config.Offer{{ID: "m1", Name: "M", URL: "https://m.example/"}}, catalogMerge[config.Offer]{
			store: s.cfg.Offers, idOf: func(v config.Offer) string { return v.ID }, valid: validOffer, imported: func(string) bool { return false },
		})
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("mergeCatalog finished while the store lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mergeCatalog did not finish after the lock was released")
	}
}

func TestCatalogCRUDAndMergeKeepEachOther(t *testing.T) {
	ctx := context.Background()
	s, adm := newKeitaroServers(t, "http://tracker.invalid", "k", nil, &fakeSink{}, &keitaroFiles{dir: t.TempDir()})
	const n = 15
	seed := make([]config.Offer, n)
	for i := range seed {
		seed[i] = config.Offer{ID: "e" + strconv.Itoa(i), Name: "seed", URL: "https://seed.example/", Status: true}
	}
	if err := s.cfg.Offers.Save(ctx, seed); err != nil {
		t.Fatal(err)
	}
	c := newClient(t)
	_, csrf := login(t, c, adm.URL, "admin", "p@ss")
	none := func(string) bool { return false }

	var wg sync.WaitGroup
	errc := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			id := "e" + strconv.Itoa(i)
			code, body, err := doJSON(c, http.MethodPut, adm.URL+"/api/offers/"+id, csrf, config.Offer{ID: id, Name: "edited", URL: "https://seed.example/", Status: true})
			if err == nil && code != http.StatusOK {
				err = fmt.Errorf("put %s: %d %s", id, code, body)
			}
			errc <- err
		}()
		go func() {
			defer wg.Done()
			_, _, err := mergeCatalog(ctx, []config.Offer{{ID: "m" + strconv.Itoa(i), Name: "merged", URL: "https://m.example/", Status: true}}, catalogMerge[config.Offer]{
				store: s.cfg.Offers, idOf: func(v config.Offer) string { return v.ID }, valid: validOffer, imported: none,
			})
			errc <- err
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.cfg.Offers.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2*n {
		t.Fatalf("offers %d want %d", len(list), 2*n)
	}
	for _, o := range list {
		if strings.HasPrefix(o.ID, "e") && o.Name != "edited" {
			t.Fatalf("edit of %s lost: %q", o.ID, o.Name)
		}
	}
}
