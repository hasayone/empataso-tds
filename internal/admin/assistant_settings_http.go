package admin

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"

	"github.com/egerkuzma/emtds/internal/security"
)

// aiKeyView is the public state of one provider key.
type aiKeyView struct {
	HasKey bool   `json:"has_key"`
	Mask   string `json:"mask"`
}

// aiUsageUserView is the usage of one user in the settings view: the id, the login and
// the counters, nothing else about the account.
type aiUsageUserView struct {
	UID    string  `json:"uid"`
	Login  string  `json:"login"`
	Tokens int64   `json:"tokens"`
	USD    float64 `json:"usd"`
}

// aiUsageView is the usage of the current UTC day: the team total, every user, and Me, the
// usage of the user who asks.
type aiUsageView struct {
	Day    string            `json:"day"`
	Tokens int64             `json:"tokens"`
	USD    float64           `json:"usd"`
	Users  []aiUsageUserView `json:"users"`
	Me     aiUsageUserView   `json:"me"`
}

// aiLimitCeilings are the hard server ceilings of the per-turn limits, the upper bound of
// each field a PUT accepts (the lower bound is 1; these limits cannot be switched off).
type aiLimitCeilings struct {
	MaxOutputTokens int `json:"max_output_tokens"`
	MaxSteps        int `json:"max_steps"`
	TurnInputTokens int `json:"turn_input_tokens"`
}

// aiLimitMax are the largest values of the daily limits (0 means no limit): tokens for the
// two token limits, usd for the two dollar limits.
type aiLimitMax struct {
	Tokens int64   `json:"tokens"`
	USD    float64 `json:"usd"`
}

// aiSettingsView is the body of GET and PUT /api/ai/settings. No key is a field:
// Keys carries has_key and the mask. Prices holds the effective price of every model
// of allowed_models and of the default model, DefaultPrices the built-in one of the
// same models, PriceUnknown the models with no price. Error is set when ai.json
// could not be used. LimitCeilings and LimitMax are the bounds the panel shows next to the limits.
// Perms is the stored preset with the effective level of every resource in its resources, whatever
// the preset; PermResources lists those resources in panel order with the highest level of each.
type aiSettingsView struct {
	Error           string               `json:"error,omitempty"`
	Enabled         bool                 `json:"enabled"`
	Provider        string               `json:"provider"`
	Model           string               `json:"model"`
	AllowedModels   []string             `json:"allowed_models"`
	Keys            map[string]aiKeyView `json:"keys"`
	MaxOutputTokens int                  `json:"max_output_tokens"`
	MaxSteps        int                  `json:"max_steps"`
	TurnInputTokens int                  `json:"turn_input_tokens"`
	DailyTokens     int64                `json:"daily_tokens"`
	UserDailyTokens int64                `json:"user_daily_tokens"`
	DailyUSD        float64              `json:"daily_usd"`
	UserDailyUSD    float64              `json:"user_daily_usd"`
	Prices          map[string]aiPrice   `json:"prices"`
	DefaultPrices   map[string]aiPrice   `json:"default_prices"`
	PriceUnknown    []string             `json:"price_unknown"`
	UsageToday      aiUsageView          `json:"usage_today"`
	Beta            bool                 `json:"beta"`
	TestEndpoint    bool                 `json:"test_endpoint"`
	LimitCeilings   aiLimitCeilings      `json:"limit_ceilings"`
	LimitMax        aiLimitMax           `json:"limit_max"`
	Perms           aiPerms              `json:"perms"`
	PermResources   []aiPermResource     `json:"perm_resources"`
}

// aiPricedModels returns the models the settings view prices: allowed_models and the
// default model, without duplicates.
func aiPricedModels(st aiSettings) []string {
	out := append([]string(nil), st.AllowedModels...)
	if st.Model != "" && !aiContains(out, st.Model) {
		out = append(out, st.Model)
	}
	return out
}

