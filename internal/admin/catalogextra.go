package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/store"
)

func (s *Server) handleDomainCheck(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Domains == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	list, err := s.cfg.Domains.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read error")
		return
	}
	id := r.PathValue("id")
	idx := -1
	for i := range list {
		if list[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	ok := s.engineAnswers(r.Context(), list[idx].Host)
	list[idx].OK = ok
	list[idx].Checked = time.Now().UTC().Format(time.RFC3339)
	if err := s.cfg.Domains.Save(r.Context(), list); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, list[idx])
}

// engineAnswers reports whether this installation's engine returned ok for host.
// The request goes to the configured engine URL with that Host. It does not ask DNS or Cloudflare.
func (s *Server) engineAnswers(ctx context.Context, host string) bool {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.EngineURL), "/")
	if base == "" || strings.TrimSpace(host) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return false
	}
	req.Host = host
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32))
	if err != nil {
		return false
	}
	return resp.StatusCode == http.StatusOK && string(body) == "ok"
}

func (s *Server) handleBotGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, loadBotProfile(s.cfg.BotPath))
}

func (s *Server) handleBotPut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.BotPath == "" {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	var profile config.Bots
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&profile); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	profile.Redirect, profile.Out, profile.Header = "", "", ""
	b, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	if err := writeFileAtomic(s.cfg.BotPath, append(b, '\n'), 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func loadBotProfile(path string) config.Bots {
	if path == "" {
		return config.DefaultBotProfile()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return config.DefaultBotProfile()
	}
	var profile config.Bots
	if json.Unmarshal(b, &profile) != nil {
		return config.DefaultBotProfile()
	}
	return profile
}

func (s *Server) handleFacts(w http.ResponseWriter, r *http.Request) {
	from, to := parseRange(r)
	if s.cfg.Stats == nil {
		writeJSON(w, http.StatusOK, store.Facts{Clicks: []store.ClickFact{}, Status: []store.StatusFact{}})
		return
	}
	conds, ok := condsFrom(w, r, "events")
	if !ok {
		return
	}
	var facts store.Facts
	var err error
	if len(conds) > 0 {
		facts, err = s.cfg.Stats.ClickFactsFiltered(r.Context(), from, to, conds)
	} else {
		facts, err = s.cfg.Stats.ClickFacts(r.Context(), from, to)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stats error")
		return
	}
	if facts.Clicks == nil {
		facts.Clicks = []store.ClickFact{}
	}
	if facts.Status == nil {
		facts.Status = []store.StatusFact{}
	}
	writeJSON(w, http.StatusOK, facts)
}
