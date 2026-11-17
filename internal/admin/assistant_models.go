package admin

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hasayone/empataso-tds/internal/security"
)

// aiModelsTTL is how long a fetched model catalog is served without asking the provider.
const aiModelsTTL = time.Hour

// aiModelInfo is one model of a provider catalog. Context is the context window in tokens, 0
// when unknown. InputPerM and OutputPerM are US dollars per million tokens, nil when the
// provider lists no price (only OpenRouter does). Tools is true or false when the provider
// states whether the model can call tools and nil when it does not say: OpenRouter states it
// through supported_parameters, the other providers never do, so their models stay allowed.
type aiModelInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Context    int      `json:"context"`
	InputPerM  *float64 `json:"input_per_m"`
	OutputPerM *float64 `json:"output_per_m"`
	Tools      *bool    `json:"tools"`
}

// aiCatalog is the model list of one provider as it was fetched. It is also the content of
// assistant/models-<provider>.json.
type aiCatalog struct {
	Provider  string        `json:"provider"`
	FetchedAt time.Time     `json:"fetched_at"`
	Models    []aiModelInfo `json:"models"`
}

// aiModelsState keeps the model catalogs in memory. mu guards cat; fetchMu makes one fetch at a
// time, so two requests for a stale catalog ask the provider once. now replaces the clock in
// tests; nil means the system clock.
type aiModelsState struct {
	mu      sync.Mutex
	fetchMu sync.Mutex
	cat     map[string]*aiCatalog
	now     func() time.Time
}

// aiRawModel is a model of a provider answer {"data":[...]}. Providers fill different parts:
// every one gives id, Anthropic display_name, OpenRouter name, context_length, pricing and
// supported_parameters.
type aiRawModel struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	DisplayName         string   `json:"display_name"`
	ContextLength       int      `json:"context_length"`
	SupportedParameters []string `json:"supported_parameters"`
	Pricing             *struct {
		Prompt     json.RawMessage `json:"prompt"`
		Completion json.RawMessage `json:"completion"`
	} `json:"pricing"`
}

// aiPerMillion converts a price per token, a JSON string or number such as "0.000003", to
// dollars per million tokens. It returns nil for a missing, negative or invalid value (OpenRouter
// prices its routers at -1) so such a price is never used.
func aiPerMillion(raw json.RawMessage) *float64 {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil
	}
	v = aiRoundUSD(v * 1e6)
	return &v
}

