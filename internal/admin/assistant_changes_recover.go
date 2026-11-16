package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// aiBlobPath returns the path of a snapshot file under the assistant directory.
// name is the file name kept in the change; a name that is not a plain file name gives "".
func (s *Server) aiBlobPath(name string) string {
	if s.cfg.AIDir == "" || name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return ""
	}
	return filepath.Join(s.cfg.AIDir, aiBlobsDir, name)
}

// aiChgRemoveBlob deletes the snapshot file called name. A missing file or name is not an error.
func (s *Server) aiChgRemoveBlob(name string) {
	if path := s.aiBlobPath(name); path != "" {
		_ = os.Remove(path)
	}
}

// aiChgAllRaw returns every stored record of the resource as raw JSON, the encoding recordHash
// hashes. A missing store or a read failure is a 500 error; an unknown resource is 400.
func (s *Server) aiChgAllRaw(ctx context.Context, resource string) ([]json.RawMessage, error) {
	switch resource {
	case "offers":
		return aiAllRaw(ctx, s.cfg.Offers)
	case "landings":
		return aiAllRaw(ctx, s.cfg.Landings)
	case "affiliate_networks":
		return aiAllRaw(ctx, s.cfg.Networks)
	case "traffic_sources":
		return aiAllRaw(ctx, s.cfg.Sources)
	case "campaigns":
		if s.cfg.Groups == nil {
			return nil, aiPlainError(http.StatusInternalServerError, "read error")
		}
		list, err := s.cfg.Groups.List(ctx)
		if err != nil {
			return nil, aiPlainError(http.StatusInternalServerError, "read error")
		}
		out := make([]json.RawMessage, len(list))
		for i, g := range list {
			out[i] = aiRaw(g)
		}
		return out, nil
	}
	return nil, aiBadPatch("unknown resource")
}

// aiAllRaw returns the records of st as raw JSON. A store that cannot be read is a 500 error.
func aiAllRaw[T any](ctx context.Context, st *JSONStore[T]) ([]json.RawMessage, error) {
	if st == nil {
		return nil, aiPlainError(http.StatusInternalServerError, "read error")
	}
	list, err := st.List(ctx)
	if err != nil {
		return nil, aiPlainError(http.StatusInternalServerError, "read error")
	}
	out := make([]json.RawMessage, len(list))
	for i, item := range list {
		out[i] = aiRaw(item)
	}
	return out, nil
}

// aiChgIsZero reports whether a decoded JSON value is the zero value a store omits: null, false,
// an empty string, zero, an empty list or object.
func aiChgIsZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case json.Number:
		f, err := x.Float64()
		return err == nil && f == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// aiChgFindCreated finds the record a create of a catalog wrote when the entry never learned its
// id (the process stopped before the write was confirmed). It returns the id and the raw record
// of the single stored record whose fields equal the fields of the stored patch, a field the
// store omits counting as its zero value, and whose id no other entry owns (taken, keyed
// "resource/id"). With none or several candidates it returns false.
func (s *Server) aiChgFindCreated(ctx context.Context, r aiChangeRec, taken map[string]bool) (string, json.RawMessage, bool) {
	patch, err := aiChgObject(r.Proposal.Patch)
	if err != nil {
		return "", nil, false
	}
	all, err := s.aiChgAllRaw(ctx, r.Change.Resource)
	if err != nil {
		return "", nil, false
	}
	var foundID string
	var foundRaw json.RawMessage
	for _, raw := range all {
		obj, err := aiChgObject(raw)
		if err != nil {
			continue
		}
		id, _ := obj["id"].(string)
		if id == "" || taken[r.Change.Resource+"/"+id] {
			continue
		}
		same := true
		for k, want := range patch {
			got, has := obj[k]
			if (!has && !aiChgIsZero(want)) || (has && aiChgCanon(got) != aiChgCanon(want)) {
				same = false
				break
			}
		}
		if !same {
			continue
		}
		if foundID != "" {
			return "", nil, false
		}
		foundID, foundRaw = id, raw
	}
	return foundID, foundRaw, foundID != ""
}