// aiSettingsView builds the view of st for the session sess, whose usage is usage_today.me.
// loadErr is the reason ai.json was not used, or nil.
func (s *Server) aiSettingsView(st aiSettings, loadErr error, sess security.Session) aiSettingsView {
	lim := st.limits()
	v := aiSettingsView{
		Enabled: st.Enabled, Provider: st.Provider, Model: st.Model, AllowedModels: st.AllowedModels,
		Keys:            make(map[string]aiKeyView, len(aiProviderIDs)),
		MaxOutputTokens: lim.MaxOutput, MaxSteps: lim.MaxSteps, TurnInputTokens: lim.TurnInput,
		DailyTokens: lim.DailyTokens, UserDailyTokens: lim.UserDailyTokens,
		DailyUSD: lim.DailyUSD, UserDailyUSD: lim.UserDailyUSD,
		Prices: map[string]aiPrice{}, DefaultPrices: map[string]aiPrice{}, PriceUnknown: []string{},
		UsageToday: s.aiUsageView(sess), Beta: true, TestEndpoint: s.cfg.AITestBaseURL != "",
		LimitCeilings: aiLimitCeilings{MaxOutputTokens: aiMaxOutputCap, MaxSteps: aiMaxStepsCap, TurnInputTokens: aiTurnInputCap},
		LimitMax:      aiLimitMax{Tokens: aiTokensCap, USD: aiUSDCap},
		Perms:         aiPerms{Preset: st.Perms.Preset, Resources: st.Perms.levels()},
		PermResources: aiPermResources,
	}
	if loadErr != nil {
		v.Error = loadErr.Error()
	}
	for _, id := range aiProviderIDs {
		key := st.Keys[id]
		v.Keys[id] = aiKeyView{HasKey: key != "", Mask: aiKeyMask(key)}
	}
	for _, m := range aiPricedModels(st) {
		if d, ok := s.aiDefaultPriceOf(st.Provider, m); ok {
			v.DefaultPrices[m] = d
		}
		if p, ok := s.aiPriceOf(st, m); ok {
			v.Prices[m] = p
		} else {
			v.PriceUnknown = append(v.PriceUnknown, m)
		}
	}
	sort.Strings(v.PriceUnknown)
	return v
}

// aiWriteErr writes the answer for a provider or settings error: an *aiError keeps its
// status, code and params (errCode recovers the code from its message), anything else is a 502
// with a fixed text.
func aiWriteErr(w http.ResponseWriter, err error) {
	var ae *aiError
	if errors.As(err, &ae) {
		writeErrParams(w, ae.status, ae.msg, ae.Params)
		return
	}
	writeErr(w, http.StatusBadGateway, "the provider call failed")
}

// aiSettingsGuard lets only a cookie session of an administrator through to the settings
// handlers, whatever the route rule says. It writes 403 and returns false for an API key
// session and for a user who is not an administrator.
func (s *Server) aiSettingsGuard(w http.ResponseWriter, r *http.Request) bool {
	sess := sessionFrom(r.Context())
	if sess.API || !s.isAdmin(sess) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}

// handleAISettingsGet returns the assistant settings without keys.
// A damaged ai.json is reported in "error" and the defaults are shown. It answers 403 to an
// API key and to a non-administrator.
func (s *Server) handleAISettingsGet(w http.ResponseWriter, r *http.Request) {
	if !s.aiSettingsGuard(w, r) {
		return
	}
	st, err := s.aiLoad()
	writeJSON(w, http.StatusOK, s.aiSettingsView(st, err, sessionFrom(r.Context())))
}

// handleAISettingsPut saves the settings to ai.json with mode 0600.
// The merge and the write happen under s.ai.mu, so two saves cannot interleave; a damaged
// file is replaced by the merge over the defaults. Answers: 400 ai_bad_request (params field and
// reason) for bad JSON, a bad field or an unknown provider; 400 ai_bad_model (params model and
// reason: not_allowed outside allowed_models, no_catalog without a catalog of the provider,
// unknown outside it, no_tools for a model known to lack tool support); the catalog is checked
// only for the models the save adds to the stored ones or all of them after a change of the
// provider, so a save that leaves the models alone never needs a catalog; a limit out of
// range (code ai_bad_limit: a per-turn limit outside 1..ceiling, a daily limit outside 0..max,
// 0 meaning no limit); 503 when no
// file is configured; 500 "save error" when the file cannot be written. Success is 200 with
// the body of GET. 403 goes to an API key and to a non-administrator. No key is echoed or logged.
func (s *Server) handleAISettingsPut(w http.ResponseWriter, r *http.Request) {
	if !s.aiSettingsGuard(w, r) {
		return
	}
	var body aiSettingsPut
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, aiSettingsBody)).Decode(&body); err != nil {
		aiWriteErr(w, aiBadJSON())
		return
	}
	if s.cfg.AIFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "ai settings unavailable")
		return
	}
	s.ai.mu.Lock()
	defer s.ai.mu.Unlock()
	cur, loadErr := s.aiLoad()
	if loadErr != nil {
		s.logWarn("ai settings file replaced", "reason", loadErr.Error())
	}
	next, err := aiMergeSettings(cur, body)
	if err == nil {
		err = s.aiCheckModels(cur, next)
	}
	if err != nil {
		var ae *aiError
		if errors.As(err, &ae) {
			aiWriteErr(w, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.aiSave(next); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	s.logInfo("ai settings saved")
	writeJSON(w, http.StatusOK, s.aiSettingsView(next, nil, sessionFrom(r.Context())))
}

// aiTestBody is the body of POST /api/ai/test. An empty or masked Key falls back to
// the stored key of the provider; an empty Provider to the stored provider.
type aiTestBody struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

// handleAITest lists the models the key can use and refreshes the model catalog of the provider;
// it saves nothing else.
// Answers: 403 for an API key and for a non-administrator; 400 for bad JSON, an unknown
// provider or a key that is not valid (ai_bad_request), a key that is missing (ai_bad_key); the
// provider errors ai_bad_key 400, ai_rate_limited 429, ai_provider_error 502 and ai_timeout 504; success is 200
// {"models": [aiModelInfo...]} and the catalog of the provider is refreshed from the answer.
func (s *Server) handleAITest(w http.ResponseWriter, r *http.Request) {
	if !s.aiSettingsGuard(w, r) {
		return
	}
	var body aiTestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, aiSettingsBody)).Decode(&body); err != nil {
		aiWriteErr(w, aiBadJSON())
		return
	}
	st, _ := s.aiLoad()
	if body.Provider == "" {
		body.Provider = st.Provider
	}
	if !aiKnownProvider(body.Provider) {
		aiWriteErr(w, aiBadProvider("provider"))
		return
	}
	key := st.Keys[body.Provider]
	if !aiKeyIsMask(body.Key) {
		clean, err := aiCleanKey("key", body.Provider, body.Key)
		if err != nil {
			aiWriteErr(w, err)
			return
		}
		key = clean
	}
	if key == "" {
		aiWriteErr(w, newAIError(http.StatusBadRequest, aiErrBadKey, "no API key is set for the provider"))
		return
	}
	cat, err := s.aiCatalogFor(r.Context(), body.Provider, key, true)
	if err != nil {
		code, _ := aiErrInfo(err)
		s.logWarn("ai key test failed", "provider", body.Provider, "code", code, "error", aiCleanText(err.Error(), key))
		aiWriteErr(w, err)
		return
	}
	s.logInfo("ai key test passed", "provider", body.Provider, "models", len(cat.Models))
	writeJSON(w, http.StatusOK, map[string]any{"models": aiModelsFilled(cat.Models)})
}