// aiParseModels turns the raw models of an answer into aiModelInfo, sorted by id and without
// duplicates. strip is removed from the start of every id. rich reads context, pricing and
// tool support too (OpenRouter); otherwise only the id and the name are taken. A model with an
// empty id is dropped.
func aiParseModels(raw []aiRawModel, strip string, rich bool) []aiModelInfo {
	seen := make(map[string]struct{}, len(raw))
	out := make([]aiModelInfo, 0, len(raw))
	for _, m := range raw {
		id := strings.TrimPrefix(strings.TrimSpace(m.ID), strip)
		if _, dup := seen[id]; id == "" || dup {
			continue
		}
		seen[id] = struct{}{}
		info := aiModelInfo{ID: id, Name: strings.TrimSpace(m.Name)}
		if info.Name == "" {
			info.Name = strings.TrimSpace(m.DisplayName)
		}
		if rich {
			info.Context = max(m.ContextLength, 0)
			if m.Pricing != nil {
				info.InputPerM, info.OutputPerM = aiPerMillion(m.Pricing.Prompt), aiPerMillion(m.Pricing.Completion)
				if info.InputPerM == nil || info.OutputPerM == nil {
					info.InputPerM, info.OutputPerM = nil, nil
				}
			}
			if m.SupportedParameters != nil {
				tools := aiContains(m.SupportedParameters, "tools")
				info.Tools = &tools
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// aiModelList reads the model list answer at path with the client c and parses it with
// aiParseModels. It returns the *aiError of the client for a failed call.
func aiModelList(ctx context.Context, c *aiClient, path, strip string, rich bool) ([]aiModelInfo, error) {
	var res struct {
		Data []aiRawModel `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &res); err != nil {
		return nil, err
	}
	return aiParseModels(res.Data, strip, rich), nil
}

// aiCatalogPath returns the file of the catalog of provider, or "" when the assistant has no
// directory.
func (s *Server) aiCatalogPath(provider string) string {
	if s.cfg.AIDir == "" || !aiKnownProvider(provider) {
		return ""
	}
	return filepath.Join(s.cfg.AIDir, "models-"+provider+".json")
}

// aiModelsNow returns the time of the catalog clock.
func (s *Server) aiModelsNow() time.Time {
	if s.ai.models.now != nil {
		return s.ai.models.now()
	}
	return time.Now()
}

// aiCatalogGet returns the last catalog of provider, from memory or else from its file, or nil
// when there is none or the file is unusable. The age is not checked: this is "the last
// catalog" that the settings and the prices are checked against. The result is read-only.
func (s *Server) aiCatalogGet(provider string) *aiCatalog {
	m := &s.ai.models
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.cat[provider]; c != nil {
		return c
	}
	path := s.aiCatalogPath(provider)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c aiCatalog
	if json.Unmarshal(raw, &c) != nil || c.Provider != provider {
		return nil
	}
	if m.cat == nil {
		m.cat = map[string]*aiCatalog{}
	}
	m.cat[provider] = &c
	return &c
}

// aiCatalogPut keeps c in memory and writes assistant/models-<provider>.json with mode 0600,
// atomically. A failed write is logged and the memory copy stays.
func (s *Server) aiCatalogPut(c *aiCatalog) {
	m := &s.ai.models
	m.mu.Lock()
	if m.cat == nil {
		m.cat = map[string]*aiCatalog{}
	}
	m.cat[c.Provider] = c
	m.mu.Unlock()
	path := s.aiCatalogPath(c.Provider)
	if path == "" {
		return
	}
	raw, err := json.Marshal(c)
	if err == nil {
		if err = os.MkdirAll(s.cfg.AIDir, 0o700); err == nil {
			err = writeFileAtomic(path, append(raw, '\n'), 0o600)
		}
	}
	if err != nil {
		s.logWarn("ai model catalog not saved", "provider", c.Provider)
	}
}

// aiCatalogFresh reports whether c was fetched less than aiModelsTTL ago.
func (s *Server) aiCatalogFresh(c *aiCatalog) bool {
	age := s.aiModelsNow().Sub(c.FetchedAt)
	return age >= 0 && age < aiModelsTTL
}

// aiCatalogFor returns the catalog of provider. Without force a catalog younger than the TTL is
// returned as is; otherwise the provider is asked with key, and the answer replaces the memory
// copy and the file. One fetch runs at a time. It returns the error of the provider call
// (ai_bad_key, ai_rate_limited, ai_provider_error, ai_timeout) and keeps the old catalog then.
func (s *Server) aiCatalogFor(ctx context.Context, provider, key string, force bool) (*aiCatalog, error) {
	if c := s.aiCatalogGet(provider); !force && c != nil && s.aiCatalogFresh(c) {
		return c, nil
	}
	s.ai.models.fetchMu.Lock()
	defer s.ai.models.fetchMu.Unlock()
	if c := s.aiCatalogGet(provider); !force && c != nil && s.aiCatalogFresh(c) {
		return c, nil
	}
	prov, err := s.aiNewProvider(provider, key)
	if err != nil {
		return nil, err
	}
	models, err := prov.Models(ctx)
	if err != nil {
		return nil, err
	}
	c := &aiCatalog{Provider: provider, FetchedAt: s.aiModelsNow().UTC(), Models: models}
	s.aiCatalogPut(c)
	return c, nil
}

// aiModelsFilled returns the models of a catalog for an answer: a model without a catalog price
// gets the built-in price when the table has one, so the panel shows what the accounting uses.
// The catalog itself is not changed.
func aiModelsFilled(models []aiModelInfo) []aiModelInfo {
	out := make([]aiModelInfo, len(models))
	copy(out, models)
	for i := range out {
		if out[i].InputPerM != nil && out[i].OutputPerM != nil {
			continue
		}
		if p, ok := aiPriceTable(out[i].ID); ok {
			in, outp := p.InputPerM, p.OutputPerM
			out[i].InputPerM, out[i].OutputPerM = &in, &outp
		}
	}
	return out
}

// aiCatalogView is the body of GET /api/ai/models.
type aiCatalogView struct {
	Provider   string        `json:"provider"`
	FetchedAt  string        `json:"fetched_at"`
	TTLSeconds int           `json:"ttl_seconds"`
	Models     []aiModelInfo `json:"models"`
}

// aiCatalogViewOf builds the answer for c.
func aiCatalogViewOf(c *aiCatalog) aiCatalogView {
	return aiCatalogView{
		Provider: c.Provider, FetchedAt: c.FetchedAt.UTC().Format(time.RFC3339),
		TTLSeconds: int(aiModelsTTL / time.Second), Models: aiModelsFilled(c.Models),
	}
}

// Reasons of an ai_bad_model error, the "reason" param next to the "model" param.
const (
	// aiModelNoCatalog: the provider has no model list yet, so the model cannot be checked.
	aiModelNoCatalog = "no_catalog"
	// aiModelNoTools: the provider states that the model cannot call tools.
	aiModelNoTools = "no_tools"
	// aiModelUnknown: the model is not in the provider list.
	aiModelUnknown = "unknown"
	// aiModelNotAllowed: the model is not in allowed_models.
	aiModelNotAllowed = "not_allowed"
)

// aiBadModelErr builds the 400 ai_bad_model error; text is the English fallback, model is the
// model id the client puts into its own text and reason is one of the aiModel* reasons.
func aiBadModelErr(text, model, reason string) *aiError {
	return newAIErrorP(http.StatusBadRequest, aiErrBadModel, text, map[string]any{"model": model, "reason": reason})
}

// aiNewModels returns the models of next that cur does not already hold: the allowed models and
// the default model, in that order, without duplicates. When the provider is the same as in cur,
// a model stored in cur's allowed_models or model is not new; after a change of the provider
// every model is new, because the old check was made against another catalog.
func aiNewModels(cur, next aiSettings) []string {
	var old []string
	if cur.Provider == next.Provider {
		old = append(append(old, cur.AllowedModels...), cur.Model)
	}
	var out []string
	for _, id := range append(append([]string(nil), next.AllowedModels...), next.Model) {
		if id != "" && !aiContains(old, id) && !aiContains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// aiCheckModels checks the models a save adds to the stored settings cur against the last
// catalog of the provider of next: each must be listed and must not be known to lack tool
// support. It returns an ai_bad_model error naming the first model that fails, with the reason
// no_catalog when the provider has no catalog (which asks the administrator to check the key
// first), unknown or no_tools. Models cur already holds are not checked, so a save that does not
// touch the models never fails for a missing catalog. No network call is made.
func (s *Server) aiCheckModels(cur, next aiSettings) error {
	ids := aiNewModels(cur, next)
	if len(ids) == 0 {
		return nil
	}
	cat := s.aiCatalogGet(next.Provider)
	if cat == nil {
		return aiBadModelErr("the model list of the provider is not loaded yet: check the API key first", ids[0], aiModelNoCatalog)
	}
	known := make(map[string]aiModelInfo, len(cat.Models))
	for _, m := range cat.Models {
		known[m.ID] = m
	}
	for _, id := range ids {
		m, ok := known[id]
		switch {
		case !ok:
			return aiBadModelErr("the model is not in the provider list", id, aiModelUnknown)
		case m.Tools != nil && !*m.Tools:
			return aiBadModelErr("the model cannot call tools", id, aiModelNoTools)
		}
	}
	return nil
}

// aiDefaultPriceOf returns the price without the administrator overrides: the catalog price of
// the model at provider when the catalog lists one, otherwise the built-in table.
func (s *Server) aiDefaultPriceOf(provider, model string) (aiPrice, bool) {
	if c := s.aiCatalogGet(provider); c != nil {
		for _, m := range c.Models {
			if m.ID == model && m.InputPerM != nil && m.OutputPerM != nil {
				return aiPrice{InputPerM: *m.InputPerM, OutputPerM: *m.OutputPerM}, true
			}
		}
	}
	return aiPriceTable(model)
}

// aiPriceOf returns the effective price of model for the settings st and whether one is known.
// The order is: the administrator price, the catalog price of st.Provider, the built-in table.
func (s *Server) aiPriceOf(st aiSettings, model string) (aiPrice, bool) {
	if p, ok := aiPriceOverride(model, st.Prices); ok {
		return p, true
	}
	return s.aiDefaultPriceOf(st.Provider, model)
}

// aiModelsQuery returns the provider named by the query of a request, or the stored provider
// when none is named. ok is false when the name is not a known provider.
func aiModelsQuery(q url.Values, stored string) (provider string, ok bool) {
	provider = strings.TrimSpace(q.Get("provider"))
	if provider == "" {
		provider = stored
	}
	return provider, aiKnownProvider(provider)
}

// handleAIModels returns the model catalog of a provider.
// The query holds provider (default: the stored one) and refresh=1 to ask the provider now;
// without it a catalog younger than an hour is served from memory or the file. It answers 403 to
// an API key and to a non-administrator, 400 ai_bad_request for an unknown provider and ai_bad_key 400 when the
// provider has no stored key; the provider errors are ai_bad_key 400, ai_rate_limited 429,
// ai_provider_error 502 and ai_timeout 504. Success is 200 with aiCatalogView.
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	if !s.aiSettingsGuard(w, r) {
		return
	}
	st, _ := s.aiLoad()
	provider, ok := aiModelsQuery(r.URL.Query(), st.Provider)
	if !ok {
		aiWriteErr(w, aiBadProvider("provider"))
		return
	}
	key := st.Keys[provider]
	if key == "" {
		aiWriteErr(w, newAIError(http.StatusBadRequest, aiErrBadKey, "no API key is set for the provider"))
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	cat, err := s.aiCatalogFor(r.Context(), provider, key, refresh)
	if err != nil {
		aiWriteErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, aiCatalogViewOf(cat))
}

// aiStatusModel is one allowed model in GET /api/ai/status. Name, Context and Tools come from the
// last catalog of the provider and stay "", 0 and null when the catalog does not list the model.
// InputPerM and OutputPerM are the effective price in US dollars per million tokens (see
// aiPriceOf) and null when there is none; PriceUnknown is true then, the daily USD limits do not
// count the model.
type aiStatusModel struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Context      int      `json:"context"`
	InputPerM    *float64 `json:"input_per_m"`
	OutputPerM   *float64 `json:"output_per_m"`
	Tools        *bool    `json:"tools"`
	PriceUnknown bool     `json:"price_unknown"`
}

// aiStatusModels describes every allowed model of st for the panel, in the order of allowed_models.
// It reads only the catalog already held in memory or in its file and never asks the provider.
// The result is never nil.
func (s *Server) aiStatusModels(st aiSettings) []aiStatusModel {
	known := map[string]aiModelInfo{}
	if cat := s.aiCatalogGet(st.Provider); cat != nil {
		for _, m := range cat.Models {
			known[m.ID] = m
		}
	}
	out := make([]aiStatusModel, 0, len(st.AllowedModels))
	for _, id := range st.AllowedModels {
		m := known[id]
		v := aiStatusModel{ID: id, Name: m.Name, Context: m.Context, Tools: m.Tools}
		if p, ok := s.aiPriceOf(st, id); ok {
			in, outp := p.InputPerM, p.OutputPerM
			v.InputPerM, v.OutputPerM = &in, &outp
		} else {
			v.PriceUnknown = true
		}
		out = append(out, v)
	}
	return out
}

// aiMyUsage returns the usage of the day of the session sess: its id, login and counters.
func (s *Server) aiMyUsage(sess security.Session) aiUsageUserView {
	uid := s.aiUsageUID(sess)
	_, _, users := s.aiUsageSnapshot()
	t := users[uid]
	return aiUsageUserView{UID: uid, Login: s.aiUsageLogin(uid), Tokens: t.Tokens, USD: aiRoundUSD(t.USD)}
}
