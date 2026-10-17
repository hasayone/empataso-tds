package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

// moneyFields maps a hidden metric id to journal filter field names.
var moneyFields = map[string][]string{
	"profit": {"profit"},
	"cost":   {"cost_cents", "cost_cur"},
}

// moneyKeys maps a hidden metric id to JSON keys stripped from stats responses.
var moneyKeys = map[string][]string{
	"profit": {"profit", "revenue", "payout"},
	"cost":   {"cost_cents", "cost", "currency", "spend"},
}

// hides reports whether metric is hidden for this viewer. Administrators see all metrics.
func (v viewer) hides(metric string) bool {
	if v.admin {
		return false
	}
	return v.hidden[metric]
}

// campaignScope returns a Scope condition limiting stats to campaigns the viewer may see.
// Administrators and read/full campaign grants get nil. List errors propagate to the caller.
func (s *Server) campaignScope(ctx context.Context) ([]store.Cond, error) {
	v := s.viewerFrom(ctx)
	if v.admin {
		return nil, nil
	}
	switch v.mode(resCampaigns) {
	case security.ModeFull, security.ModeRead:
		return nil, nil
	}
	if s.cfg.Groups == nil {
		return []store.Cond{{
			Field: "campaign_id", Op: "in", Values: []string{store.ScopeNone}, Scope: true,
		}}, nil
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, g := range groups {
		if v.canSee(resCampaigns, campaignRef(g)) {
			ids = append(ids, g.ID)
		}
	}
	if len(ids) == 0 {
		ids = []string{store.ScopeNone}
	}
	return []store.Cond{{
		Field: "campaign_id", Op: "in", Values: ids, Scope: true,
	}}, nil
}

// condsFrom parses f=, rejects hidden metric filters, and appends campaign scope.
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
		if hiddenFilterField(v, c.Field) {
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

// hiddenFilterField reports whether field is a money column hidden for v.
func hiddenFilterField(v viewer, field string) bool {
	for metric, fields := range moneyFields {
		for _, f := range fields {
			if f == field && v.hides(metric) {
				return true
			}
		}
	}
	return false
}

// writeStats writes a stats JSON body and strips keys for hidden money metrics.
func (s *Server) writeStats(w http.ResponseWriter, r *http.Request, code int, v any) {
	s.writeStatsExtra(w, r, code, v, nil)
}

// writeStatsExtra is writeStats plus extra top-level keys to drop per hidden metric.
func (s *Server) writeStatsExtra(w http.ResponseWriter, r *http.Request, code int, v any, extra map[string][]string) {
	keys := statsStripKeys(s.viewerFrom(r.Context()), extra)
	if len(keys) == 0 {
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
	writeJSON(w, code, stripKeys(raw, keys))
}

// statsStripKeys collects JSON keys to remove for the current viewer.
func statsStripKeys(v viewer, extra map[string][]string) map[string]bool {
	out := map[string]bool{}
	for metric, jsonKeys := range moneyKeys {
		if !v.hides(metric) {
			continue
		}
		for _, k := range jsonKeys {
			out[k] = true
		}
	}
	for metric, keys := range extra {
		if !v.hides(metric) {
			continue
		}
		for _, k := range keys {
			out[k] = true
		}
	}
	return out
}

// stripKeys returns v with keys removed recursively from maps.
func stripKeys(v any, keys map[string]bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if keys[k] {
				continue
			}
			out[k] = stripKeys(val, keys)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = stripKeys(val, keys)
		}
		return out
	default:
		return v
	}
}

// campaignRefByID loads one campaign by id and builds its access ref.
// ok is false when the id is missing. List errors are returned as-is.
func (s *Server) campaignRefByID(ctx context.Context, id string) (ref, bool, error) {
	if s.cfg.Groups == nil {
		return ref{}, false, nil
	}
	groups, err := s.cfg.Groups.List(ctx)
	if err != nil {
		return ref{}, false, err
	}
	for _, g := range groups {
		if g.ID == id {
			return campaignRef(g), true, nil
		}
	}
	return ref{}, false, nil
}

// spendVisible reports whether v may see spend for campaign id.
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

// filterSpendList returns entries the viewer may see; entries without a campaign are omitted.
func (s *Server) filterSpendList(ctx context.Context, v viewer, list []config.SpendEntry) ([]config.SpendEntry, error) {
	if v.admin {
		return list, nil
	}
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
		if v.canSee(resCampaigns, campaignRef(g)) {
			out = append(out, e)
		}
	}
	return out, nil
}