// aiStatusUsage is the usage of the asking user and of the whole team with their limits.
// A limit of 0 means no limit.
type aiStatusUsage struct {
	Tokens          int64   `json:"tokens"`
	USD             float64 `json:"usd"`
	LimitTokens     int64   `json:"limit_tokens"`
	LimitUSD        float64 `json:"limit_usd"`
	TeamTokens      int64   `json:"team_tokens"`
	TeamLimitTokens int64   `json:"team_limit_tokens"`
	TeamUSD         float64 `json:"team_usd"`
	TeamLimitUSD    float64 `json:"team_limit_usd"`
}

// aiStatusView is the body of GET /api/ai/status. Provider, Model, Models, ModelInfo and Usage
// are filled only when Allowed is true. ModelInfo describes the models of Models in the same order.
type aiStatusView struct {
	Enabled      bool            `json:"enabled"`
	Allowed      bool            `json:"allowed"`
	Beta         bool            `json:"beta"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Models       []string        `json:"models"`
	ModelInfo    []aiStatusModel `json:"model_info"`
	PriceUnknown bool            `json:"price_unknown"`
	Usage        *aiStatusUsage  `json:"usage,omitempty"`
	TestEndpoint bool            `json:"test_endpoint"`
}

// handleAIStatus tells the panel whether the assistant is on and open to the user.
// It is open to every signed-in session. A session that may not use the assistant gets
// only enabled, allowed and beta; the others also get the provider, the models and
// their own usage with the limits.
func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	st, err := s.aiLoad()
	v := aiStatusView{Enabled: err == nil && st.Enabled, Allowed: s.aiAllowed(sess), Beta: true, Models: []string{},
		ModelInfo: []aiStatusModel{}, TestEndpoint: s.cfg.AITestBaseURL != ""}
	if !v.Allowed {
		writeJSON(w, http.StatusOK, v)
		return
	}
	lim := st.limits()
	v.Provider, v.Model = st.Provider, st.Model
	v.Models = st.AllowedModels
	v.ModelInfo = s.aiStatusModels(st)
	if st.Model != "" {
		_, known := s.aiPriceOf(st, st.Model)
		v.PriceUnknown = !known
	}
	_, total, users := s.aiUsageSnapshot()
	mine := users[s.aiUsageUID(sess)]
	v.Usage = &aiStatusUsage{
		Tokens: mine.Tokens, USD: aiRoundUSD(mine.USD), LimitTokens: lim.UserDailyTokens, LimitUSD: lim.UserDailyUSD,
		TeamTokens: total.Tokens, TeamUSD: aiRoundUSD(total.USD), TeamLimitTokens: lim.DailyTokens, TeamLimitUSD: lim.DailyUSD,
	}
	writeJSON(w, http.StatusOK, v)
}

// aiRoundUSD rounds an amount of dollars to six decimals so float sums print cleanly.
func aiRoundUSD(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
