package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

// campaignScope returns Scope conditions limiting stats to campaigns of the current section
// that the viewer may see. List errors propagate to the caller.
func (s *Server) campaignScope(ctx context.Context) ([]store.Cond, error) {
	v := s.viewerFrom(ctx)
	board := v.boardCurrent
	if s.cfg.Groups == nil {
		if v.admin {
			return nil, nil
		}
		switch v.mode(resCampaigns) {
		case security.ModeFull, security.ModeRead:
			return nil, nil
		default:
			return []store.Cond{{
				Field: "campaign_id", Op: "in", Values: []string{store.ScopeNone}, Scope: true,
			}}, nil
		}
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return nil, err
	}
	var own []string
	var foreign []string
	for _, g := range groups {
		if !groupInBoard(g, board) {
			continue
		}
		if !v.canSee(resCampaigns, campaignRef(g)) {
			continue
		}
		own = append(own, g.ID)
	}
	for _, g := range groups {
		inBoard := groupInBoard(g, board)
		visible := v.canSee(resCampaigns, campaignRef(g))
		if inBoard && visible {
			continue
		}
		foreign = append(foreign, g.ID)
	}
	if len(own) == 0 {
		return []store.Cond{{
			Field: "campaign_id", Op: "in", Values: []string{store.ScopeNone}, Scope: true,
		}}, nil
	}
	if len(foreign) > 0 && len(foreign) < len(own) {
		return []store.Cond{{
			Field: "campaign_id", Op: "nin", Values: foreign, Scope: true,
		}}, nil
	}
	return []store.Cond{{
		Field: "campaign_id", Op: "in", Values: own, Scope: true,
	}}, nil
}

// condsFrom parses f=, rejects filters on hidden fields, and appends campaign scope.
// w receives 400 on a bad filter or 500 when groups cannot be listed.
// The bool is false after an error response has been written.
func (s *Server) condsFrom(w http.ResponseWriter, r *http.Request, table string) ([]store.Cond, bool) {
	conds, err := parseConds(r.URL.Query())
	if err != nil {
		writeBadFilter(w, err)
		return nil, false
	}
	v := s.viewerFrom(r.Context())
	for _, c := range conds {
		if fieldHidden(v, c.Field) {
			writeErr(w, http.StatusBadRequest, "bad filter: hidden field "+c.Field)
			return nil, false
		}
	}
	if err := store.ValidateConds(table, conds); err != nil {
		writeBadFilter(w, err)
		return nil, false
	}
	scope, err := s.campaignScope(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return nil, false
	}
	return append(conds, scope...), true
}

// writeStats writes a stats JSON body without the keys of fields hidden for the viewer.
func (s *Server) writeStats(w http.ResponseWriter, r *http.Request, code int, v any) {
	s.writeStatsWith(w, r, code, v, statsOpts{})
}

// statsOpts tunes writeStatsWith.
type statsOpts struct {
	// extra maps a hidden id to the response keys it also covers.
	extra map[string][]string
	// keep lists field names whose keys stay in the body even when hidden, because
	// the handler has already made the rows safe for them.
	keep map[string]bool
}

// writeStatsExtra is writeStats plus extra keys to drop per hidden field.
// extra maps a hidden id to the response keys it also covers.
func (s *Server) writeStatsExtra(w http.ResponseWriter, r *http.Request, code int, v any, extra map[string][]string) {
	s.writeStatsWith(w, r, code, v, statsOpts{extra: extra})
}

// writeStatsWith writes v as JSON for the viewer of r.
//
// Keys of hidden fields (and opts.extra for hidden ids) are removed at every depth,
// except the keys of the names in opts.keep that no other hidden field carries.
// Hidden Sub IDs are blanked inside every subs array.
// A body with nothing to hide is written as is. An encode error answers 500.
func (s *Server) writeStatsWith(w http.ResponseWriter, r *http.Request, code int, v any, opts statsOpts) {
	vw := s.viewerFrom(r.Context())
	keys := statsStripKeys(vw, opts.extra)
	for name := range opts.keep {
		for _, k := range fieldJSONKeys(name) {
			delete(keys, k)
		}
	}
	for id := range vw.hidden {
		if opts.keep[id] {
			continue
		}
		for _, k := range fieldJSONKeys(id) {
			keys[k] = true
		}
	}
	subs := hiddenSubIndexes(vw)
	if len(keys) == 0 && len(subs) == 0 {
		writeJSON(w, code, v)
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode error")
		return
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		writeErr(w, http.StatusInternalServerError, "encode error")
		return
	}
	writeJSON(w, code, blankSubs(stripKeys(raw, keys), subs))
}

// statsStripKeys collects the JSON keys to remove for the viewer: the keys of every
// hidden field, plus the extra keys of each hidden id in extra.
func statsStripKeys(v viewer, extra map[string][]string) map[string]bool {
	out := map[string]bool{}
	for id := range v.hidden {
		for _, k := range fieldJSONKeys(id) {
			out[k] = true
		}
	}
	for id, keys := range extra {
		if !fieldHidden(v, id) {
			continue
		}
		for _, k := range keys {
			out[k] = true
		}
	}
	return out
}

// stripKeys deletes keys from every map inside v, in place, and returns v.
// v is a decoded JSON tree and is changed; nothing is copied.
func stripKeys(v any, keys map[string]bool) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if keys[k] {
				delete(x, k)
				continue
			}
			x[k] = stripKeys(val, keys)
		}
	case []any:
		for i, val := range x {
			x[i] = stripKeys(val, keys)
		}
	}
	return v
}

// campaignRefByID loads one campaign by id and builds its access ref.
// ok is false when the id is missing, not in the current section, or not visible. List errors are returned as-is.
func (s *Server) campaignRefByID(ctx context.Context, id string) (ref, bool, error) {
	if s.cfg.Groups == nil {
		return ref{}, false, nil
	}
	v := s.viewerFrom(ctx)
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return ref{}, false, err
	}
	for _, g := range groups {
		if g.ID != id {
			continue
		}
		if !groupInBoard(g, v.boardCurrent) {
			return ref{}, false, nil
		}
		r := campaignRef(g)
		if !v.canSee(resCampaigns, r) {
			return ref{}, false, nil
		}
		return r, true, nil
	}
	return ref{}, false, nil
}

// spendVisible reports whether v may see spend for campaign id in the current section.
func (s *Server) spendVisible(ctx context.Context, v viewer, groupID string) (bool, error) {
	if groupID == "" {
		return false, nil
	}
	r, ok, err := s.campaignRefByID(ctx, groupID)
	if err != nil || !ok {
		return false, err
	}
	return v.canSee(resCampaigns, r), nil
}

// filterSpendList returns entries the viewer may see in the current section.
func (s *Server) filterSpendList(ctx context.Context, v viewer, list []config.SpendEntry) ([]config.SpendEntry, error) {
	if s.cfg.Groups == nil {
		return []config.SpendEntry{}, nil
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]config.Group{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	out := make([]config.SpendEntry, 0, len(list))
	for _, e := range list {
		g, ok := byID[e.GroupID]
		if !ok {
			continue
		}
		if !groupInBoard(g, v.boardCurrent) {
			continue
		}
		if v.canSee(resCampaigns, campaignRef(g)) {
			out = append(out, e)
		}
	}
	return out, nil
}
