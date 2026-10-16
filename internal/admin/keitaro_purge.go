package admin

import (
	"context"
	"net/http"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/kuztds/bridge/ktimport"
)

// keitaroRemoved is the body of POST /api/keitaro/purge.
// Each field is how many imported records were removed. Events and conversions
// are the warehouse counts taken before the delete.
type keitaroRemoved struct {
	Groups      int   `json:"groups"`
	Offers      int   `json:"offers"`
	Landings    int   `json:"landings"`
	Networks    int   `json:"networks"`
	Sources     int   `json:"sources"`
	Domains     int   `json:"domains"`
	Lists       int   `json:"lists"`
	Pages       int   `json:"pages"`
	Events      int64 `json:"events"`
	Conversions int64 `json:"conversions"`
}

// handleKeitaroPurge removes catalogs, files and warehouse rows listed in the manifest.
// An empty manifest returns zeros and does not read or write anything else.
// The warehouse host is the manifest host, or the tracker hostname when that is empty.
// Landing directories named in the manifest are removed after the warehouse.
// The manifest is then saved empty. An import in progress, or a purge that already
// holds the lock, is 409 and removes nothing. A warehouse error is 502.
func (s *Server) handleKeitaroPurge(w http.ResponseWriter, r *http.Request) {
	if msg := s.ktJob.beginPurge(); msg != "" {
		writeErr(w, http.StatusConflict, msg)
		return
	}
	defer s.ktJob.endPurge()
	manifest, err := s.loadManifest()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if manifest.Empty() {
		writeJSON(w, http.StatusOK, map[string]any{"removed": keitaroRemoved{}})
		return
	}
	ctx := r.Context()
	var removed keitaroRemoved
	n, err := s.purgeGroups(ctx, manifest.Imported(ktimport.KindGroups))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	removed.Groups = n
	if removed.Networks, err = purgeCatalog(ctx, s.cfg.Networks, func(v config.AffiliateNetwork) string { return v.ID }, manifest.Imported(ktimport.KindNetworks)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if removed.Sources, err = purgeCatalog(ctx, s.cfg.Sources, func(v config.TrafficSource) string { return v.ID }, manifest.Imported(ktimport.KindSources)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if removed.Domains, err = purgeCatalog(ctx, s.cfg.Domains, func(v config.Domain) string { return v.ID }, manifest.Imported(ktimport.KindDomains)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if removed.Landings, err = purgeCatalog(ctx, s.cfg.Landings, func(v config.Landing) string { return v.ID }, manifest.Imported(ktimport.KindLandings)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if removed.Offers, err = purgeCatalog(ctx, s.cfg.Offers, func(v config.Offer) string { return v.ID }, manifest.Imported(ktimport.KindOffers)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if removed.Lists, err = s.purgeImportedLists(manifest.Imported(ktimport.KindLists)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if removed.Pages, err = s.purgeImportedPages(manifest.Imported(ktimport.KindPages)); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	host := manifest.Host
	if host == "" {
		host = keitaroHost(s.cfg.KeitaroBase)
	}
	if s.cfg.Warehouse != nil && host != "" {
		events, conv, err := s.cfg.Warehouse.CountByDomain(ctx, host)
		if err != nil {
			s.logPurge(removed)
			writeErr(w, http.StatusBadGateway, sanitize(err, s.cfg.KeitaroKey))
			return
		}
		if err := s.cfg.Warehouse.PurgeImported(ctx, host); err != nil {
			s.logPurge(removed)
			writeErr(w, http.StatusBadGateway, sanitize(err, s.cfg.KeitaroKey))
			return
		}
		removed.Events = events
		removed.Conversions = conv
	}
	if err := s.purgeLandingDirs(manifest.Landings); err != nil {
		s.logPurge(removed)
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if s.cfg.Manifest != nil {
		if err := s.cfg.Manifest.Save(ktimport.Manifest{}); err != nil {
			s.logPurge(removed)
			writeErr(w, http.StatusInternalServerError, "save error")
			return
		}
	}
	s.logPurge(removed)
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

// purgeGroups drops groups whose id is in the manifest.
// imported is true for those ids. A nil func removes nothing.
// It saves only when the list changed. Other groups are copied unchanged.
// It returns how many groups were removed. An error means the file could not be read or saved.
func (s *Server) purgeGroups(ctx context.Context, imported func(string) bool) (int, error) {
	if s.cfg.Groups == nil {
		return 0, nil
	}
	existing, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return 0, err
	}
	bridge, err := convert[[]ktimport.Group](existing)
	if err != nil {
		return 0, err
	}
	kept := ktimport.PurgeImported(bridge, imported)
	left := map[string]int{}
	for _, g := range kept {
		left[g.ID]++
	}
	out := make([]config.Group, 0, len(existing))
	for _, g := range existing {
		if left[g.ID] == 0 {
			continue
		}
		out = append(out, g)
		left[g.ID]--
	}
	if len(out) == len(existing) {
		return 0, nil
	}
	if err := s.cfg.Groups.Save(ctx, out); err != nil {
		return 0, err
	}
	return len(existing) - len(out), nil
}

// purgeCatalog drops records whose id is in the manifest.
// store may be nil, which removes nothing. idOf reads the id.
// imported is true for ids to remove. A nil func removes nothing.
// It saves only when something was removed.
// It returns how many records were removed. An error means the file could not be read or saved.
func purgeCatalog[T any](ctx context.Context, store *JSONStore[T], idOf func(T) string, imported func(string) bool) (int, error) {
	if store == nil {
		return 0, nil
	}
	list, err := store.List(ctx)
	if err != nil {
		return 0, err
	}
	next := ktimport.PurgeByID(list, idOf, imported)
	removed := len(list) - len(next)
	if removed == 0 {
		return 0, nil
	}
	if err := store.Save(ctx, next); err != nil {
		return 0, err
	}
	return removed, nil
}

// purgeImportedLists removes .dat files named in the manifest.
// imported is true for those file names. A nil func removes nothing.
// It returns how many files were removed. A nil lists store removes nothing.
// Names errors are ignored. Delete returns an error when a file cannot be removed.
func (s *Server) purgeImportedLists(imported func(string) bool) (int, error) {
	if s.cfg.Lists == nil {
		return 0, nil
	}
	if imported == nil {
		imported = func(string) bool { return false }
	}
	names, _ := s.cfg.Lists.Names()
	n := 0
	for _, name := range names {
		if !imported(name) {
			continue
		}
		if err := s.cfg.Lists.Delete(name); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// purgeImportedPages removes HTML files named in the manifest.
// imported is true for those file names. A nil func removes nothing.
// It returns how many files were removed. A nil pages store removes nothing.
// Names errors are ignored. Delete returns an error when a file cannot be removed.
func (s *Server) purgeImportedPages(imported func(string) bool) (int, error) {
	if s.cfg.Pages == nil {
		return 0, nil
	}
	if imported == nil {
		imported = func(string) bool { return false }
	}
	names, _ := s.cfg.Pages.Names()
	n := 0
	for _, info := range names {
		if !imported(info.Name) {
			continue
		}
		if err := s.cfg.Pages.Delete(info.Name); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// purgeLandingDirs removes landing directories named in the manifest.
// ids are the landing catalog ids. A nil store removes nothing.
// It returns an error when a directory cannot be removed.
func (s *Server) purgeLandingDirs(ids []string) error {
	if s.cfg.LandingFiles == nil {
		return nil
	}
	for _, id := range ids {
		if err := s.cfg.LandingFiles.Remove(id); err != nil {
			return err
		}
	}
	return nil
}

// logPurge writes one purge line with removal counts.
// removed is the response payload. The host and the key are not logged.
func (s *Server) logPurge(removed keitaroRemoved) {
	s.logInfo("keitaro purge",
		"groups", removed.Groups,
		"offers", removed.Offers,
		"landings", removed.Landings,
		"networks", removed.Networks,
		"sources", removed.Sources,
		"domains", removed.Domains,
		"lists", removed.Lists,
		"pages", removed.Pages,
		"events", removed.Events,
		"conversions", removed.Conversions,
	)
}