// aiChgSettle decides what became of an entry that was pending (an apply) or reverting (a revert)
// when the process stopped, by what the store holds now. r is a copy of the entry and taken the
// ids owned by entries ("resource/id"). It returns the settled copy, the snapshot files to remove
// once the copy is saved, and whether the entry changed; a store that cannot be read leaves it
// unchanged.
//
// Pending: a delete is done when the record is gone. A create is done when the record with the id
// exists, or, for a catalog create that never learned its id, when aiChgFindCreated finds it. An
// update is done when the stored hash is the recorded one, or, with none recorded, differs from
// the hash before. Anything else is aborted, with the reason in Error, and loses its copy and
// snapshot. Reverting: an update is reverted when the stored hash is the one before, a create when
// the record is gone, a delete when the record exists; otherwise the entry is done again (a
// revert is then refused as a conflict if the record moved on). A delete that stays done because
// the landing record is gone has the files of a half-finished restore removed.
func (s *Server) aiChgSettle(ctx context.Context, r aiChangeRec, taken map[string]bool) (aiChangeRec, []string, bool) {
	c := &r.Change
	raw, exists, err := s.aiChgStored(ctx, c.Resource, c.EntityID)
	if c.EntityID == "" {
		exists, err = false, nil
	}
	if err != nil {
		return r, nil, false
	}
	var drop []string
	markDone := func() {
		c.Status, c.Error = aiChangeDone, ""
		if c.Applied.IsZero() {
			c.Applied = time.Now().UTC()
		}
	}
	if c.Status == aiChangeReverting {
		reverted := false
		switch c.Op {
		case aiOpUpdate:
			reverted = exists && rawHash(raw) == rawHash(c.Before)
		case aiOpCreate:
			reverted = !exists
		case aiOpDelete:
			reverted = exists
			if !exists && c.Resource == "landings" && c.Blob != "" {
				s.removeLandingFiles(c.EntityID)
			}
		}
		if reverted {
			c.Status, c.Reverted = aiChangeReverted, time.Now().UTC()
			drop = append(drop, c.Blob)
		} else {
			c.Status = aiChangeDone
		}
		return r, drop, true
	}
	switch {
	case c.Op == aiOpDelete && !exists:
		markDone()
	case c.Op == aiOpCreate && c.EntityID == "":
		id, found, ok := s.aiChgFindCreated(ctx, r, taken)
		if !ok {
			return s.aiChgAborted(r), []string{c.Blob}, true
		}
		c.EntityID, r.Proposal.EntityID, c.AfterHash = id, id, rawHash(found)
		markDone()
	case c.Op == aiOpCreate && exists:
		c.AfterHash = rawHash(raw)
		markDone()
	case c.Op == aiOpUpdate && exists && c.AfterHash != "" && rawHash(raw) == c.AfterHash:
		markDone()
	case c.Op == aiOpUpdate && exists && c.AfterHash == "" && rawHash(raw) != rawHash(c.Before):
		c.AfterHash = rawHash(raw)
		markDone()
	default:
		return s.aiChgAborted(r), []string{c.Blob}, true
	}
	return r, drop, true
}

// aiChgAborted returns r as an aborted entry: the reason in Error, no copy of the record and no
// snapshot name.
func (s *Server) aiChgAborted(r aiChangeRec) aiChangeRec {
	r.Change.Status = aiChangeAborted
	r.Change.Error = "the write was interrupted and did not complete"
	r.Change.Before = nil
	r.Change.Blob = ""
	return r
}

// aiRecoverChanges settles every change that was pending or reverting when the last process
// stopped, as aiChgSettle decides, and logs how many ended in each state. It does nothing without
// an assistant directory. The entries are read first and settled without any lock on the change
// file (the stores are read meanwhile); the settled entries are then written under the file lock,
// and only over an entry that is still in the state it was read in. A file that cannot be read is
// logged and left alone. New runs it once at startup, before any request.
func (s *Server) aiRecoverChanges() {
	if s.cfg.AIDir == "" {
		return
	}
	s.ai.applyMu.Lock()
	defer s.ai.applyMu.Unlock()
	recs, err := s.aiChgRead()
	if err != nil {
		s.logWarn("assistant changes not recovered")
		return
	}
	taken := map[string]bool{}
	for _, r := range recs {
		if r.Change.EntityID != "" {
			taken[r.Change.Resource+"/"+r.Change.EntityID] = true
		}
	}
	type outcome struct {
		from string
		rec  aiChangeRec
	}
	settled := map[string]outcome{}
	var drop []string
	for _, r := range recs {
		if r.Change.Status != aiChangePending && r.Change.Status != aiChangeReverting {
			continue
		}
		if nr, blobs, changed := s.aiChgSettle(context.Background(), r, taken); changed {
			settled[r.Proposal.ID] = outcome{from: r.Change.Status, rec: nr}
			drop = append(drop, blobs...)
		}
	}
	if len(settled) == 0 {
		return
	}
	counts := map[string]int{}
	err = s.aiChgMutate(func(list *[]aiChangeRec) (bool, error) {
		changed := false
		for i := range *list {
			out, ok := settled[(*list)[i].Proposal.ID]
			if !ok || (*list)[i].Change.Status != out.from {
				continue
			}
			(*list)[i] = out.rec
			counts[out.rec.Change.Status]++
			changed = true
		}
		return changed, nil
	})
	if err != nil {
		s.logWarn("assistant changes not recovered")
		return
	}
	for _, name := range drop {
		s.aiChgRemoveBlob(name)
	}
	s.logInfo("assistant changes recovered", "done", counts[aiChangeDone], "aborted", counts[aiChangeAborted], "reverted", counts[aiChangeReverted])
}

// aiPruneChanges removes the entries created more than 90 days before now, with their snapshots,
// and every snapshot that no entry names. It never touches a pending entry. now is the current
// time. It returns the number of entries removed, or the error of the change file.
func (s *Server) aiPruneChanges(now time.Time) (int, error) {
	if s.cfg.AIDir == "" {
		return 0, nil
	}
	s.ai.applyMu.Lock()
	defer s.ai.applyMu.Unlock()
	removed := 0
	cutoff := now.Add(-aiChangesKeep)
	live := map[string]bool{}
	err := s.aiChgMutate(func(recs *[]aiChangeRec) (bool, error) {
		kept := (*recs)[:0]
		for _, r := range *recs {
			if r.Change.Status != aiChangePending && r.Proposal.Created.Before(cutoff) {
				s.aiChgRemoveBlob(r.Change.Blob)
				removed++
				continue
			}
			if r.Change.Blob != "" {
				live[r.Change.Blob] = true
			}
			kept = append(kept, r)
		}
		*recs = kept
		return removed > 0, nil
	})
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(filepath.Join(s.cfg.AIDir, aiBlobsDir))
	if err != nil {
		return removed, nil
	}
	for _, e := range entries {
		if !e.IsDir() && !live[e.Name()] {
			s.aiChgRemoveBlob(e.Name())
		}
	}
	return removed, nil
}
