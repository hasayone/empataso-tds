package admin

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kuztds/bridge/ktimport"
)

// importLandingFiles downloads each local landing archive and stores its files.
// res is the mapper output. The catalog row Entry is set from the unpacked directory when that row exists.
// It returns how many archives were stored and one skipped loss per archive that could not be fetched or unpacked.
// A catalog save failure is returned. One landing's failure does not stop the rest.
func (s *Server) importLandingFiles(ctx context.Context, res ktimport.Result) (int, []ktimport.Loss, error) {
	ids := res.LocalLandingIDs()
	if len(ids) == 0 {
		return 0, nil, nil
	}
	if s.cfg.LandingFiles == nil {
		losses := make([]ktimport.Loss, 0, len(ids))
		for _, id := range ids {
			losses = append(losses, ktimport.Loss{
				Kind:   "skipped",
				Ref:    fmt.Sprintf("landing %d", id),
				Detail: "архив не скачан: landing files unavailable",
			})
		}
		return 0, losses, nil
	}
	var losses []ktimport.Loss
	entries := map[string]string{}
	n := 0
	for _, id := range ids {
		body, err := s.keitaroClient().FetchLandingArchive(ctx, id)
		if err != nil {
			losses = append(losses, landingArchiveLoss(id, s.cfg.KeitaroKey, err))
			continue
		}
		sid := strconv.Itoa(id)
		if _, err := s.cfg.LandingFiles.Unzip(sid, body); err != nil {
			losses = append(losses, landingArchiveLoss(id, s.cfg.KeitaroKey, err))
			continue
		}
		entries[sid] = s.cfg.LandingFiles.Entry(sid)
		n++
	}
	if err := s.applyLandingEntries(ctx, entries); err != nil {
		return n, losses, err
	}
	return n, losses, nil
}

// landingArchiveLoss builds the skipped row for one landing archive.
// id is the Keitaro landing id. key is removed from the error text. err is the failure.
// It returns the loss. It does not return an error.
func landingArchiveLoss(id int, key string, err error) ktimport.Loss {
	return ktimport.Loss{
		Kind:   "skipped",
		Ref:    fmt.Sprintf("landing %d", id),
		Detail: "архив не скачан: " + sanitize(err, key),
	}
}

// applyLandingEntries writes detected entry paths onto catalog rows.
// entries maps a landing id to the relative file name. Rows that are not in the map stay as they are.
// It returns nil when there is nothing to save. It returns an error when the catalog cannot be read or saved.
func (s *Server) applyLandingEntries(ctx context.Context, entries map[string]string) error {
	if len(entries) == 0 || s.cfg.Landings == nil {
		return nil
	}
	list, err := s.cfg.Landings.List(ctx)
	if err != nil {
		return err
	}
	changed := false
	for i := range list {
		entry, ok := entries[list[i].ID]
		if !ok || list[i].Entry == entry {
			continue
		}
		list[i].Entry = entry
		changed = true
	}
	if !changed {
		return nil
	}
	return s.cfg.Landings.Save(ctx, list)
}
